// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
)

// placeholderString stands in for secret values in the plans post-processing
// runs. Those plans are only compared with each other, never applied.
const placeholderString = "infraharvest-placeholder"

// postProcess improves the configuration once it plans, in this order:
// arguments that only repeat a default (see stripDefaults),
// references between resources (see addReferences) and to resources it
// doesn't manage (see addDataSources), shared tags (see
// applyTagLift) and repeated identifiers (see liftLiterals). Each step is
// checked against the plan before it (see verify) and undone if the plan
// changes. Secret variables get placeholder values in these plans. If the
// configuration doesn't plan even so, only the literal lift is made,
// checked by validation. Last, clusters of resources move into modules:
// curated ones first (see synthesize), then generated local ones (see
// liftModules). It returns the clusters it didn't move into a curated
// module, with why.
func postProcess(ctx context.Context, tf Terraform, dir string, opts Options, secrets []Secret) ([]ModuleCall, error) {
	vars := placeholders(secrets)
	baseline, values, changes, err := planValues(ctx, tf, vars)
	if err != nil {
		return nil, err
	}
	if baseline == nil {
		return nil, liftLiteralsValidated(ctx, tf, dir, opts.Taken)
	}
	// First, so the steps below see only the arguments that matter.
	stripped, err := stripDefaults(ctx, tf, dir, *baseline, changes, vars)
	if err != nil {
		return nil, err
	}
	if stripped {
		// Leaving out a default may remove changes: compare with the new plan.
		if baseline, values, changes, err = planValues(ctx, tf, vars); err != nil {
			return nil, err
		}
		if baseline == nil {
			return nil, errors.New("the configuration stopped planning after leaving out defaults")
		}
	}
	type step struct {
		files []string
		edit  func() (bool, error)
	}
	steps := []step{{[]string{GeneratedFileName}, func() (bool, error) { return addReferences(dir, values) }}}
	if len(opts.External) > 0 {
		steps = append(steps, step{[]string{GeneratedFileName, DataFileName}, func() (bool, error) {
			return addDataSources(dir, opts.External, opts.DataSources, opts.Taken)
		}})
	}
	// Tags before literals, so a tag value is never made a local.
	if opts.DefaultTags != nil {
		dt := *opts.DefaultTags
		steps = append(steps, step{[]string{GeneratedFileName, ProvidersFileName, LocalsFileName}, func() (bool, error) { return applyTagLift(dir, dt) }})
	}
	steps = append(steps, step{[]string{GeneratedFileName, LocalsFileName}, func() (bool, error) { return liftLiterals(dir, opts.Taken) }})
	for _, step := range steps {
		if _, err := verify(ctx, tf, dir, *baseline, vars, step.edit, step.files...); err != nil {
			return nil, err
		}
	}
	// Last, as clusters reach the modules through the references above.
	var declined []ModuleCall
	if len(opts.Adapters) > 0 {
		if declined, err = synthesize(ctx, tf, dir, opts.Adapters, opts.Taken, *baseline, changes, vars); err != nil {
			return nil, err
		}
	}
	if opts.ModulesDir != "" {
		if _, err := liftModules(ctx, tf, dir, opts.ModulesDir, *baseline, vars); err != nil {
			return nil, err
		}
	}
	return declined, nil
}

// liftLiteralsValidated lifts repeated identifiers when the configuration
// can't be planned, and undoes the lift if it doesn't validate.
func liftLiteralsValidated(ctx context.Context, tf Terraform, dir string, taken Names) error {
	backup, err := backupFiles(dir, GeneratedFileName, LocalsFileName)
	if err != nil {
		return err
	}
	lifted, err := liftLiterals(dir, taken)
	if err != nil || !lifted {
		return err
	}
	validation, err := tf.Validate(ctx)
	if err != nil {
		return fmt.Errorf("terraform validate: %w", err)
	}
	if !validation.Valid {
		return backup.restore()
	}
	return nil
}

// verify makes edit, which changes files in dir, and keeps it only if the
// configuration then plans, with vars, without errors and with the same
// changes as baseline. It reports whether it kept the edit.
func verify(ctx context.Context, tf Terraform, dir string, baseline changeSummary, vars []tfexec.PlanOption, edit func() (bool, error), files ...string) (bool, error) {
	backup, err := backupFiles(dir, files...)
	if err != nil {
		return false, err
	}
	changed, err := edit()
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	diags, summary, err := plan(ctx, tf, vars...)
	if err != nil {
		return false, fmt.Errorf("terraform plan: %w", err)
	}
	if len(diags) == 0 && summary != nil && *summary == baseline {
		return true, nil
	}
	return false, backup.restore()
}

// planValues plans with vars and returns the plan's change summary, and
// each resource's imported attribute values and planned change (see
// changeSignature), by address. It returns no summary if the
// configuration doesn't plan.
func planValues(ctx context.Context, tf Terraform, vars []tfexec.PlanOption) (*changeSummary, map[string]map[string]any, map[string]string, error) {
	p, summary, _, err := showPlanWithSummary(ctx, tf, vars)
	if err != nil || p == nil {
		return nil, nil, nil, err
	}
	values := map[string]map[string]any{}
	changes := map[string]string{}
	for _, rc := range p.ResourceChanges {
		if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil {
			continue
		}
		// An import's before is the imported object.
		attrs, ok := rc.Change.Before.(map[string]any)
		if !ok {
			attrs, _ = rc.Change.After.(map[string]any)
		}
		values[rc.Address] = attrs
		changes[rc.Address] = changeSignature(rc)
	}
	return summary, values, changes, nil
}

// placeholders gives each secret variable a value of its type.
func placeholders(secrets []Secret) []tfexec.PlanOption {
	vars := make([]tfexec.PlanOption, 0, len(secrets))
	for _, s := range secrets {
		value := placeholderString
		switch {
		case s.ty.Equals(cty.Number):
			value = "0"
		case s.ty.Equals(cty.Bool):
			value = "false"
		case s.ty.IsListType(), s.ty.IsSetType(), s.ty.IsTupleType():
			value = "[]"
		case s.ty.IsMapType(), s.ty.IsObjectType():
			value = "{}"
		}
		vars = append(vars, tfexec.Var(s.Variable+"="+value))
	}
	return vars
}
