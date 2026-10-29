// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// postProcess improves the configuration once it plans, with the passes of
// tidying.passes, in their order: the tags an import leaves out (see
// fillEffectiveTags), arguments that only repeat a default (see
// stripDefaults), references between resources (see
// addReferencesSkipping), to resources it doesn't manage (see
// addDataSourcesSkipping), to what other roots manage (see
// addRootVariables) and to what the cloud or the provider defines (see
// addLookups), shared tags (see applyTagLift) and repeated identifiers
// (see liftLiterals). The passes between two that plan on their own are
// checked together by one plan, and only if it changes, each against the
// plan before it, undone if the plan changes, the references by resource
// (see tidying.run). Secret variables get their
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
// from instead of planning. The passes edit the root's files in memory (see
// workspace), written to dir before each command Terraform runs and once
// they are done.
func postProcess(ctx context.Context, tf tfrun.Terraform, dir string, r *run, secrets []secret.Variable, planned *shownPlan) (declined []ModuleCall, err error) {
	ws, err := openWorkspace(dir)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, ws.Flush()) }()
	tf = ws.terraform(tf)
	vars := append(secret.Placeholders(secrets), r.secrets.Options()...)
	baseline, values, err := replanValues(ctx, tf, vars, planned, r.opts.StateOnly)
	if err != nil {
		return nil, err
	}
	if baseline == nil {
		return nil, liftLiteralsValidated(ctx, tf, ws, r.taken, r.opts.Traits)
	}
	t := &tidying{ctx: ctx, tf: tf, ws: ws, r: r, vars: vars, baseline: *baseline, values: values}
	if err := t.run(t.passes()); err != nil {
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

// passes returns postProcess's passes, declared in the order they usually
// run in, which what they run after (see tidyPass.after) settles. A pass the
// run doesn't make, such as the data sources of a run without external
// resources, does nothing.
func (t *tidying) passes() []tidyPass {
	ctx, tf, ws, opts, taken := t.ctx, t.tf, t.ws, t.r.opts, t.r.taken
	// eachResource makes a pass that makes less of itself for the
	// resources a plan blames it for (see resourceEdit).
	eachResource := func(name string, after []string, edit func(skip map[string]int) (map[string]bool, error), enabled bool) tidyPass {
		return tidyPass{name: name, after: after, rounds: maxEditRounds, start: func() (*tidyEdit, error) {
			if !enabled {
				return nil, nil
			}
			return &tidyEdit{edit: resourceEdit(edit)}, nil
		}}
	}
	return []tidyPass{
		// Tags an import leaves out of the configuration first: they are
		// what the passes below compare with, and the shared ones go to a
		// local.
		{name: passEffectiveTags, alone: true, policy: StateOnly, rounds: maxFillRounds, rebases: true, stopped: "the configuration stopped planning after writing its tags", start: t.effectiveTags},
		// Next, so the passes below see only the arguments that matter.
		{name: passDefaults, after: []string{passEffectiveTags}, alone: true, policy: FewerChanges, rounds: maxDefaultRounds, rebases: true, stopped: "the configuration stopped planning after leaving out defaults", start: t.defaults},
		eachResource(passReferences, []string{passDefaults}, func(skip map[string]int) (map[string]bool, error) {
			return addReferencesSkipping(ws, t.values, skip, opts.Traits)
		}, true),
		// After the references: data sources are for what the root
		// doesn't manage.
		eachResource(passDataSources, []string{passReferences}, func(skip map[string]int) (map[string]bool, error) {
			return addDataSourcesSkipping(ws, opts.External, opts.DataSources, taken, skip, opts.Traits)
		}, len(opts.External) > 0),
		// After the data sources: variables are for what they can't read.
		eachResource(passRootVariables, []string{passReferences, passDataSources}, func(skip map[string]int) (map[string]bool, error) {
			return addRootVariables(ws, opts.OtherRoots, opts.ARNs, opts.Traits, taken, skip)
		}, len(opts.OtherRoots) > 0),
		// Lookups are for what nothing the import lists has.
		eachResource(passLookups, []string{passReferences, passDataSources, passRootVariables}, func(skip map[string]int) (map[string]bool, error) {
			return addLookups(ws, taken, skip)
		}, true),
		{name: passTagLift, after: []string{passEffectiveTags, passDefaults}, start: t.tagLift},
		// Tags before literals, so a tag value is never made a local, and
		// the references first, so a literal they replace isn't.
		{name: passLiterals, after: []string{passTagLift, passReferences, passDataSources, passRootVariables, passLookups}, start: func() (*tidyEdit, error) {
			return &tidyEdit{edit: plainEdit(func() (bool, error) { return liftLiterals(ws, taken, opts.Traits) })}, nil
		}},
		// After the passes above, as clusters reach the modules through the
		// references.
		{name: passCuratedModules, after: []string{passReferences, passDataSources, passRootVariables, passLookups, passLiterals}, alone: true, policy: StateOnly, rounds: untilSettled, rebases: true, stopped: "the configuration stopped planning after moving resources into modules", start: t.synthesize},
		{name: passOutputLists, after: []string{passCuratedModules}, start: func() (*tidyEdit, error) {
			if len(t.lists) == 0 {
				return nil, nil
			}
			lists := t.lists
			return &tidyEdit{edit: plainEdit(func() (bool, error) {
				return collapseOutputLists(ws, lists, func() (*tfjson.ProviderSchemas, error) { return tf.ProvidersSchema(ctx) })
			})}, nil
		}},
		{name: passProjects, after: []string{passCuratedModules}, start: func() (*tidyEdit, error) {
			if len(opts.Adapters) == 0 {
				return nil, nil
			}
			return &tidyEdit{edit: plainEdit(func() (bool, error) { return referModulesToResources(ws) })}, nil
		}},
		// After the curated modules, whose calls take the project from the
		// resources.
		{name: passInherited, after: []string{passCuratedModules}, start: func() (*tidyEdit, error) {
			if len(opts.Inherited) == 0 {
				return nil, nil
			}
			return &tidyEdit{edit: plainEdit(func() (bool, error) { return omitInherited(ctx, tf, ws, opts.Inherited) })}, nil
		}},
		// Last: curated modules first, and the resources left no longer
		// repeat what the provider sets.
		{name: passLocalModules, after: []string{passCuratedModules, passInherited}, alone: true, start: t.localModules},
	}
}

// effectiveTags writes the tags an import leaves out of the configuration,
// with Options.EffectiveTags (see fillEffectiveTags).
func (t *tidying) effectiveTags() (*tidyEdit, error) {
	et := t.r.opts.EffectiveTags
	if et == nil {
		return nil, nil
	}
	return fillEffectiveTags(t.ws, *et, t.values), nil
}

// defaults leaves out the arguments that only repeat a default (see
// stripDefaults).
func (t *tidying) defaults() (*tidyEdit, error) {
	opts := t.r.opts
	rules := defaultRules{defaults: opts.Defaults, keepEmpty: opts.KeepEmpty, hints: schemaHintsFor(t.ws.dir, opts.SchemaMetadata)}
	if et := opts.EffectiveTags; et != nil {
		rules.tags = et.Attribute
	}
	return stripDefaults(t.ctx, t.tf, t.ws, rules)
}

// tagLift is the pass that moves the tags every resource shares into
// local.tags, with the run's default tags (see applyTagLift).
func (t *tidying) tagLift() (*tidyEdit, error) {
	if t.r.opts.DefaultTags == nil {
		return nil, nil
	}
	dt, ws := *t.r.opts.DefaultTags, t.ws
	// The schemas tell which resources the provider tags although they
	// set no tags.
	schemas, err := t.tf.ProvidersSchema(t.ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform providers schema: %w", err)
	}
	tags := &tidyEdit{edit: plainEdit(func() (bool, error) { return applyTagLift(ws, dt, schemas) })}
	if dt.Block != "" && dt.Applied == nil {
		// Where the provider can't apply the tags without changing a
		// resource's, the resources still share them through the local.
		local := dt
		local.Block, local.Argument = "", false
		tags.otherwise = func() (bool, error) { return applyTagLift(ws, local, schemas) }
	}
	return tags, nil
}

// localModules moves clusters that repeat with the same shape into local
// modules, in the run's modules directory (see liftModules).
func (t *tidying) localModules() (*tidyEdit, error) {
	if t.r.opts.ModulesDir == "" {
		return nil, nil
	}
	return liftModules(t.ctx, t.tf, t.ws, t.r.opts.ModulesDir, t.r.modules), nil
}

// liftLiteralsValidated lifts repeated identifiers when the configuration
// can't be planned, and undoes the lift if it doesn't validate. tf is the
// workspace's (see workspace.terraform).
func liftLiteralsValidated(ctx context.Context, tf tfrun.Terraform, ws *workspace, taken Names, t Traits) error {
	before := ws.Snapshot()
	lifted, err := liftLiterals(ws, taken, t)
	if err != nil || !lifted {
		return err
	}
	validation, err := tf.Validate(ctx)
	if err != nil {
		ws.Restore(before)
		return fmt.Errorf("terraform validate: %w", err)
	}
	if !validation.Valid {
		ws.Restore(before)
	}
	return nil
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
// errors, those of the plan that verdict says plan another change than the
// baseline's. It returns none if an error is about no resource, or the plan
// had no result.
func blame(ws *workspace, diags []tfjson.Diagnostic, verdict Verdict) (map[string]bool, error) {
	blamed := map[string]bool{}
	if len(diags) > 0 {
		generated, err := ws.Load(rootfile.GeneratedFileName)
		if err != nil {
			return nil, err
		}
		imports, err := ws.LoadOptional(rootfile.ImportsFileName)
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
	if verdict.Failed {
		return nil, nil
	}
	for _, rc := range verdict.Changed {
		blamed[rc.Address] = true
	}
	return blamed, nil
}

// planValues plans with vars and returns the baseline of the plan, with
// stateOnly as its state-only rules, and each resource's imported attribute
// values by address. It returns no baseline if the configuration doesn't
// plan.
func planValues(ctx context.Context, tf tfrun.Terraform, vars []tfexec.PlanOption, stateOnly map[string][]string) (*Baseline, map[string]map[string]any, error) {
	p, summary, _, err := tfrun.ShowPlan(ctx, tf, vars)
	if err != nil || p == nil {
		return nil, nil, err
	}
	baseline := newBaseline(shownPlan{p, summary}, stateOnly)
	return &baseline, valuesOf(p), nil
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
func replanValues(ctx context.Context, tf tfrun.Terraform, vars []tfexec.PlanOption, last *shownPlan, stateOnly map[string][]string) (*Baseline, map[string]map[string]any, error) {
	if last == nil || last.plan == nil || last.summary == nil {
		return planValues(ctx, tf, vars, stateOnly)
	}
	baseline := newBaseline(*last, stateOnly)
	return &baseline, valuesOf(last.plan), nil
}

// valuesOf returns each resource's imported attribute values in p, by
// address.
func valuesOf(p *tfjson.Plan) map[string]map[string]any {
	values := map[string]map[string]any{}
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
	}
	return values
}
