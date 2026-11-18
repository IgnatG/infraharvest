// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// WithEnvVars returns tf with each plan reading the TF_VAR_<name>
// environment variables of the process for the variables the root in dir
// declares. tfexec refuses TF_VAR_ variables in a runner's environment (see
// terraformEnviron), and values on the command line show in `ps`, so each
// plan gets them from a var file in a private directory (mode 0600), which
// goes as soon as the plan ends. Terraform's precedence holds: the var
// files and -var options a plan has win over it, and so do the root's
// terraform.tfvars and *.auto.tfvars files, whose variables it leaves out.
func WithEnvVars(tf DriftTerraform, dir string) DriftTerraform {
	return &envVarTerraform{DriftTerraform: tf, dir: dir, lookup: os.LookupEnv}
}

// envVarTerraform is a Terraform that gives its plans the environment's
// TF_VAR_ values (see WithEnvVars).
type envVarTerraform struct {
	DriftTerraform
	dir    string
	lookup func(string) (string, bool)
}

// PlanJSON plans with a var file of the environment's values first, which
// is removed when the plan ends.
func (t *envVarTerraform) PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	path, remove, err := t.varFile()
	if err != nil {
		return false, err
	}
	defer remove()
	if path != "" {
		opts = append([]tfexec.PlanOption{tfexec.VarFile(path)}, opts...)
	}
	return t.DriftTerraform.PlanJSON(ctx, w, opts...)
}

// varFile writes the var file of the TF_VAR_ values for the root's
// declared variables, in a directory only the process can read, and
// returns its path, or "" if there are none, and what removes it. A value
// that isn't valid for its variable's type fails the plan without being
// quoted.
func (t *envVarTerraform) varFile() (string, func(), error) {
	files, err := hclfile.ConfigFiles(t.dir)
	if err != nil {
		return "", func() {}, err
	}
	skip := varFileNames(t.dir)
	body := hclwrite.NewEmptyFile().Body()
	var names []string
	types := map[string]string{}
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			if b.Type != "variable" || len(b.Labels) != 1 || skip[b.Labels[0]] {
				continue
			}
			name := b.Labels[0]
			if _, ok := t.lookup("TF_VAR_" + name); !ok {
				continue
			}
			names = append(names, name)
			if a, ok := b.Body.Attributes["type"]; ok {
				rng := a.Expr.Range()
				types[name] = strings.TrimSpace(string(f.Src[rng.Start.Byte:rng.End.Byte]))
			}
			skip[name] = true
		}
	}
	if len(names) == 0 {
		return "", func() {}, nil
	}
	sort.Strings(names)
	var raw []byte
	for _, name := range names {
		value, _ := t.lookup("TF_VAR_" + name)
		if typ, typed := types[name]; !typed || typ == "string" {
			body.SetAttributeValue(name, cty.StringVal(value))
			continue
		}
		// Any other type takes the value as an HCL expression, as
		// Terraform reads it from the environment.
		if _, diags := hclsyntax.ParseExpression([]byte(value), "TF_VAR_"+name, hcl.InitialPos); diags.HasErrors() {
			return "", func() {}, fmt.Errorf("TF_VAR_%s is not a valid value for its variable's type, %s", name, types[name])
		}
		raw = append(raw, name+" = "+value+"\n"...)
	}
	private, err := MkdirTempHeld(EnvVarsPattern)
	if err != nil {
		return "", func() {}, err
	}
	remove := func() { RemoveHeld(private) }
	// The mode is 0700 already; this also covers Windows, which the mode
	// doesn't.
	if _, err := fsutil.MakePrivate(private); err != nil {
		remove()
		return "", func() {}, err
	}
	path := filepath.Join(private, "env.tfvars")
	if err := os.WriteFile(path, hclwrite.Format(append(body.BuildTokens(nil).Bytes(), raw...)), 0o600); err != nil {
		remove()
		return "", func() {}, err
	}
	return path, remove, nil
}

// varFileNames returns the variables the root's terraform.tfvars,
// terraform.tfvars.json and *.auto.tfvars(.json) files set, which
// Terraform reads after the environment.
func varFileNames(dir string) map[string]bool {
	names := map[string]bool{}
	var paths []string
	for _, pattern := range []string{"terraform.tfvars", "terraform.tfvars.json", "*.auto.tfvars", "*.auto.tfvars.json"} {
		found, _ := filepath.Glob(filepath.Join(dir, pattern))
		paths = append(paths, found...)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, ".json") {
			var values map[string]json.RawMessage
			if src, err := os.ReadFile(path); err == nil && json.Unmarshal(src, &values) == nil {
				for name := range values {
					names[name] = true
				}
			}
			continue
		}
		if f, err := hclfile.ReadVarFile(path); err == nil && f != nil {
			for name := range f.Syntax.Attributes {
				names[name] = true
			}
		}
	}
	return names
}
