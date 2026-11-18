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
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/run"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/logging"
)

// maxPlanRounds bounds how often Generate plans again after repairing the
// configuration or leaving resources out.
const maxPlanRounds = 3

// The steps of generate that Result.Plans counts plans by.
const (
	// stepGenerate has Terraform generate the configuration, and import
	// again what it rejected with an import ID to fall back on.
	stepGenerate = "generate"
	// stepPrepare plans the configuration again once edits that come before
	// anything reads it, such as sorting its resources, changed it.
	stepPrepare = "prepare"
	// stepRepair plans again after repairing the configuration or leaving
	// out what still had errors.
	stepRepair = "repair"
	// stepSnapshot imports the root once into a state snapshot, if
	// Options.ImportOnce is set.
	stepSnapshot = "snapshot"
	// stepSecrets makes secret attributes read from variables.
	stepSecrets = "secrets"
	// stepTidy post-processes the configuration, each edit checked by a
	// plan.
	stepTidy = "tidy"
	// stepFinish lays out the root's files.
	stepFinish = "finish"
	// stepRefine runs Options.Refine.
	stepRefine = "refine"
	// stepGate runs the verification gate.
	stepGate = "gate"
)

// Generate writes opts.Config and an import block per resource into dir,
// then runs Terraform to generate the configuration of every imported
// resource into generated.tf, without the arguments opts.Omit names.
// Resource names become labels (see Label). If Terraform
// rejects what it generated, Generate repairs it (see repair) and plans
// again. Resources it still can't plan are left out (see Rejection). Secret
// values Terraform doesn't write into the configuration become sensitive
// variables in variables.tf (see secret.Variable). Last, it writes a README with
// what is left to do.
//
// Generate fails if Terraform can't plan the directory at all. It refuses
// to overwrite an existing generated.tf or imports.tf.
func Generate(ctx context.Context, tf tfrun.Terraform, dir string, imports []rootfile.Import, opts Options) (*Result, error) {
	return generate(ctx, tf, dir, imports, run.Generating(opts))
}

// generate is Generate's pipeline, run with r's options. It runs in
// stages, each a method of generation: it writes the root and has
// Terraform generate its configuration (write, planGenerated), moves the
// secrets that holds into variables (hideSecrets), plans until what it
// keeps is valid (planUntilValid), leaves out what the root doesn't
// import (leaveOut), makes the secret attributes read from variables
// (secretVariables), tidies the configuration (tidy, finish) and gates it
// (gate). Last, it writes the README.
func generate(ctx context.Context, tf tfrun.Terraform, dir string, imports []rootfile.Import, r *run.Run) (result *Result, err error) {
	if len(imports) == 0 {
		return nil, errors.New("no resources to import")
	}
	run.WarnLargeRoot(dir, len(imports))
	generated := filepath.Join(dir, rootfile.GeneratedFileName)
	// imports.tf too: with SplitFiles, an earlier run leaves no generated.tf.
	for _, path := range []string{generated, filepath.Join(dir, rootfile.ImportsFileName)} {
		if _, err := os.Stat(path); err == nil {
			return nil, fmt.Errorf("%s already exists; use an empty output directory", path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	// Several steps need the providers' schemas: read them once per init.
	// No plan shows the secret values Generate finds (see
	// secret.RedactingTerraform), and none reads the state the root may have
	// locally (see tfrun.WithoutLocalState).
	raw := tf
	tf, removeEmptyState, err := tfrun.WithoutLocalState(tf, dir)
	if err != nil {
		return nil, err
	}
	defer removeEmptyState()
	timed := tfrun.WithTimeout(tf, r.Opts.CommandTimeout)
	plain := timed
	// With Options.ImportOnce the plans after leaveOut run against a state
	// snapshot (see buildSnapshot), until the gate, which plans as the user
	// will.
	cache := cachedRunner(timed, raw, dir, r.Opts)
	if cache != nil {
		plain = cache
	}
	defer cache.Release()
	defer func() {
		if result != nil {
			result.UsedSnapshot = cache.CachedPlans() > 0
		}
		if err != nil && cache.CachedPlans() > 0 {
			err = &SnapshotError{Err: err}
		}
	}()
	redacting := &secret.RedactingTerraform{Terraform: plain}
	counting := &tfrun.Counting{Terraform: redacting}
	g := &generation{
		tf:        &schemaCache{Terraform: counting},
		redacting: redacting,
		counting:  counting,
		cache:     cache,
		dir:       dir,
		generated: generated,
		imports:   relabelled(imports, r.Taken, r.Opts.TidyLabels, r.Opts.Previous),
		r:         r,
		opts:      r.Opts,
		result:    &Result{},
		leftOut:   map[string]bool{},
	}
	if cache != nil {
		cache.MoveIn(g.moveCopy)
		g.followProviders()
	}
	restoreAccess, err := g.write(ctx)
	if err != nil {
		return nil, err
	}
	counting.Step(stepGenerate)
	reasons, err := g.planGenerated(ctx)
	if err != nil {
		return nil, err
	}
	// The values of the secrets hideSecrets moves, which the run's plans
	// read, go with the run.
	defer func() { r.Secrets.Remove() }()
	if err := g.hideSecrets(); err != nil {
		return nil, err
	}
	if len(g.literal) > 0 {
		reasons = append(reasons, replanSecrets)
	}
	if err := restoreAccess(); err != nil {
		return nil, err
	}
	if err := g.planUntilValid(ctx, reasons); err != nil {
		return nil, err
	}
	if err := g.leaveOut(); err != nil {
		return nil, err
	}
	if err := g.buildSnapshot(ctx); err != nil {
		return nil, err
	}
	counting.Step(stepSecrets)
	if err := g.secretVariables(ctx); err != nil {
		return nil, err
	}
	counting.Step(stepTidy)
	if err := g.tidy(ctx); err != nil {
		return nil, err
	}
	counting.Step(stepFinish)
	if err := g.finish(ctx); err != nil {
		return nil, err
	}
	if err := g.gate(ctx); err != nil {
		return nil, err
	}
	g.result.Plans = counting.Counts()
	// The README shows the gate's details, which come from plans that had
	// the secret values: the gate couldn't scan it, so it is scanned here.
	redactResult(g.result, g.literal)
	if err := WriteReadme(dir, g.result); err != nil {
		return g.result, err
	}
	return g.result, checkReadme(dir, g.result, secret.KnownValues(g.literal))
}

// generation is the state generate's stages share as they generate a
// root in dir.
type generation struct {
	// tf runs Terraform in dir, without showing the secret values hidden
	// with redacting.
	tf        tfrun.Terraform
	redacting *secret.RedactingTerraform
	// counting counts tf's plans by the step generate is at.
	counting *tfrun.Counting
	// cache is tf's plain runner that plans against the state snapshot, if
	// Options.ImportOnce is set (see buildSnapshot).
	cache *tfrun.Cached
	dir   string
	// generated is the path of generated.tf.
	generated string
	imports   []rootfile.Import
	r         *run.Run
	opts      Options
	// literal are the secrets found in the configuration Terraform
	// generated (see hideSecrets).
	literal []secret.Variable
	// diags are the errors of the last plan.
	diags []tfjson.Diagnostic
	// last is the last of the plans planUntilValid makes, if it had no
	// errors, and planned the files it read: tidy starts from it if they
	// are still the same then.
	last    *shownPlan
	planned map[string][]byte
	result  *Result
	// rejected is the configuration of the resources left out, for
	// rejected.tf.
	rejected bytes.Buffer
	// leftOut are the addresses of the resources the root doesn't import.
	leftOut map[string]bool
}

// write writes the root's files and initialises it. It makes dir private
// first and returns what makes it public again, once the secrets are out
// of generated.tf.
func (g *generation) write(ctx context.Context) (restoreAccess func() error, err error) {
	opts, dir := g.opts, g.dir
	importsHCL, err := rootfile.ImportsFile(g.imports)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// Terraform writes every value the provider doesn't mark sensitive
	// into generated.tf, secrets among them, and with the mode it chooses.
	// The directory stays private until those secrets are moved out of
	// the file (see hideSecrets); should Generate fail before that, it
	// stays so, and the file stays for the caller to remove, as the
	// importer does with what it wrote of a root that fails. On Windows,
	// where a mode restricts nothing, its access list does it.
	if restoreAccess, err = fsutil.MakePrivate(dir); err != nil {
		return nil, err
	}
	ws, err := run.OpenWorkspace(dir)
	if err != nil {
		return nil, err
	}
	ws.Write(rootfile.ImportsFileName, importsHCL)
	for name, content := range opts.Config {
		ws.Write(name, content)
	}
	if dt := opts.DefaultTags; dt != nil && dt.Applied != nil {
		if err := applyDefaultTags(ws, *dt); err != nil {
			return nil, err
		}
	}
	// Resources whose type another provider of opts.ModuleProviders
	// implies, such as azapi_resource, need it required and configured
	// before init, which selects its newest release, pinned as the
	// root's own provider is.
	others := resourceProviders(g.imports, opts.ModuleProviders)
	if err := requireProviders(ws, others, opts.ModuleProviders); err != nil {
		return nil, err
	}
	if err := tfrun.Init(ctx, ws.Terraform(g.tf)); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	if len(others) > 0 {
		if err := pinModuleProviders(ws, opts.ModuleProviders); err != nil {
			return nil, err
		}
	}
	return restoreAccess, ws.Flush()
}

// planGenerated has Terraform generate generated.tf, imports again what
// it rejected that has an import ID to fall back on, and makes the edits
// that come before anything reads the file (see generatedAnnotations). It
// returns why the file then needs another plan, if it does.
func (g *generation) planGenerated(ctx context.Context) ([]string, error) {
	// Plan reports changes because every import is pending; only errors matter.
	// The release the lock file records decides how far the provider's
	// metadata goes (see schemaHints).
	hints := schemaHintsFor(g.dir, g.opts.SchemaMetadata)
	diags, _, err := tfrun.Plan(ctx, g.tf, tfexec.GenerateConfigOut(rootfile.GeneratedFileName))
	if err != nil {
		return nil, fmt.Errorf("terraform plan: %w", err)
	}
	if _, err := os.Stat(g.generated); errors.Is(err, fs.ErrNotExist) {
		// Terraform stopped before generating anything, for example on the
		// provider configuration.
		if len(diags) == 0 {
			return nil, errors.New("terraform plan generated no configuration")
		}
		return nil, fmt.Errorf("terraform plan: %w", tfrun.DiagnosticsError(diags))
	} else if err != nil {
		return nil, err
	}
	// What Terraform rejected that has an import ID to fall back on is
	// imported again by it, before anything reads the configuration.
	if g.diags, err = retryFallbacks(ctx, g.tf, g.dir, g.imports, diags); err != nil {
		return nil, err
	}
	gone, err := g.leaveGone()
	if err != nil {
		return nil, err
	}
	reasons, err := g.annotate(func(*run.Workspace) []annotation { return generatedAnnotations(g.imports, g.opts, hints) })
	if err != nil {
		return nil, err
	}
	if gone && len(g.diags) > 0 {
		// The errors left point at lines the move changed.
		reasons = append(reasons, replanGone)
	}
	return reasons, nil
}

// leaveGone leaves out of the root the resources whose import found no
// object, as the errors of the last plan say, with that error, and reports
// whether it did. Terraform generates nothing for them, so the root's next
// plan would fail on their import blocks instead, for want of
// configuration, and say nothing of why. Such a resource may have been
// deleted since it was listed, or its import ID may be wrong: the import
// can't tell, so it is left out like any other, but for those
// Options.MayBeGone names, which go in Result.Gone.
func (g *generation) leaveGone() (bool, error) {
	rejections, gone, rest, err := dropGone(g.dir, g.diags, &g.rejected, g.opts.MayBeGone)
	if err != nil || len(rejections)+len(gone) == 0 {
		return false, err
	}
	g.diags = rest
	g.result.Rejected = append(g.result.Rejected, rejections...)
	g.result.Gone = append(g.result.Gone, gone...)
	return true, nil
}

// hideSecrets moves the secrets in generated.tf into variables.
// Terraform wrote every value the provider doesn't mark sensitive into the
// file, secrets among them: they move before anything else reads it.
// Generate's own plans read their values from outside the output (see
// secret.Values), which the run's secrets hold once there are any.
func (g *generation) hideSecrets() error {
	literal, warnings, err := secret.LiteralsToVariables(g.generated, g.r.Taken)
	if err != nil {
		return err
	}
	g.literal, g.result.Warnings = literal, warnings
	if len(literal) == 0 {
		return nil
	}
	g.redacting.Hide(literal)
	if g.r.Secrets, err = secret.NewValues(); err != nil {
		return err
	}
	if err := g.r.Secrets.Write(literal); err != nil {
		return err
	}
	return writeVariables(g.dir, literal, nil)
}

// replan plans the root again, for its errors and, if it has none, its
// plan (see generation.last).
func (g *generation) replan(ctx context.Context) error {
	shown, summary, ds, err := tfrun.ShowPlan(ctx, g.tf, g.r.Secrets.Options())
	if err != nil {
		return err
	}
	g.diags, g.last, g.planned = ds, nil, nil
	if len(ds) == 0 && shown != nil {
		if g.planned, err = rootFileContents(g.dir); err != nil {
			return err
		}
		g.last = &shownPlan{plan: shown, summary: summary}
	}
	return nil
}

// planUntilValid plans the root until it has no errors: the plan the
// configuration was generated with no longer holds once an edit changed
// it, for reasons. Terraform writes generated.tf even when the
// configuration it generated is invalid: it is repaired once (see
// repair), and the resources that still have errors then are left out
// (see Rejection). It fails on errors it can't attribute to a resource,
// and once maxPlanRounds plans still had errors.
func (g *generation) planUntilValid(ctx context.Context, reasons []string) error {
	if len(reasons) > 0 {
		logging.Infof("%s: planning %s again: %s", g.dir, rootfile.GeneratedFileName, strings.Join(reasons, ", "))
		g.counting.Step(stepPrepare)
		if err := g.replan(ctx); err != nil {
			return err
		}
	}
	g.counting.Step(stepRepair)
	repaired := false
	for round := 0; ; round++ {
		gone, err := g.leaveGone()
		if err != nil {
			return err
		}
		if gone && len(g.diags) > 0 {
			if err := g.replan(ctx); err != nil {
				return err
			}
		}
		errs, err := unresolved("terraform plan", g.dir, g.diags)
		if err != nil {
			// Errors can quote the values they reject, as below.
			return errors.New(secret.Redact(err.Error(), g.literal))
		}
		if len(errs) == 0 {
			return nil
		}
		if round == maxPlanRounds {
			var all []tfjson.Diagnostic
			for _, ds := range errs {
				all = append(all, ds...)
			}
			// Errors can quote the values they reject, as below.
			return fmt.Errorf("terraform plan: %s", secret.Redact(tfrun.DiagnosticsError(all).Error(), g.literal))
		}
		// Repair once; leave out what is left.
		changed := false
		if !repaired {
			repaired = true
			if changed, err = repair(ctx, g.tf, g.generated); err != nil {
				return err
			}
		}
		if !changed {
			rejections, err := reject(g.dir, errs, &g.rejected)
			if err != nil {
				return err
			}
			g.result.Rejected = append(g.result.Rejected, rejections...)
		}
		if err := g.replan(ctx); err != nil {
			return err
		}
	}
}

// leaveOut leaves out the resources the root doesn't import though their
// configuration is valid: those Terraform generated nothing for, and,
// unless opts.IncludeOwned, those another resource owns.
func (g *generation) leaveOut() error {
	// Terraform generates nothing for a resource its import finds gone, such
	// as a KMS key scheduled for deletion, and only a plan without
	// -generate-config-out says so: leave those out too.
	missing, err := unconfigured(g.dir)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		rejections, err := reject(g.dir, missing, &g.rejected)
		if err != nil {
			return err
		}
		g.result.Rejected = append(g.result.Rejected, rejections...)
	}
	if !g.opts.IncludeOwned {
		if g.result.Owned, err = leaveOutOwned(g.dir, g.opts.KeepOwned, g.opts.Traits); err != nil {
			return err
		}
		for _, o := range g.result.Owned {
			g.leftOut[o.Address] = true
		}
	}
	return nil
}

// secretVariables makes the secret attributes of the resources the root
// keeps read from variables (see useVariables), and those whose values
// aren't secret after all literals again (see secret.PublicToLiterals).
func (g *generation) secretVariables(ctx context.Context) error {
	var kept []secret.Variable
	for _, s := range withoutRejected(g.literal, g.result.Rejected) {
		if !g.leftOut[s.Address] {
			kept = append(kept, s)
		}
	}
	// Both read the imported objects from one plan: planUntilValid's last,
	// if it had no errors, or else the first of their own that has none.
	// What an import reads doesn't depend on the configuration, so the
	// edits since then, and their own, don't change what it shows.
	var imported *tfjson.Plan
	if g.last != nil {
		imported = g.last.plan
	}
	importedPlan := func(vars []tfexec.PlanOption) func() (*tfjson.Plan, error) {
		return func() (*tfjson.Plan, error) {
			if imported != nil {
				return imported, nil
			}
			p, _, _, err := tfrun.ShowPlan(ctx, g.tf, vars)
			imported = p
			return p, err
		}
	}
	var err error
	if g.result.Secrets, err = useVariables(ctx, g.tf, g.dir, g.r.Taken, kept, &g.result.Rejected, &g.rejected, importedPlan(g.r.Secrets.Options())); err != nil {
		return err
	}
	// The secrets read from variables now: they plan with placeholders.
	vars := append(secret.Placeholders(g.result.Secrets), g.r.Secrets.Options()...)
	var public bool
	if g.result.Secrets, public, err = secret.PublicToLiterals(filepath.Join(g.dir, rootfile.GeneratedFileName), g.result.Secrets, importedPlan(vars)); err != nil {
		return err
	}
	if public {
		if err := writeVariables(g.dir, g.result.Secrets, nil); err != nil {
			return err
		}
	}
	if g.r.Secrets != nil {
		// Only the variables still declared: Terraform warns about others.
		if err := g.r.Secrets.Write(g.result.Secrets); err != nil {
			return err
		}
	}
	return nil
}

// tidy post-processes the configuration (see postProcess), then groups
// what it moved into modules into the root's module calls, and gives the
// secrets the addresses their resources moved to.
func (g *generation) tidy(ctx context.Context) error {
	before, err := importTargets(g.dir)
	if err != nil {
		return err
	}
	// The last plan of planUntilValid is postProcess's first if nothing
	// changed since: the same files, and the same values of the secret
	// variables, which planUntilValid's plans got from the secrets
	// hideSecrets found, and postProcess's get from those the root keeps,
	// with placeholders for the secrets without a value.
	last := g.last
	if last != nil && (len(secret.Placeholders(g.result.Secrets)) > 0 || !secret.SameValues(g.literal, g.result.Secrets)) {
		last = nil
	}
	if last != nil {
		same, err := sameFiles(g.dir, g.planned)
		if err != nil {
			return err
		}
		if !same {
			last = nil
		}
	}
	declined, err := postProcess(ctx, g.tf, g.dir, g.r, g.result.Secrets, last)
	if err != nil {
		return err
	}
	if _, err := g.annotate(func(ws *run.Workspace) []annotation { return referencedAnnotations(ws, g.opts, g.r.Taken) }); err != nil {
		return err
	}
	moved, err := movedAddresses(g.dir, before)
	if err != nil {
		return err
	}
	if g.result.Modules, err = moduleCalls(g.dir, moved, declined); err != nil {
		return err
	}
	for i, s := range g.result.Secrets {
		if to, ok := moved[s.Address]; ok {
			g.result.Secrets[i].Address = to
		}
	}
	return nil
}

// finish writes rejected.tf, settles what the root imports, and lays out
// its files: split by concern if opts.SplitFiles, with the moved and
// removed blocks the addresses the root had before call for, and the
// Terraform release they need.
func (g *generation) finish(ctx context.Context) error {
	opts, dir, result := g.opts, g.dir, g.result
	// Errors can quote the values they reject.
	for i := range result.Rejected {
		for j, e := range result.Rejected[i].Errors {
			result.Rejected[i].Errors[j] = secret.Redact(e, g.literal)
		}
	}
	if g.rejected.Len() > 0 {
		if err := fsutil.WriteFile(filepath.Join(dir, rootfile.RejectedFileName), secret.RedactBytes(g.rejected.Bytes(), g.literal), 0o644); err != nil {
			return err
		}
	}
	for _, r := range result.Rejected {
		g.leftOut[r.Address] = true
	}
	for _, imp := range g.imports {
		if !g.leftOut[imp.Type+"."+imp.Name] {
			result.Imported = append(result.Imported, imp)
		}
	}
	var err error
	if opts.SplitFiles {
		if result.Files, err = splitFiles(dir, result.Imported, result.Modules); err != nil {
			return err
		}
	}
	if err := typeLiteralsOf(ctx, g.tf, dir); err != nil {
		return err
	}
	if err := hclfile.LayoutDir(dir); err != nil {
		return err
	}
	if result.Addresses, err = ImportedAddresses(dir); err != nil {
		return err
	}
	// Last, once the addresses are final: the gate's plans read the moved
	// and removed blocks too, and they may need a later release.
	result.Moves, result.Retyped, result.Unmoved = moves(opts.Previous, result.Addresses, callSources(result.Modules))
	result.Removed = removals(opts.Previous, result.Addresses)
	result.Arrived = arrived(opts.Arrivals, result.Addresses)
	if err := rootfile.WriteMoves(dir, result.Moves, result.Removed, result.Retyped); err != nil {
		return err
	}
	return rootfile.RequireMinimumVersion(dir)
}

// gate refines the finished root if opts.Refine is set, then gates it
// (see gateRefined).
func (g *generation) gate(ctx context.Context) error {
	// The gate plans the root as the user will: with import blocks, no
	// state.
	g.cache.Release()
	var unrefined map[string][]byte
	warned := len(g.result.Warnings)
	if g.opts.Refine != nil {
		g.counting.Step(stepRefine)
		var err error
		if unrefined, err = refine(ctx, g.tf, g.dir, g.result, g.opts); err != nil {
			return err
		}
	}
	// The gate checks the modules the run may call, which for Add's are
	// fewer than the caller's (see adding).
	g.counting.Step(stepGate)
	return gateRefined(ctx, g.tf, g.dir, g.result, g.opts, g.r.Secrets, unrefined, warned)
}

// unresolved returns the errors in diags by resource, leaving out errors
// about secret attributes, which variables resolve (see useVariables). It
// fails on errors it can't attribute to a resource.
func unresolved(command, dir string, diags []tfjson.Diagnostic) (map[string][]tfjson.Diagnostic, error) {
	if len(diags) == 0 {
		return nil, nil
	}
	generated, err := hclfile.Load(filepath.Join(dir, rootfile.GeneratedFileName))
	if err != nil {
		return nil, err
	}
	imports, err := hclfile.Load(filepath.Join(dir, rootfile.ImportsFileName))
	if err != nil {
		return nil, err
	}
	errs, general := byResource(diags, generated, imports)
	if len(general) > 0 {
		return nil, fmt.Errorf("%s: %w", command, tfrun.DiagnosticsError(diags))
	}
	secrets := secret.FindSensitive(generated)
	for addr, ds := range errs {
		var left []tfjson.Diagnostic
		for _, d := range ds {
			if !secret.About(d, secrets[addr]) {
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
// sensitive variables, declared in variables.tf with those of literal (the
// secrets infraharvest found in the configuration, which read from
// variables already), named other than the variables in taken, and
// validates the result. It leaves out resources that still don't validate,
// adding them to rejections and out. It returns literal's variables first.
// Secret attributes the imported objects hold no value for are left out of
// the configuration instead (see secret.WithoutUnset); imported returns the
// plan that shows those objects, or nil if the root doesn't plan.
func useVariables(ctx context.Context, tf tfrun.Terraform, dir string, taken Names, literal []secret.Variable, rejections *[]Rejection, out *bytes.Buffer, imported func() (*tfjson.Plan, error)) ([]secret.Variable, error) {
	generated, err := hclfile.Load(filepath.Join(dir, rootfile.GeneratedFileName))
	if err != nil {
		return nil, err
	}
	found := secret.FindSensitive(generated)
	var schemas *tfjson.ProviderSchemas
	if len(found) > 0 {
		if schemas, err = tf.ProvidersSchema(ctx); err != nil {
			return nil, fmt.Errorf("terraform providers schema: %w", err)
		}
		found = secret.WithoutWriteOnly(found, schemas)
	}
	if len(found) > 0 {
		p, err := imported()
		if err != nil {
			return nil, err
		}
		var unset bool
		if found, unset = secret.WithoutUnset(found, schemas, p); unset {
			if err := generated.Save(); err != nil {
				return nil, err
			}
		}
	}
	if len(found) == 0 {
		// Written again, without the variables of resources left out since.
		return literal, writeVariables(dir, literal, schemas)
	}
	secrets := append(slices.Clone(literal), secret.ToVariables(generated, found, withVariables(taken, literal), schemas)...)
	if err := generated.Save(); err != nil {
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
	errs, err := unresolved("terraform validate", dir, tfrun.ErrorDiagnostics(validation.Diagnostics))
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
func writeVariables(dir string, secrets []secret.Variable, schemas *tfjson.ProviderSchemas) error {
	path := filepath.Join(dir, hclfile.VariablesFileName)
	if len(secrets) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	content, err := secret.VariablesFile(secrets, schemas)
	if err != nil {
		return err
	}
	return fsutil.WriteFile(path, content, 0o644)
}

// schemaCache is a Terraform that reads the providers' schemas once, until
// the next init, which may install other providers, such as a module's.
type schemaCache struct {
	tfrun.Terraform
	schemas *tfjson.ProviderSchemas
	read    bool
}

func (c *schemaCache) Init(ctx context.Context, opts ...tfexec.InitOption) error {
	c.schemas, c.read = nil, false
	return c.Terraform.Init(ctx, opts...)
}

func (c *schemaCache) ProvidersSchema(ctx context.Context) (*tfjson.ProviderSchemas, error) {
	if c.read {
		return c.schemas, nil
	}
	schemas, err := c.Terraform.ProvidersSchema(ctx)
	if err != nil {
		return nil, err
	}
	c.schemas, c.read = schemas, true
	return schemas, nil
}

// rootFileContents returns the content of the regular files directly in dir, by
// name: the configuration a plan of dir reads, but for the providers and
// modules init installs.
func rootFileContents(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		files[e.Name()] = content
	}
	return files, nil
}

// sameFiles reports whether the regular files directly in dir are those
// of files, with the same content (see rootFileContents).
func sameFiles(dir string, files map[string][]byte) (bool, error) {
	now, err := rootFileContents(dir)
	if err != nil {
		return false, err
	}
	return maps.EqualFunc(now, files, bytes.Equal), nil
}

// withoutRejected returns the secrets of resources rejections doesn't
// name.
func withoutRejected(secrets []secret.Variable, rejections []Rejection) []secret.Variable {
	left := map[string]bool{}
	for _, r := range rejections {
		left[r.Address] = true
	}
	var kept []secret.Variable
	for _, s := range secrets {
		if !left[s.Address] {
			kept = append(kept, s)
		}
	}
	return kept
}

// withVariables returns taken with the variables of secrets added.
func withVariables(taken Names, secrets []secret.Variable) Names {
	if len(secrets) == 0 {
		return taken
	}
	names := Names{}
	for name := range taken {
		names[name] = true
	}
	for _, s := range secrets {
		names["var."+s.Variable] = true
	}
	return names
}
