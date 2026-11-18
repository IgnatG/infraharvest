// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package secret

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// Values holds the values of the secret variables infraharvest
// found in the configuration, for its own plans: in a .tfvars.json file in
// a private temporary directory outside the output, which Remove deletes.
// They are never written into the output.
type Values struct {
	dir  string
	path string // "" while no secret has a value
}

func NewValues() (*Values, error) {
	// What a run killed before it could remove its own goes, once per
	// process.
	removeStaleOnce.Do(func() { removeStaleSecretValues(os.TempDir(), time.Now().Add(-staleSecretsAge)) })
	dir, err := os.MkdirTemp("", secretValuesPattern)
	if err != nil {
		return nil, err
	}
	return &Values{dir: dir}, nil
}

// The temporary directories of Values, and the file in each.
const (
	secretValuesPattern = "infraharvest-secrets-"
	secretValuesFile    = "secrets.tfvars.json" //nolint:gosec // G101: a file name, not a credential
)

// staleSecretsAge is how old a Values directory is when no run uses
// it any more: a run that was killed left it.
const staleSecretsAge = 24 * time.Hour

// removeStaleOnce has NewValues remove stale directories once per
// process.
var removeStaleOnce sync.Once

// removeStaleSecretValues removes the Values directories in parent
// that neither changed nor had their values file change since before.
func removeStaleSecretValues(parent string, before time.Time) {
	dirs, err := filepath.Glob(filepath.Join(parent, secretValuesPattern+"*"))
	if err != nil {
		return
	}
	for _, dir := range dirs {
		stale := true
		for _, path := range []string{dir, filepath.Join(dir, secretValuesFile)} {
			info, err := os.Stat(path)
			if err == nil && info.ModTime().After(before) {
				stale = false
			}
		}
		if info, err := os.Stat(dir); stale && err == nil && info.IsDir() {
			_ = os.RemoveAll(dir)
		}
	}
}

// Write replaces the file with the values of the secrets that have one.
func (s *Values) Write(secrets []Variable) error {
	values := valuesByVariable(secrets)
	if len(values) == 0 {
		s.path = ""
		return nil
	}
	content, err := json.Marshal(values)
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, secretValuesFile)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return err
	}
	s.path = path
	return nil
}

// valuesByVariable returns the values of the secrets that have one, by
// variable: what Values.Write writes.
func valuesByVariable(secrets []Variable) map[string]string {
	values := map[string]string{}
	for _, secret := range secrets {
		if secret.value != "" {
			values[secret.Variable] = secret.value
		}
	}
	return values
}

// SameValues reports whether a and b have the same values, by variable,
// for the secrets that have one: plans that read Values written with
// either get the same variables.
func SameValues(a, b []Variable) bool {
	return maps.Equal(valuesByVariable(a), valuesByVariable(b))
}

// Options are the plan options that set the variables, if any.
func (s *Values) Options() []tfexec.PlanOption {
	if s == nil || s.path == "" {
		return nil
	}
	return []tfexec.PlanOption{tfexec.VarFile(s.path)}
}

// Remove deletes the file and its directory.
func (s *Values) Remove() {
	if s != nil {
		_ = os.RemoveAll(s.dir)
	}
}

// Redact replaces the secret values in s, such as a diagnostic that quotes
// the value it rejects, with "(sensitive value)", in the escaped forms text
// quotes them in too (see TextForms).
func Redact(s string, secrets []Variable) string {
	return RedactText(s, TextForms(KnownValues(secrets)))
}

// RedactBytes is redact for file content.
func RedactBytes(b []byte, secrets []Variable) []byte {
	return []byte(Redact(string(b), secrets))
}

// readsLiteralSecrets reports whether Literals reads the file rel: a
// .tf or .hcl file, or a merged root's var file.
func readsLiteralSecrets(rel string) bool {
	return strings.HasSuffix(rel, ".tf") || strings.HasSuffix(rel, ".hcl") || hclfile.IsMergedVarFile(rel)
}

// Literals returns, for a .tf or .hcl file, a finding for each
// secret value secretsInBody finds in any of its blocks; and for a merged
// root's var file (see hclfile.IsMergedVarFile), in its arguments too, the values it
// sets, as secretsInBody judges a provider's arguments.
func Literals(f *hclfile.File) []string {
	rel, body := f.Path, f.Syntax
	if !readsLiteralSecrets(rel) {
		return nil
	}
	var findings []string
	if hclfile.IsMergedVarFile(rel) {
		names := make([]string, 0, len(body.Attributes))
		for name := range body.Attributes {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			// The values are those of the provider's arguments, which
			// variables are named after: judged as those are, while G4
			// fails a var file that sets a sensitive variable.
			for _, s := range secretsInExpr(body.Attributes[name].Expr, name, name, false, tagAttribute(name)) {
				findings = append(findings, fmt.Sprintf("%s: %s holds a secret value (%s); it belongs in a sensitive variable, set outside the file", rel, s.attribute, s.reason))
			}
		}
	}
	for _, b := range body.Blocks {
		what := strings.Join(append([]string{b.Type}, b.Labels...), ".")
		for _, f := range secretsInBody(b.Body, "") {
			findings = append(findings, fmt.Sprintf("%s: %s of %s holds a secret value (%s); it belongs in a sensitive variable", rel, f.attribute, what, f.reason))
		}
	}
	return findings
}

// KnownValues returns the values of the secrets infraharvest found, which
// no file may contain.
func KnownValues(secrets []Variable) []string {
	var values []string
	for _, s := range secrets {
		if len(s.value) >= minSecretLength {
			values = append(values, s.value)
		}
	}
	return values
}
