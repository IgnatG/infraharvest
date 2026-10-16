// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
)

// placeholderString stands in for secret values in the plans post-processing
// runs. Those plans are only compared with each other, never applied. It
// meets the password rules providers check before planning (Redshift's,
// RDS's, Azure's): upper and lower case letters, a digit and a symbol, 8
// to 41 characters, and none of / @ " ' \ or a space.
const placeholderString = "Infraharvest-Placeholder-0"

// postProcess improves the configuration once it plans, in this order:
// the tags an import leaves out (see fillEffectiveTags), arguments that
// only repeat a default (see stripDefaults), references between resources
// (see addReferences), to resources it doesn't manage (see
// addDataSources), to what other roots manage (see addRootVariables) and
// to what the cloud or the provider defines (see addLookups), shared tags
// (see applyTagLift) and repeated identifiers (see liftLiterals). Each
// step is checked against the plan before it (see verify) and undone if
// the plan changes; the references by resource (see verifyEach). Secret
// variables get their values in these plans where Generate found them,
// and placeholder values where Terraform doesn't show them. If the
// configuration doesn't plan even so, only the literal lift is made,
// checked by validation. Last, clusters of resources move into modules:
// curated ones first (see synthesize), whose list outputs then replace
// the lists of all their elements (see collapseOutputLists), then, once
// the resources left in the root no longer repeat what the provider sets
// (see omitInherited), generated local ones (see liftModules). It returns
// the clusters it didn't move into a curated module, with why.
func postProcess(ctx context.Context, tf Terraform, dir string, opts Options, secrets []Secret) ([]ModuleCall, error) {
	vars := append(placeholders(secrets), opts.secretValues.options()...)
	baseline, values, changes, err := planValues(ctx, tf, vars)
	if err != nil {
		return nil, err
	}
	if baseline == nil {
		return nil, liftLiteralsValidated(ctx, tf, dir, opts.Taken)
	}
	// Tags an import leaves out of the configuration first: they are what
	// the steps below compare with, and the shared ones go to a local.
	if et := opts.EffectiveTags; et != nil {
		filled, err := fillEffectiveTags(ctx, tf, dir, *et, values, changes, opts.StateOnly, vars)
		if err != nil {
			return nil, err
		}
		if filled {
			if baseline, values, changes, err = planValues(ctx, tf, vars); err != nil {
				return nil, err
			}
			if baseline == nil {
				return nil, errors.New("the configuration stopped planning after writing its tags")
			}
		}
	}
	// First, so the steps below see only the arguments that matter.
	rules := defaultRules{defaults: opts.Defaults, keepEmpty: opts.KeepEmpty}
	if et := opts.EffectiveTags; et != nil {
		rules.tags = et.Attribute
	}
	stripped, err := stripDefaults(ctx, tf, dir, *baseline, changes, vars, rules)
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
		// each, instead of edit, is checked by resource (see verifyEach).
		each func(skip map[string]int) (map[string]bool, error)
		// fallback, if set, is the edit to try when the plan rejects edit.
		fallback func() (bool, error)
	}
	steps := []step{{files: []string{GeneratedFileName}, each: func(skip map[string]int) (map[string]bool, error) {
		return addReferencesSkipping(dir, values, skip)
	}}}
	if len(opts.External) > 0 {
		steps = append(steps, step{files: []string{GeneratedFileName, DataFileName}, each: func(skip map[string]int) (map[string]bool, error) {
			return addDataSourcesSkipping(dir, opts.External, opts.DataSources, opts.Taken, skip)
		}})
	}
	// After the data sources: variables are for what they can't read.
	if len(opts.OtherRoots) > 0 {
		steps = append(steps, step{files: []string{GeneratedFileName, VariablesFileName, DataFileName}, each: func(skip map[string]int) (map[string]bool, error) {
			return addRootVariables(dir, opts.OtherRoots, opts.ARNs, opts.Taken, skip)
		}})
	}
	steps = append(steps, step{files: []string{GeneratedFileName, DataFileName}, each: func(skip map[string]int) (map[string]bool, error) {
		return addLookups(dir, opts.Taken, skip)
	}})
	// Tags before literals, so a tag value is never made a local.
	if opts.DefaultTags != nil {
		dt := *opts.DefaultTags
		// The schemas tell which resources the provider tags although
		// they set no tags.
		schemas, err := tf.ProvidersSchema(ctx)
		if err != nil {
			return nil, fmt.Errorf("terraform providers schema: %w", err)
		}
		tags := step{files: []string{GeneratedFileName, ProvidersFileName, LocalsFileName}, edit: func() (bool, error) { return applyTagLift(dir, dt, schemas) }}
		if dt.Block != "" && dt.Applied == nil {
			// Where the provider can't apply the tags without changing a
			// resource's, the resources still share them through the local.
			local := dt
			local.Block, local.Argument = "", false
			tags.fallback = func() (bool, error) { return applyTagLift(dir, local, schemas) }
		}
		steps = append(steps, tags)
	}
	steps = append(steps, step{files: []string{GeneratedFileName, LocalsFileName}, edit: func() (bool, error) { return liftLiterals(dir, opts.Taken) }})
	for _, step := range steps {
		var kept bool
		var err error
		if step.each != nil {
			_, err = verifyEach(ctx, tf, dir, *baseline, changes, vars, step.each, step.files...)
		} else {
			kept, err = verify(ctx, tf, dir, *baseline, changes, vars, step.edit, step.files...)
		}
		if err == nil && !kept && step.fallback != nil {
			_, err = verify(ctx, tf, dir, *baseline, changes, vars, step.fallback, step.files...)
		}
		if err != nil {
			return nil, err
		}
	}
	// Last, as clusters reach the modules through the references above.
	var declined []ModuleCall
	if len(opts.Adapters) > 0 {
		// Module calls may update arguments import can't set.
		var lists map[string]int
		opts.values = values
		if declined, *baseline, lists, err = synthesize(ctx, tf, dir, opts, *baseline, changes, vars); err != nil {
			return nil, err
		}
		if len(lists) > 0 {
			collapse := func() (bool, error) {
				return collapseOutputLists(dir, lists, func() (*tfjson.ProviderSchemas, error) { return tf.ProvidersSchema(ctx) })
			}
			if _, err := verify(ctx, tf, dir, *baseline, changes, vars, collapse, GeneratedFileName); err != nil {
				return nil, err
			}
		}
	}
	// After the curated modules, whose calls take the project from the
	// resources; checked like the steps above.
	if len(opts.Inherited) > 0 {
		inherited := func() (bool, error) { return omitInherited(ctx, tf, dir, opts.Inherited) }
		if _, err := verify(ctx, tf, dir, *baseline, changes, vars, inherited, GeneratedFileName); err != nil {
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
		return errors.Join(fmt.Errorf("terraform validate: %w", err), backup.restore())
	}
	if !validation.Valid {
		return backup.restore()
	}
	return nil
}

// verify makes edit, which changes files in dir, and keeps it only if the
// configuration then plans, with vars, without errors, with the same
// change summary as baseline, and with each resource changes has planning
// the same change it had (see changeSignature): the same actions, on the
// same attributes. Totals alone would let an edit hide a real change in a
// resource that already has an expected update, such as a label-only one
// after an import. Resources changes doesn't have, such as those moved
// into module calls since, are judged by the totals. It reports whether
// it kept the edit.
func verify(ctx context.Context, tf Terraform, dir string, baseline changeSummary, changes map[string]string, vars []tfexec.PlanOption, edit func() (bool, error), files ...string) (bool, error) {
	backup, err := backupFiles(dir, files...)
	if err != nil {
		return false, err
	}
	changed, err := edit()
	if err != nil {
		// The edit may have changed some of the files before failing.
		return false, errors.Join(err, backup.restore())
	}
	if !changed {
		return false, nil
	}
	p, summary, diags, err := showPlanWithSummary(ctx, tf, vars)
	if err != nil {
		return false, errors.Join(err, backup.restore())
	}
	if len(diags) == 0 && summary != nil && *summary == baseline && sameChanges(p, changes) {
		return true, nil
	}
	return false, backup.restore()
}

// maxEditRounds bounds how often verifyEach plans an edit.
const maxEditRounds = 3

// verifyEach makes edit, which changes files in dir by resource, and
// keeps what the plan accepts of it: where a resource then plans
// differently from changes, its planned change before the edit (see
// changeSignature), or an error is about it, it makes edit again on the
// files as they were, with that resource's level in skip raised, until
// the plan, with vars, matches baseline, every resource planning the
// change it had (see sameChanges). edit returns the resources it
// changed. It undoes the edit if the plan differs in a way no resource it
// changed explains, and reports whether it kept any of it.
func verifyEach(ctx context.Context, tf Terraform, dir string, baseline changeSummary, changes map[string]string, vars []tfexec.PlanOption, edit func(skip map[string]int) (map[string]bool, error), files ...string) (bool, error) {
	backup, err := backupFiles(dir, files...)
	if err != nil {
		return false, err
	}
	skip := map[string]int{}
	for round := 0; round < maxEditRounds; round++ {
		if round > 0 {
			if err := backup.restore(); err != nil {
				return false, err
			}
		}
		edited, err := edit(skip)
		if err != nil {
			// The edit may have changed some of the files before failing.
			return false, errors.Join(err, backup.restore())
		}
		if len(edited) == 0 {
			return false, backup.restore()
		}
		p, summary, diags, err := showPlanWithSummary(ctx, tf, vars)
		if err != nil {
			return false, errors.Join(err, backup.restore())
		}
		if len(diags) == 0 && summary != nil && *summary == baseline && sameChanges(p, changes) {
			return true, nil
		}
		blamed, err := blame(dir, p, diags, changes)
		if err != nil {
			return false, errors.Join(err, backup.restore())
		}
		progress := false
		for address := range blamed {
			if edited[address] {
				skip[address]++
				progress = true
			}
		}
		if !progress {
			break
		}
	}
	return false, backup.restore()
}

// dataReaders returns the resources of generated that refer to the data
// source at address, such as data.aws_kms_alias.ebs, or none if address
// isn't a data source's.
func dataReaders(generated *hclFile, address string) []string {
	parts := strings.Split(address, ".")
	if len(parts) != 3 || parts[0] != "data" {
		return nil
	}
	var readers []string
	for _, r := range generated.resources() {
		for _, t := range allTraversals(r.syntax.Body) {
			if len(t) < 3 || t.RootName() != "data" {
				continue
			}
			typ, ok1 := t[1].(hcl.TraverseAttr)
			name, ok2 := t[2].(hcl.TraverseAttr)
			if ok1 && ok2 && typ.Name == parts[1] && name.Name == parts[2] {
				readers = append(readers, r.address)
				break
			}
		}
	}
	return readers
}

// blame returns the resources the plan's errors are about, or, without
// errors, those of p that plan differently from changes. It returns none
// if an error is about no resource.
func blame(dir string, p *tfjson.Plan, diags []tfjson.Diagnostic, changes map[string]string) (map[string]bool, error) {
	blamed := map[string]bool{}
	if len(diags) > 0 {
		generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
		if err != nil {
			return nil, err
		}
		imports, err := loadOptionalHCL(filepath.Join(dir, ImportsFileName))
		if err != nil || imports == nil {
			return nil, err
		}
		assigned, unassigned := byResource(diags, generated, imports)
		// An error about a data source, such as an alias the account
		// lacks, is the resources' that read it.
		for _, d := range unassigned {
			readers := dataReaders(generated, trimInstanceKey(d.Address))
			if len(readers) == 0 {
				return nil, nil
			}
			for _, address := range readers {
				blamed[address] = true
			}
		}
		for address := range assigned {
			blamed[address] = true
		}
		return blamed, nil
	}
	if p == nil {
		return nil, nil
	}
	for _, rc := range p.ResourceChanges {
		if changedFrom(rc, changes) {
			blamed[rc.Address] = true
		}
	}
	return blamed, nil
}

// sameChanges reports whether every resource of changes that p plans has
// the change changes has for it.
func sameChanges(p *tfjson.Plan, changes map[string]string) bool {
	if p == nil {
		return len(changes) == 0
	}
	for _, rc := range p.ResourceChanges {
		if changedFrom(rc, changes) {
			return false
		}
	}
	return true
}

// changedFrom reports whether a resource a plan has plans another change
// than the one changes has for it (see changeSignature). A resource changes
// doesn't have, such as one moved into a module call since, is judged by
// the plan's totals instead.
func changedFrom(rc *tfjson.ResourceChange, changes map[string]string) bool {
	if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil {
		return false
	}
	want, ok := changes[rc.Address]
	return ok && changeSignature(rc) != want
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

// placeholders gives each secret variable without a known value (see
// secretValues) a value of its type.
func placeholders(secrets []Secret) []tfexec.PlanOption {
	vars := make([]tfexec.PlanOption, 0, len(secrets))
	for _, s := range secrets {
		if s.value != "" {
			continue
		}
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
