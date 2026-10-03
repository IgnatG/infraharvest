// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package engine generates Terraform configuration for existing resources by
// running Terraform itself. It writes `import` blocks and lets
// `terraform plan -generate-config-out` produce the resource configuration.
// It never writes state and never applies.
package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

// Files the engine writes into each output directory.
const (
	ProvidersFileName = "providers.tf"
	ImportsFileName   = "imports.tf"
	GeneratedFileName = "generated.tf"
	VariablesFileName = "variables.tf"
	// RejectedFileName holds resources left out of the configuration. It is
	// not a .tf file, so Terraform doesn't load it.
	RejectedFileName = "rejected.hcl"
	// LockFileName is Terraform's dependency lock file.
	LockFileName = ".terraform.lock.hcl"
)

// maxPlanRounds bounds how often Generate plans again after repairing the
// configuration or leaving resources out.
const maxPlanRounds = 3

// Terraform is the subset of *tfexec.Terraform the engine uses.
type Terraform interface {
	Init(ctx context.Context, opts ...tfexec.InitOption) error
	PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error)
	Validate(ctx context.Context) (*tfjson.ValidateOutput, error)
	ProvidersSchema(ctx context.Context) (*tfjson.ProviderSchemas, error)
	SetStdout(w io.Writer)
}

// NewTerraform returns a Terraform runner for dir that shares downloaded
// providers through pluginCacheDir. It creates both directories.
func NewTerraform(dir, execPath, pluginCacheDir string) (*tfexec.Terraform, error) {
	for _, d := range []string{dir, pluginCacheDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	tf, err := tfexec.NewTerraform(dir, execPath)
	if err != nil {
		return nil, err
	}
	// SetEnv replaces the inherited environment and rejects variables that
	// tfexec manages itself (TF_LOG, TF_CLI_ARGS, ...), so drop those.
	env := tfexec.CleanEnv(environ())
	env["TF_PLUGIN_CACHE_DIR"] = pluginCacheDir
	if err := tf.SetEnv(env); err != nil {
		return nil, err
	}
	return tf, nil
}

// Fixup repairs one generated resource block in place and reports whether
// it changed anything. Providers use it for quirks of their configuration
// generator; see terraformutils.ProviderWithConfigFixups.
type Fixup func(resourceType string, body *hclwrite.Body) bool

// Result is what Generate leaves for the user to finish.
type Result struct {
	// Secrets are the variables to set before planning.
	Secrets []Secret
	// Rejected are the resources left out of the configuration.
	Rejected []Rejection
}

// Generate writes providers and imports into dir, then runs Terraform to
// generate the configuration of every imported resource into generated.tf.
// If Terraform rejects what it generated, Generate repairs it (see repair),
// using fixup if not nil, and plans again. Resources it still can't plan
// are left out (see Rejection). Secret values Terraform doesn't write into
// the configuration become sensitive variables in variables.tf (see Secret).
//
// Generate fails if Terraform can't plan the directory at all. It refuses
// to overwrite an existing generated.tf.
func Generate(ctx context.Context, tf Terraform, dir string, providers []byte, imports []Import, fixup Fixup) (*Result, error) {
	if len(imports) == 0 {
		return nil, errors.New("no resources to import")
	}
	generated := filepath.Join(dir, GeneratedFileName)
	if _, err := os.Stat(generated); err == nil {
		return nil, fmt.Errorf("%s already exists; use an empty output directory", generated)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	importsHCL, err := ImportsFile(imports)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for name, content := range map[string][]byte{ProvidersFileName: providers, ImportsFileName: importsHCL} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			return nil, err
		}
	}
	if err := tf.Init(ctx); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	// Plan reports changes because every import is pending; only errors matter.
	diags, err := plan(ctx, tf, tfexec.GenerateConfigOut(GeneratedFileName))
	if err != nil {
		return nil, fmt.Errorf("terraform plan: %w", err)
	}
	if _, err := os.Stat(generated); errors.Is(err, fs.ErrNotExist) {
		// Terraform stopped before generating anything, for example on the
		// provider configuration.
		if len(diags) == 0 {
			return nil, errors.New("terraform plan generated no configuration")
		}
		return nil, fmt.Errorf("terraform plan: %w", diagnosticsError(diags))
	} else if err != nil {
		return nil, err
	}

	result := &Result{}
	var rejected bytes.Buffer
	repaired := false
	for round := 0; ; round++ {
		errs, err := unresolved(dir, diags)
		if err != nil {
			return nil, err
		}
		if len(errs) == 0 {
			break
		}
		if round == maxPlanRounds {
			var all []tfjson.Diagnostic
			for _, ds := range errs {
				all = append(all, ds...)
			}
			return nil, fmt.Errorf("terraform plan: %w", diagnosticsError(all))
		}
		// Terraform writes generated.tf even when the configuration it
		// generated is invalid. Repair it once; leave out what is left.
		changed := false
		if !repaired {
			repaired = true
			if changed, err = repair(ctx, tf, generated, fixup); err != nil {
				return nil, err
			}
		}
		if !changed {
			rejections, err := reject(dir, errs, &rejected)
			if err != nil {
				return nil, err
			}
			result.Rejected = append(result.Rejected, rejections...)
		}
		if diags, err = plan(ctx, tf); err != nil {
			return nil, fmt.Errorf("terraform plan: %w", err)
		}
	}
	// Terraform can't plan with the secret variables unset, so the resources
	// that use them are only validated from here on.
	if result.Secrets, err = useVariables(ctx, tf, dir, &result.Rejected, &rejected); err != nil {
		return nil, err
	}
	if rejected.Len() > 0 {
		if err := os.WriteFile(filepath.Join(dir, RejectedFileName), rejected.Bytes(), 0o644); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// unresolved returns the errors in diags by resource, leaving out errors
// about secret attributes, which variables resolve (see useVariables). It
// fails on errors it can't attribute to a resource.
func unresolved(dir string, diags []tfjson.Diagnostic) (map[string][]tfjson.Diagnostic, error) {
	if len(diags) == 0 {
		return nil, nil
	}
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	imports, err := loadHCL(filepath.Join(dir, ImportsFileName))
	if err != nil {
		return nil, err
	}
	errs, general := byResource(diags, generated, imports)
	if len(general) > 0 {
		return nil, fmt.Errorf("terraform plan: %w", diagnosticsError(diags))
	}
	secrets := findSecrets(generated)
	for addr, ds := range errs {
		var left []tfjson.Diagnostic
		for _, d := range ds {
			if !aboutSecret(d, secrets[addr]) {
				left = append(left, d)
			}
		}
		if len(left) == 0 {
			delete(errs, addr)
		} else {
			errs[addr] = left
		}
	}
	return errs, nil
}

// useVariables makes the secret attributes in generated.tf read from
// sensitive variables, declared in variables.tf, and validates the result.
// It leaves out resources that still don't validate, adding them to
// rejections and out.
func useVariables(ctx context.Context, tf Terraform, dir string, rejections *[]Rejection, out *bytes.Buffer) ([]Secret, error) {
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	found := findSecrets(generated)
	if len(found) == 0 {
		return nil, nil
	}
	secrets := secretsToVariables(generated, found)
	if err := generated.save(); err != nil {
		return nil, err
	}
	schemas, err := tf.ProvidersSchema(ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform providers schema: %w", err)
	}
	if err := writeVariables(dir, secrets, schemas); err != nil {
		return nil, err
	}

	validation, err := tf.Validate(ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform validate: %w", err)
	}
	if validation.Valid {
		return secrets, nil
	}
	errs, err := unresolved(dir, errorDiagnostics(validation.Diagnostics))
	if err != nil {
		return nil, err
	}
	rejected, err := reject(dir, errs, out)
	if err != nil {
		return nil, err
	}
	*rejections = append(*rejections, rejected...)
	kept := secrets[:0]
	for _, s := range secrets {
		if _, gone := errs[s.Address]; !gone {
			kept = append(kept, s)
		}
	}
	return kept, writeVariables(dir, kept, schemas)
}

// writeVariables writes variables.tf, or removes it if there are no secrets.
func writeVariables(dir string, secrets []Secret, schemas *tfjson.ProviderSchemas) error {
	path := filepath.Join(dir, VariablesFileName)
	if len(secrets) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	content, err := variablesFile(secrets, schemas)
	if err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

// environ returns the current environment as a map.
func environ() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		for i := 1; i < len(kv); i++ { // start at 1: Windows has "=C:=C:\" entries
			if kv[i] == '=' {
				env[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	return env
}
