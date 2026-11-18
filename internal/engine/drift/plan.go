// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// variablePlaceholders returns -var options with a placeholder value for
// each variable of files without a default, a TF_VAR_ environment
// variable (unless noEnvVars) or a value in a tfvars file of dir that Terraform reads by
// itself, or in varFiles, the other var files the plans read, and their
// names, sorted: a plan with -input=false fails without them.
func variablePlaceholders(dir string, files []*hclfile.File, noEnvVars bool, varFiles ...string) ([]tfexec.PlanOption, []string) {
	set := tfvarsNames(dir, varFiles...)
	var names []string
	types := map[string]string{}
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			if b.Type != "variable" || len(b.Labels) != 1 {
				continue
			}
			name := b.Labels[0]
			if _, ok := b.Body.Attributes["default"]; ok || set[name] {
				continue
			}
			if _, ok := os.LookupEnv("TF_VAR_" + name); ok && !noEnvVars {
				continue
			}
			names = append(names, name)
			if t, ok := b.Body.Attributes["type"]; ok {
				rng := t.Expr.Range()
				types[name] = strings.TrimSpace(string(f.Src[rng.Start.Byte:rng.End.Byte]))
			}
		}
	}
	sort.Strings(names)
	names = slices.Compact(names)
	vars := make([]tfexec.PlanOption, 0, len(names))
	for _, name := range names {
		vars = append(vars, tfexec.Var(name+"="+placeholderFor(types[name])))
	}
	return vars, names
}

// placeholderFor returns a placeholder value, as -var takes it, for a
// variable of the type constraint typ.
func placeholderFor(typ string) string {
	switch {
	case typ == "number":
		return "0"
	case typ == "bool":
		return "false"
	case strings.HasPrefix(typ, "list(") || strings.HasPrefix(typ, "set(") || strings.HasPrefix(typ, "tuple("):
		return "[]"
	case strings.HasPrefix(typ, "map(") || strings.HasPrefix(typ, "object("):
		return "{}"
	}
	return secret.PlaceholderValue
}

// tfvarsNames returns the variables that terraform.tfvars and the
// *.auto.tfvars files of dir set, and those of the var files more.
func tfvarsNames(dir string, more ...string) map[string]bool {
	names := map[string]bool{}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.auto.tfvars"))
	paths = append(paths, filepath.Join(dir, "terraform.tfvars"))
	paths = append(paths, more...)
	for _, path := range paths {
		f, err := hclfile.ReadVarFile(path)
		if err != nil || f == nil {
			continue
		}
		for name := range f.Syntax.Attributes {
			names[name] = true
		}
	}
	return names
}

// savedPlan plans with opts into the plan file path and returns it as
// JSON, or the plan's errors.
func savedPlan(ctx context.Context, tf tfrun.DriftTerraform, path string, opts ...tfexec.PlanOption) (*tfjson.Plan, error) {
	diags, summary, err := tfrun.Plan(ctx, tf, append(opts, tfexec.Out(path))...)
	if err != nil {
		return nil, err
	}
	if len(diags) > 0 {
		return nil, tfrun.DiagnosticsError(diags)
	}
	if summary == nil {
		return nil, errors.New("terraform plan reported no change summary")
	}
	if _, err := os.Stat(path); err == nil {
		_ = os.Chmod(path, 0o600)
	}
	p, err := tf.ShowPlanFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("terraform show: %w", err)
	}
	return p, nil
}

// hasDrift reports whether a refresh-only plan found a managed resource
// that differs in the cloud.
func hasDrift(p *tfjson.Plan) bool {
	if p == nil {
		return false
	}
	for _, rc := range p.ResourceDrift {
		if rc.Mode == tfjson.ManagedResourceMode && rc.Change != nil {
			return true
		}
	}
	return false
}
