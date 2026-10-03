// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package engine generates Terraform configuration for existing resources by
// running Terraform itself. It writes `import` blocks and lets
// `terraform plan -generate-config-out` produce the resource configuration.
// It never writes state and never applies.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"
)

// Files the engine writes into each output directory.
const (
	ProvidersFileName = "providers.tf"
	ImportsFileName   = "imports.tf"
	GeneratedFileName = "generated.tf"
)

// Terraform is the subset of *tfexec.Terraform the engine uses.
type Terraform interface {
	Init(ctx context.Context, opts ...tfexec.InitOption) error
	Plan(ctx context.Context, opts ...tfexec.PlanOption) (bool, error)
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

// Generate writes providers and imports into dir, then runs Terraform to
// generate the configuration of every imported resource into generated.tf.
// If fixup (optional) changes the generated configuration, Generate plans
// again to check the result. It refuses to overwrite an existing
// generated.tf.
func Generate(ctx context.Context, tf Terraform, dir string, providers []byte, imports []Import, fixup Fixup) error {
	if len(imports) == 0 {
		return errors.New("no resources to import")
	}
	generated := filepath.Join(dir, GeneratedFileName)
	if _, err := os.Stat(generated); err == nil {
		return fmt.Errorf("%s already exists; use an empty output directory", generated)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	importsHCL, err := ImportsFile(imports)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, content := range map[string][]byte{ProvidersFileName: providers, ImportsFileName: importsHCL} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			return err
		}
	}
	if err := tf.Init(ctx); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}
	// Plan reports changes because every import is pending; only errors matter.
	// Terraform writes generated.tf even when the generated configuration is
	// invalid, so fixups can still repair it.
	_, planErr := tf.Plan(ctx, tfexec.GenerateConfigOut(GeneratedFileName))
	if planErr != nil {
		planErr = fmt.Errorf("terraform plan: %w", planErr)
	}
	if fixup == nil {
		return planErr
	}
	fixed, err := fixGenerated(generated, fixup)
	if errors.Is(err, fs.ErrNotExist) {
		return planErr
	}
	if err != nil || !fixed {
		return errors.Join(planErr, err)
	}
	if _, err := tf.Plan(ctx); err != nil {
		return fmt.Errorf("terraform plan of the repaired configuration: %w", err)
	}
	return nil
}

// fixGenerated applies fixup to every resource block in path and rewrites the
// file if anything changed.
func fixGenerated(path string, fixup Fixup) (bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	f, diags := hclwrite.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return false, fmt.Errorf("parse %s: %w", path, diags)
	}
	fixed := false
	for _, block := range f.Body().Blocks() {
		if block.Type() == "resource" && len(block.Labels()) == 2 && fixup(block.Labels()[0], block.Body()) {
			fixed = true
		}
	}
	if !fixed {
		return false, nil
	}
	return true, os.WriteFile(path, hclwrite.Format(f.Bytes()), 0o644)
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
