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
	"strings"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/adapters"
)

// Files the engine writes into each output directory.
const (
	VersionsFileName  = "versions.tf"
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
	ShowPlanFile(ctx context.Context, planPath string, opts ...tfexec.ShowOption) (*tfjson.Plan, error)
	FormatCheck(ctx context.Context, opts ...tfexec.FormatOption) (bool, []string, error)
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

// Result is what Generate leaves for the user to finish.
type Result struct {
	// Imported are the resources the configuration imports, with the
	// labels Generate gave them.
	Imported []Import
	// Secrets are the variables to set before planning.
	Secrets []Secret
	// Rejected are the resources left out of the configuration.
	Rejected []Rejection
	// Gate is the result of the verification gate (see runGate).
	Gate Gate
	// Modules are the module calls the configuration makes, and the
	// clusters of resources Generate didn't move into one, with why.
	Modules []ModuleCall
}

// Options configure Generate.
type Options struct {
	// Config are files to write besides imports.tf, such as versions.tf and
	// providers.tf, by name.
	Config map[string][]byte
	// Omit names, per resource type, arguments to leave out of the generated
	// configuration because another imported resource manages them, such as
	// an S3 bucket's versioning. They must be computed, so that leaving them
	// out changes no plan.
	Omit map[string][]string
	// DefaultTags, if set, lets Generate move the tags every resource shares
	// into local.tags, applied through the provider (see applyTagLift).
	DefaultTags *DefaultTags
	// StateOnly names, per resource type, arguments the provider keeps only
	// in state, which import can't set and the plan check (see runGate)
	// lets change.
	StateOnly map[string][]string
	// ModulesDir, if set, is where Generate puts local modules for clusters
	// of resources that repeat with the same shape (see modularize).
	ModulesDir string
	// Adapters map clusters of resources onto calls of curated modules,
	// tried before generated local modules (see synthesize).
	Adapters []adapters.Adapter
	// External are resources the import listed but doesn't manage, which
	// the configuration may refer to through the data sources in
	// DataSources, by resource type (see addDataSources).
	External    []External
	DataSources map[string]DataSource
	// Scanners run in the verification gate (see runScanners).
	Scanners []Scanner
	// Taken are names Generate leaves alone, such as those of the root an
	// incremental import adds to (see Add).
	Taken Names
}

// Generate writes opts.Config and an import block per resource into dir,
// then runs Terraform to generate the configuration of every imported
// resource into generated.tf, without the arguments opts.Omit names.
// Resource names become labels (see Label). If Terraform
// rejects what it generated, Generate repairs it (see repair) and plans
// again. Resources it still can't plan are left out (see Rejection). Secret
// values Terraform doesn't write into the configuration become sensitive
// variables in variables.tf (see Secret). Last, it writes a README with
// what is left to do.
//
// Generate fails if Terraform can't plan the directory at all. It refuses
// to overwrite an existing generated.tf.
func Generate(ctx context.Context, tf Terraform, dir string, imports []Import, opts Options) (*Result, error) {
	if len(imports) == 0 {
		return nil, errors.New("no resources to import")
	}
	generated := filepath.Join(dir, GeneratedFileName)
	if _, err := os.Stat(generated); err == nil {
		return nil, fmt.Errorf("%s already exists; use an empty output directory", generated)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	imports = labelled(imports, opts.Taken)
	importsHCL, err := ImportsFile(imports)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	files := map[string][]byte{ImportsFileName: importsHCL}
	for name, content := range opts.Config {
		files[name] = content
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			return nil, err
		}
	}
	if dt := opts.DefaultTags; dt != nil && dt.Applied != nil {
		if err := applyDefaultTags(dir, *dt); err != nil {
			return nil, err
		}
	}
	if err := tf.Init(ctx); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	// Plan reports changes because every import is pending; only errors matter.
	diags, _, err := plan(ctx, tf, tfexec.GenerateConfigOut(GeneratedFileName))
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
	// Terraform writes resources in the order its graph walk reaches them,
	// which differs between runs: sort them, so the same estate always gives
	// the same files. Sorting and leaving out what other resources manage
	// move the lines the errors point at, so plan again.
	sorted, err := sortResources(generated)
	if err != nil {
		return nil, err
	}
	omitted, err := omitArguments(generated, opts.Omit)
	if err != nil {
		return nil, err
	}
	if sorted || omitted {
		if diags, _, err = plan(ctx, tf); err != nil {
			return nil, fmt.Errorf("terraform plan: %w", err)
		}
	}

	result := &Result{}
	var rejected bytes.Buffer
	repaired := false
	for round := 0; ; round++ {
		errs, err := unresolved("terraform plan", dir, diags)
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
			if changed, err = repair(ctx, tf, generated); err != nil {
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
		if diags, _, err = plan(ctx, tf); err != nil {
			return nil, fmt.Errorf("terraform plan: %w", err)
		}
	}
	if result.Secrets, err = useVariables(ctx, tf, dir, opts.Taken, &result.Rejected, &rejected); err != nil {
		return nil, err
	}
	before, err := importTargets(dir)
	if err != nil {
		return nil, err
	}
	declined, err := postProcess(ctx, tf, dir, opts, result.Secrets)
	if err != nil {
		return nil, err
	}
	moved, err := movedAddresses(dir, before)
	if err != nil {
		return nil, err
	}
	if result.Modules, err = moduleCalls(dir, moved, declined); err != nil {
		return nil, err
	}
	for i, s := range result.Secrets {
		if to, ok := moved[s.Address]; ok {
			result.Secrets[i].Address = to
		}
	}
	if rejected.Len() > 0 {
		if err := os.WriteFile(filepath.Join(dir, RejectedFileName), rejected.Bytes(), 0o644); err != nil {
			return nil, err
		}
	}
	leftOut := map[string]bool{}
	for _, r := range result.Rejected {
		leftOut[r.Address] = true
	}
	for _, imp := range imports {
		if !leftOut[imp.Type+"."+imp.Name] {
			result.Imported = append(result.Imported, imp)
		}
	}
	if result.Gate, err = runGate(ctx, tf, dir, result.Secrets, opts); err != nil {
		return nil, err
	}
	return result, WriteReadme(dir, result)
}

// unresolved returns the errors in diags by resource, leaving out errors
// about secret attributes, which variables resolve (see useVariables). It
// fails on errors it can't attribute to a resource.
func unresolved(command, dir string, diags []tfjson.Diagnostic) (map[string][]tfjson.Diagnostic, error) {
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
		return nil, fmt.Errorf("%s: %w", command, diagnosticsError(diags))
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
// sensitive variables, declared in variables.tf, named other than the
// variables in taken, and validates the result. It leaves out resources
// that still don't validate, adding them to rejections and out.
func useVariables(ctx context.Context, tf Terraform, dir string, taken Names, rejections *[]Rejection, out *bytes.Buffer) ([]Secret, error) {
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	found := findSecrets(generated)
	if len(found) == 0 {
		return nil, nil
	}
	schemas, err := tf.ProvidersSchema(ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform providers schema: %w", err)
	}
	if found = withoutWriteOnly(found, schemas); len(found) == 0 {
		return nil, nil
	}
	secrets := secretsToVariables(generated, found, taken)
	for i, s := range secrets {
		secrets[i].ty = attributeType(schemas, strings.SplitN(s.Address, ".", 2)[0], s.schemaPath)
	}
	if err := generated.save(); err != nil {
		return nil, err
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
	errs, err := unresolved("terraform validate", dir, errorDiagnostics(validation.Diagnostics))
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
