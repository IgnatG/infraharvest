// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// postProcess improves the configuration once it plans, with the passes of
// tidyPasses, in their order: the tags an import leaves out (see
// fillEffectiveTags), arguments that only repeat a default (see
// stripDefaults), references between resources (see
// addReferencesSkipping), to resources it doesn't manage (see
// addDataSourcesSkipping), to what other roots manage (see
// addRootVariables) and to what the cloud or the provider defines (see
// addLookups), shared tags (see applyTagLift) and repeated identifiers
// (see liftLiterals). The steps between two passes that plan
// on their own are checked together by one plan, and only if it changes,
// each against the plan before it, undone if the plan changes, the
// references by resource (see verifySteps). Secret variables get their
// values in these plans where Generate found them, and placeholder values
// where Terraform doesn't show them. If the configuration doesn't plan
// even so, only the literal lift is made, checked by validation. Last,
// clusters of resources move into modules: curated ones first (see
// synthesize), whose list outputs then replace the lists of all their
// elements (see collapseOutputLists) and whose calls refer to what the
// root manages, such as its project (see referModulesToResources), then,
// once the resources left in the root no longer repeat what the provider
// sets (see omitInherited), generated local ones (see liftModules). It returns the clusters it
// didn't move into a curated module, with why. planned, if set, is a plan
// of the configuration as it is, with the same variables, which it starts
// from instead of planning.
func postProcess(ctx context.Context, tf tfrun.Terraform, dir string, r *run, secrets []secret.Variable, planned *shownPlan) ([]ModuleCall, error) {
	vars := append(secret.Placeholders(secrets), r.secrets.Options()...)
	baseline, values, changes, err := replanValues(ctx, tf, vars, planned)
	if err != nil {
		return nil, err
	}
	if baseline == nil {
		return nil, liftLiteralsValidated(ctx, tf, dir, r.taken)
	}
	t := &tidying{ctx: ctx, tf: tf, dir: dir, r: r, vars: vars, baseline: *baseline, values: values, changes: changes}
	passes := t.passes()
	if err := checkPassOrder(passes); err != nil {
		return nil, err
	}
	var steps []tidyStep
	verify := func() error {
		err := verifySteps(ctx, tf, dir, t.baseline, t.changes, vars, steps)
		steps = nil
		return err
	}
	for _, p := range passes {
		if p.step != nil {
			step, err := p.step()
			if err != nil {
				return nil, err
			}
			if step != nil {
				steps = append(steps, *step)
			}
			continue
		}
		// The steps before a pass that plans on its own are checked
		// against the plan they compare with before it changes.
		if err := verify(); err != nil {
			return nil, err
		}
		if err := p.run(); err != nil {
			return nil, err
		}
	}
	if err := verify(); err != nil {
		return nil, err
	}
	return t.declined, nil
}

// The passes of postProcess, by name (see tidyPass).
const (
	passEffectiveTags  = "effective tags"
	passDefaults       = "defaults"
	passReferences     = "references"
	passDataSources    = "data sources"
	passRootVariables  = "root variables"
	passLookups        = "lookups"
	passTagLift        = "tag lift"
	passLiterals       = "literals"
	passCuratedModules = "curated modules"
	passOutputLists    = "output lists"
	passProjects       = "project references"
	passInherited      = "inherited arguments"
	passLocalModules   = "local modules"
)

// tidyPass is one of postProcess's passes over the configuration. It names
// the passes it must run after, which checkPassOrder holds the order of
// tidyPasses to.
type tidyPass struct {
	name  string
	after []string
	// run makes a pass that plans on its own, such as one that changes the
	// plan the passes after it compare with.
	run func() error
	// step, instead of run, returns the edit verifySteps checks together
	// with the steps next to it, or nil where the run makes none.
	step func() (*tidyStep, error)
}

// tidying is what postProcess's passes share: the plan they compare with,
// as the passes before them left it, and what the curated modules leave
// for the passes after them.
type tidying struct {
	ctx  context.Context
	tf   tfrun.Terraform
	dir  string
	r    *run
	vars []tfexec.PlanOption

	baseline tfrun.ChangeSummary
	values   map[string]map[string]any
	changes  map[string]string

	// declined are the clusters synthesize didn't move into a curated
	// module, with why, and lists the lengths of the list outputs of the
	// calls it made.
	declined []ModuleCall
	lists    map[string]int
}

// passes returns postProcess's passes in the order it runs them. A pass
// the run doesn't make, such as the data sources of a run without external
// resources, does nothing.
func (t *tidying) passes() []tidyPass {
	ctx, tf, dir, opts, taken := t.ctx, t.tf, t.dir, t.r.opts, t.r.taken
	return []tidyPass{
		// Tags an import leaves out of the configuration first: they are
		// what the passes below compare with, and the shared ones go to a
		// local.
		{name: passEffectiveTags, run: t.fillEffectiveTags},
		// Next, so the passes below see only the arguments that matter.
		{name: passDefaults, after: []string{passEffectiveTags}, run: t.stripDefaults},
		{name: passReferences, after: []string{passDefaults}, step: func() (*tidyStep, error) {
			return &tidyStep{files: []string{rootfile.GeneratedFileName}, each: func(skip map[string]int) (map[string]bool, error) {
				return addReferencesSkipping(dir, t.values, skip)
			}}, nil
		}},
		// After the references: data sources are for what the root
		// doesn't manage.
		{name: passDataSources, after: []string{passReferences}, step: func() (*tidyStep, error) {
			if len(opts.External) == 0 {
				return nil, nil
			}
			return &tidyStep{files: []string{rootfile.GeneratedFileName, rootfile.DataFileName}, each: func(skip map[string]int) (map[string]bool, error) {
				return addDataSourcesSkipping(dir, opts.External, opts.DataSources, taken, skip)
			}}, nil
		}},
		// After the data sources: variables are for what they can't read.
		{name: passRootVariables, after: []string{passReferences, passDataSources}, step: func() (*tidyStep, error) {
			if len(opts.OtherRoots) == 0 {
				return nil, nil
			}
			return &tidyStep{files: []string{rootfile.GeneratedFileName, hclfile.VariablesFileName, rootfile.DataFileName}, each: func(skip map[string]int) (map[string]bool, error) {
				return addRootVariables(dir, opts.OtherRoots, opts.ARNs, taken, skip)
			}}, nil
		}},
		// Lookups are for what nothing the import lists has.
		{name: passLookups, after: []string{passReferences, passDataSources, passRootVariables}, step: func() (*tidyStep, error) {
			return &tidyStep{files: []string{rootfile.GeneratedFileName, rootfile.DataFileName}, each: func(skip map[string]int) (map[string]bool, error) {
				return addLookups(dir, taken, skip)
			}}, nil
		}},
		{name: passTagLift, after: []string{passEffectiveTags, passDefaults}, step: t.tagLift},
		// Tags before literals, so a tag value is never made a local, and
		// the references first, so a literal they replace isn't.
		{name: passLiterals, after: []string{passTagLift, passReferences, passDataSources, passRootVariables, passLookups}, step: func() (*tidyStep, error) {
			return &tidyStep{files: []string{rootfile.GeneratedFileName, rootfile.LocalsFileName}, edit: func() (bool, error) { return liftLiterals(dir, taken) }}, nil
		}},
		// After the steps above, as clusters reach the modules through the
		// references.
		{name: passCuratedModules, after: []string{passReferences, passDataSources, passRootVariables, passLookups, passLiterals}, run: t.synthesize},
		{name: passOutputLists, after: []string{passCuratedModules}, step: func() (*tidyStep, error) {
			if len(t.lists) == 0 {
				return nil, nil
			}
			lists := t.lists
			return &tidyStep{files: []string{rootfile.GeneratedFileName}, edit: func() (bool, error) {
				return collapseOutputLists(dir, lists, func() (*tfjson.ProviderSchemas, error) { return tf.ProvidersSchema(ctx) })
			}}, nil
		}},
		{name: passProjects, after: []string{passCuratedModules}, step: func() (*tidyStep, error) {
			if len(opts.Adapters) == 0 {
				return nil, nil
			}
			return &tidyStep{files: []string{rootfile.GeneratedFileName}, edit: func() (bool, error) { return referModulesToResources(dir) }}, nil
		}},
		// After the curated modules, whose calls take the project from the
		// resources.
		{name: passInherited, after: []string{passCuratedModules}, step: func() (*tidyStep, error) {
			if len(opts.Inherited) == 0 {
				return nil, nil
			}
			return &tidyStep{files: []string{rootfile.GeneratedFileName}, edit: func() (bool, error) { return omitInherited(ctx, tf, dir, opts.Inherited) }}, nil
		}},
		// Last: curated modules first, and the resources left no longer
		// repeat what the provider sets.
		{name: passLocalModules, after: []string{passCuratedModules, passInherited}, run: t.liftModules},
	}
}

// checkPassOrder reports a pass of passes that runs before a pass it must
// run after, or names one passes doesn't have.
func checkPassOrder(passes []tidyPass) error {
	at := map[string]int{}
	for i, p := range passes {
		at[p.name] = i
	}
	for i, p := range passes {
		for _, name := range p.after {
			j, ok := at[name]
			if !ok {
				return fmt.Errorf("the %s pass runs after the %s pass, which there isn't", p.name, name)
			}
			if j > i {
				return fmt.Errorf("the %s pass runs before the %s pass, which it must run after", p.name, name)
			}
		}
	}
	return nil
}

// replan makes the plan last shows, or a new one, what the passes after
// compare with; stopped is the error should the configuration no longer
// plan.
func (t *tidying) replan(last *shownPlan, stopped string) error {
	baseline, values, changes, err := replanValues(t.ctx, t.tf, t.vars, last)
	if err != nil {
		return err
	}
	if baseline == nil {
		return errors.New(stopped)
	}
	t.baseline, t.values, t.changes = *baseline, values, changes
	return nil
}

// fillEffectiveTags writes the tags an import leaves out of the
// configuration, with Options.EffectiveTags (see fillEffectiveTags).
func (t *tidying) fillEffectiveTags() error {
	et := t.r.opts.EffectiveTags
	if et == nil {
		return nil
	}
	filled, last, err := fillEffectiveTags(t.ctx, t.tf, t.dir, *et, t.values, t.changes, t.r.opts.StateOnly, t.vars)
	if err != nil || !filled {
		return err
	}
	return t.replan(last, "the configuration stopped planning after writing its tags")
}

// stripDefaults leaves out the arguments that only repeat a default (see
// stripDefaults).
func (t *tidying) stripDefaults() error {
	opts := t.r.opts
	rules := defaultRules{defaults: opts.Defaults, keepEmpty: opts.KeepEmpty, hints: schemaHintsFor(t.dir, opts.SchemaMetadata)}
	if et := opts.EffectiveTags; et != nil {
		rules.tags = et.Attribute
	}
	stripped, last, err := stripDefaults(t.ctx, t.tf, t.dir, t.baseline, t.changes, t.vars, rules)
	if err != nil || !stripped {
		return err
	}
	// Leaving out a default may remove changes: compare with the new plan.
	return t.replan(last, "the configuration stopped planning after leaving out defaults")
}

// tagLift is the step that moves the tags every resource shares into
// local.tags, with the run's default tags (see applyTagLift).
func (t *tidying) tagLift() (*tidyStep, error) {
	if t.r.opts.DefaultTags == nil {
		return nil, nil
	}
	dt, dir := *t.r.opts.DefaultTags, t.dir
	// The schemas tell which resources the provider tags although they
	// set no tags.
	schemas, err := t.tf.ProvidersSchema(t.ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform providers schema: %w", err)
	}
	tags := &tidyStep{files: []string{rootfile.GeneratedFileName, rootfile.ProvidersFileName, rootfile.LocalsFileName}, edit: func() (bool, error) { return applyTagLift(dir, dt, schemas) }}
	if dt.Block != "" && dt.Applied == nil {
		// Where the provider can't apply the tags without changing a
		// resource's, the resources still share them through the local.
		local := dt
		local.Block, local.Argument = "", false
		tags.fallback = func() (bool, error) { return applyTagLift(dir, local, schemas) }
	}
	return tags, nil
}

// synthesize moves clusters into calls of curated modules, with the run's
// adapters (see synthesize). Module calls may update arguments import
// can't set.
func (t *tidying) synthesize() error {
	if len(t.r.opts.Adapters) == 0 {
		return nil
	}
	var err error
	t.declined, t.baseline, t.lists, err = synthesize(t.ctx, t.tf, t.dir, t.r, t.values, t.baseline, t.changes, t.vars)
	return err
}

// liftModules moves clusters that repeat with the same shape into local
// modules, in the run's modules directory (see liftModules).
func (t *tidying) liftModules() error {
	if t.r.opts.ModulesDir == "" {
		return nil
	}
	_, err := liftModules(t.ctx, t.tf, t.dir, t.r.opts.ModulesDir, t.r.modules, t.baseline, t.changes, t.vars)
	return err
}

// liftLiteralsValidated lifts repeated identifiers when the configuration
// can't be planned, and undoes the lift if it doesn't validate.
func liftLiteralsValidated(ctx context.Context, tf tfrun.Terraform, dir string, taken Names) error {
	backup, err := fsutil.BackupFiles(dir, rootfile.GeneratedFileName, rootfile.LocalsFileName)
	if err != nil {
		return err
	}
	lifted, err := liftLiterals(dir, taken)
	if err != nil || !lifted {
		return err
	}
	validation, err := tf.Validate(ctx)
	if err != nil {
		return errors.Join(fmt.Errorf("terraform validate: %w", err), backup.Restore())
	}
	if !validation.Valid {
		return backup.Restore()
	}
	return nil
}

// tidyStep is one of postProcess's edits of the configuration, which a
// plan checks (see verifySteps).
type tidyStep struct {
	// files are those the step may change.
	files []string
	edit  func() (bool, error)
	// each, instead of edit, is checked by resource (see verifyEach).
	each func(skip map[string]int) (map[string]bool, error)
	// fallback, if set, is the edit to try when the plan rejects edit.
	fallback func() (bool, error)
}

// apply makes the step's edit, or its edit by resource with nothing
// skipped, and reports whether it changed anything.
func (s tidyStep) apply() (bool, error) {
	if s.each == nil {
		return s.edit()
	}
	edited, err := s.each(map[string]int{})
	return len(edited) > 0, err
}

// verifySteps makes steps, in order, and keeps what the plan accepts of
// them. Every plan with import blocks reads every imported resource
// again, so it first makes them all and plans once (see verifyTogether).
// Only if that plan differs does it check them one at a time, each step
// against the plan before it (see verify) and undone if the plan
// changes, by resource where the step has each (see verifyEach), and
// with its fallback where the plan rejects its edit. Either way, what it
// keeps has every resource plan the change it had.
func verifySteps(ctx context.Context, tf tfrun.Terraform, dir string, baseline tfrun.ChangeSummary, changes map[string]string, vars []tfexec.PlanOption, steps []tidyStep) error {
	if len(steps) > 1 {
		settled, err := verifyTogether(ctx, tf, dir, baseline, changes, vars, steps)
		if err != nil || settled {
			return err
		}
	}
	for _, step := range steps {
		var kept bool
		var err error
		if step.each != nil {
			_, err = verifyEach(ctx, tf, dir, baseline, changes, vars, step.each, step.files...)
		} else {
			kept, err = verify(ctx, tf, dir, baseline, changes, vars, step.edit, step.files...)
		}
		if err == nil && !kept && step.fallback != nil {
			_, err = verify(ctx, tf, dir, baseline, changes, vars, step.fallback, step.files...)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// verifyTogether makes the edit of every step, in order, each on the
// files as the ones before it left them, and keeps them all if the
// configuration then plans, with vars, as verify requires: without
// errors, with baseline's change summary, and with each resource planning
// the change it had. It reports whether that settled the steps: it kept
// them, or none of them changed anything. Otherwise, as when one edit
// fails, it leaves the files as they were, for the steps to be checked
// one at a time.
func verifyTogether(ctx context.Context, tf tfrun.Terraform, dir string, baseline tfrun.ChangeSummary, changes map[string]string, vars []tfexec.PlanOption, steps []tidyStep) (bool, error) {
	var files []string
	for _, s := range steps {
		for _, f := range s.files {
			if !slices.Contains(files, f) {
				files = append(files, f)
			}
		}
	}
	backup, err := fsutil.BackupFiles(dir, files...)
	if err != nil {
		return false, err
	}
	changed := false
	for _, s := range steps {
		edited, err := s.apply()
		if err != nil {
			// The step may fail only on top of another's edit: let the
			// steps one at a time tell.
			return false, backup.Restore()
		}
		changed = changed || edited
	}
	if !changed {
		return true, backup.Restore()
	}
	p, summary, diags, err := tfrun.ShowPlan(ctx, tf, vars)
	if err != nil {
		return false, errors.Join(err, backup.Restore())
	}
	if len(diags) == 0 && summary != nil && *summary == baseline && sameChanges(p, changes) {
		return true, nil
	}
	return false, backup.Restore()
}

// verify makes edit, which changes files in dir, and keeps it only if the
// configuration then plans, with vars, without errors, with the same
// change summary as baseline, and with each resource changes has planning
// the same change it had (see tfrun.ChangeSignature): the same actions, on the
// same attributes. Totals alone would let an edit hide a real change in a
// resource that already has an expected update, such as a label-only one
// after an import. Resources changes doesn't have, such as those moved
// into module calls since, are judged by the totals. It reports whether
// it kept the edit.
func verify(ctx context.Context, tf tfrun.Terraform, dir string, baseline tfrun.ChangeSummary, changes map[string]string, vars []tfexec.PlanOption, edit func() (bool, error), files ...string) (bool, error) {
	backup, err := fsutil.BackupFiles(dir, files...)
	if err != nil {
		return false, err
	}
	changed, err := edit()
	if err != nil {
		// The edit may have changed some of the files before failing.
		return false, errors.Join(err, backup.Restore())
	}
	if !changed {
		return false, nil
	}
	p, summary, diags, err := tfrun.ShowPlan(ctx, tf, vars)
	if err != nil {
		return false, errors.Join(err, backup.Restore())
	}
	if len(diags) == 0 && summary != nil && *summary == baseline && sameChanges(p, changes) {
		return true, nil
	}
	return false, backup.Restore()
}

// maxEditRounds bounds how often verifyEach plans an edit.
const maxEditRounds = 3

// verifyEach makes edit, which changes files in dir by resource, and
// keeps what the plan accepts of it: where a resource then plans
// differently from changes, its planned change before the edit (see
// tfrun.ChangeSignature), or an error is about it, it makes edit again on the
// files as they were, with that resource's level in skip raised, until
// the plan, with vars, matches baseline, every resource planning the
// change it had (see sameChanges), in at most maxEditRounds plans (see
// gate.Verifier). edit returns the resources it changed. It undoes the edit
// if the plan still differs then, or differs in a way no resource it
// changed explains, and reports whether it kept any of it.
func verifyEach(ctx context.Context, tf tfrun.Terraform, dir string, baseline tfrun.ChangeSummary, changes map[string]string, vars []tfexec.PlanOption, edit func(skip map[string]int) (map[string]bool, error), files ...string) (bool, error) {
	backup, err := fsutil.BackupFiles(dir, files...)
	if err != nil {
		return false, err
	}
	skip := map[string]int{}
	retry := false
	kept, err := gate.Verifier[string, bool]{
		Edit: func(context.Context) ([]string, error) {
			if retry {
				if err := backup.Restore(); err != nil {
					return nil, err
				}
			}
			retry = true
			resources, err := edit(skip)
			return sortedKeys(resources), err
		},
		Verify: func(ctx context.Context) (*gate.Verdict[string, bool], error) {
			p, summary, diags, err := tfrun.ShowPlan(ctx, tf, vars)
			if err != nil {
				return nil, err
			}
			if len(diags) == 0 && summary != nil && *summary == baseline && sameChanges(p, changes) {
				return nil, nil
			}
			blamed, err := blame(dir, p, diags, changes)
			return &gate.Verdict[string, bool]{Blamed: blamed}, err
		},
		Drop: func(blamed map[string]bool) {
			for address := range blamed {
				skip[address]++
			}
		},
		Rounds: maxEditRounds,
	}.Run(ctx)
	if err != nil {
		// The edit may have changed some of the files before failing.
		return false, errors.Join(err, backup.Restore())
	}
	if !kept {
		return false, backup.Restore()
	}
	return true, nil
}

// dataReaders returns the resources of generated that refer to the data
// source at address, such as data.aws_kms_alias.ebs, or none if address
// isn't a data source's.
func dataReaders(generated *hclfile.File, address string) []string {
	parts := strings.Split(address, ".")
	if len(parts) != 3 || parts[0] != "data" {
		return nil
	}
	var readers []string
	for _, r := range generated.Resources() {
		for _, t := range hclfile.Traversals(r.Syntax.Body) {
			if len(t) < 3 || t.RootName() != "data" {
				continue
			}
			typ, ok1 := t[1].(hcl.TraverseAttr)
			name, ok2 := t[2].(hcl.TraverseAttr)
			if ok1 && ok2 && typ.Name == parts[1] && name.Name == parts[2] {
				readers = append(readers, r.Address)
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
		generated, err := hclfile.Load(filepath.Join(dir, rootfile.GeneratedFileName))
		if err != nil {
			return nil, err
		}
		imports, err := hclfile.LoadOptional(filepath.Join(dir, rootfile.ImportsFileName))
		if err != nil || imports == nil {
			return nil, err
		}
		assigned, unassigned := byResource(diags, generated, imports)
		// An error about a data source, such as an alias the account
		// lacks, is the resources' that read it.
		for _, d := range unassigned {
			readers := dataReaders(generated, hclfile.TrimInstanceKey(d.Address))
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
// than the one changes has for it (see tfrun.ChangeSignature). A resource changes
// doesn't have, such as one moved into a module call since, is judged by
// the plan's totals instead.
func changedFrom(rc *tfjson.ResourceChange, changes map[string]string) bool {
	if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil {
		return false
	}
	want, ok := changes[rc.Address]
	return ok && tfrun.ChangeSignature(rc) != want
}

// planValues plans with vars and returns the plan's change summary, and
// each resource's imported attribute values and planned change (see
// tfrun.ChangeSignature), by address. It returns no summary if the
// configuration doesn't plan.
func planValues(ctx context.Context, tf tfrun.Terraform, vars []tfexec.PlanOption) (*tfrun.ChangeSummary, map[string]map[string]any, map[string]string, error) {
	p, summary, _, err := tfrun.ShowPlan(ctx, tf, vars)
	if err != nil || p == nil {
		return nil, nil, nil, err
	}
	values, changes := valuesOf(p)
	return summary, values, changes, nil
}

// shownPlan is a plan Terraform showed of the configuration as a step
// left it, with its change summary. A step that ends on such a plan hands
// it back, so that the steps after it compare with it rather than plan
// the same configuration again.
type shownPlan struct {
	plan    *tfjson.Plan
	summary *tfrun.ChangeSummary
}

// replanValues is planValues, from last if set: a plan of the
// configuration as it is, with vars, such as the one a step ended on.
// Planning the same configuration with the same vars again would only
// read every imported resource again.
func replanValues(ctx context.Context, tf tfrun.Terraform, vars []tfexec.PlanOption, last *shownPlan) (*tfrun.ChangeSummary, map[string]map[string]any, map[string]string, error) {
	if last == nil || last.plan == nil || last.summary == nil {
		return planValues(ctx, tf, vars)
	}
	values, changes := valuesOf(last.plan)
	return last.summary, values, changes, nil
}

// valuesOf returns each resource's imported attribute values and planned
// change (see tfrun.ChangeSignature) in p, by address.
func valuesOf(p *tfjson.Plan) (map[string]map[string]any, map[string]string) {
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
		changes[rc.Address] = tfrun.ChangeSignature(rc)
	}
	return values, changes
}
