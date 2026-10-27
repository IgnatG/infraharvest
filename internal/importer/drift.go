// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/driftpolicy"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// DriftOptions say which roots Drift checks and how.
type DriftOptions struct {
	Out           string        // the output directory of the import
	Engine        string        // EngineTerraform or EngineTofu
	TerraformPath string        // the binary, or "" to find or install one
	EngineTimeout time.Duration // how long each engine command may run; 0 means no limit
	Format        string        // FormatText or FormatJSON
	Parallel      int           // how many roots to check at once
	// Catalogue is a directory of catalogue files that extend the
	// built-in catalogue, as the import's --catalogue: their state-only
	// arguments, defaults and arguments left out apply to the checks.
	Catalogue string
	// Roots are patterns of the roots' paths in Out, as path.Match takes
	// them, such as aws/*/eu-west-2: a root is checked if one matches its
	// path or a directory it is in. None checks every root.
	Roots []string
	// Rules decide which changes the next apply would revert to accept
	// into the code, and which to keep (see driftpolicy.Rule): the last
	// that matches a change decides it, and a change none matches is
	// kept. None edits nothing, as detection alone does. They decide
	// after Decisions.
	Rules []driftpolicy.Rule
	// Decisions is a decisions file to decide the changes by (see
	// driftpolicy.Decisions), or "" for none.
	Decisions string
	// WriteDecisions writes the run's changes into the decisions file,
	// Decisions, or driftpolicy.DefaultDecisionsFile in Out without it,
	// merged with what the file has (see driftpolicy.Decisions.Merge).
	// Without Decisions, the file it merges into isn't applied.
	WriteDecisions bool
	// WriteDecisionsTo, if not "", is the file WriteDecisions writes. One
	// Decisions doesn't name isn't applied, so that a decisions file kept
	// outside Out is written again without editing the code.
	WriteDecisionsTo string
	// DetailedExitCode exits with report.ExitDrift when drift is found,
	// and with report.ExitDriftRejected when an accepted change's edit
	// failed verification.
	DetailedExitCode bool
	ForceUnlock      bool // check even when another run holds the output directory's lock
	// Environments are, for the roots environments share (see
	// engine.MergedEnvironments), the environments to check each as, with
	// its backend settings and var files, or config.AllEnvironments for
	// every one under its vars/. None checks the one environment the
	// roots have, and fails on roots of more than one (see driftTargets).
	Environments []string
	// Provider and Names find each root's provider, for its catalogue
	// (see rootProvider).
	Provider func(name string) harvest.Provider
	Names    []string
	// Backends are the types of the backends drift reads state from,
	// besides local state (see engine.DriftOptions.Backends).
	Backends []string
	// Modules are the organisation's own modules, as the import's
	// configuration file lists them: the checks of an accepted change's
	// edited code leave those in directories to their owners, as the
	// import's verification gate does (see engine.DriftOptions.Adapters).
	Modules []config.Module
	// UntrustedRoots says the roots may come from someone else, as the
	// MCP server's runs take them: Terraform ignores the .terraform
	// directories they have and any filesystem mirror, and installs
	// providers from their registries alone (see untrustedTerraform).
	UntrustedRoots bool
	// RootsDigest, if set, is the driftpolicy.RootsDigest of Out the
	// roots must still have once the output directory is locked, before
	// Terraform runs: the MCP server's, for the roots the user confirmed.
	RootsDigest string

	// check checks one root, in place of the engine, for tests.
	check driftCheck
}

// driftCheck checks the root in dir, staging it in staging.
type driftCheck func(ctx context.Context, dir, staging string, opts engine.DriftOptions) (*engine.RootDrift, error)

// Drift checks the roots an import generated in options.Out for what
// changed in the cloud since they were applied (see engine.Drift), writes
// report/drift.json and report/drift.md, and prints a summary, or with
// --format json drift.json. It changes no state; it changes the code only
// to take the cloud's values of the changes options.Decisions and
// options.Rules accept, once the plan of the edited code says it takes
// them. With WriteDecisions, it writes the decisions file, which it
// changes only after every root is checked. A root it can't check,
// such as one with a remote backend, is reported and makes it exit with
// report.ExitCouldNotRun; with DetailedExitCode, an accepted change the
// plan rejected makes it exit with report.ExitDriftRejected, and drift
// left makes it exit with report.ExitDrift.
func Drift(ctx context.Context, options DriftOptions, w io.Writer) error {
	if err := checkDriftOptions(options); err != nil {
		return err
	}
	// Terraform runs in each root, so a path it's given, such as the
	// staged state's, must not be relative to where drift runs.
	out, err := filepath.Abs(options.Out)
	if err != nil {
		return err
	}
	options.Out = out
	if err := checkDriftCatalogue(options); err != nil {
		return err
	}
	decider := driftpolicy.Decider{Rules: options.Rules}
	if options.Decisions != "" {
		f, err := driftpolicy.LoadDecisions(options.Decisions)
		if err != nil {
			return fmt.Errorf("--decisions: %w", err)
		}
		decider.Decisions = f
	}
	// What --write-decisions merges into is read first, so that a file it
	// can't merge into fails the run before any root is refreshed.
	var merge *driftpolicy.Decisions
	if options.WriteDecisions {
		m, err := decisionsToMerge(options, decider.Decisions)
		if err != nil {
			return err
		}
		merge = m
	}
	all, err := generatedRoots(options.Out)
	if err != nil {
		return err
	}
	if len(all) == 0 {
		return fmt.Errorf("no generated roots in %s", options.Out)
	}
	var roots []string
	for _, dir := range all {
		if matchesRoot(options.Roots, relativePath(options.Out, dir)) {
			roots = append(roots, dir)
		}
	}
	if len(roots) == 0 {
		return fmt.Errorf("no root in %s matches --root %s", options.Out, strings.Join(options.Roots, ", "))
	}
	targets, err := driftTargets(roots, options.Environments)
	if err != nil {
		return err
	}
	// Its inits write into the roots, as verify's do.
	lock, err := lockOutput(options.Out, options.ForceUnlock)
	if err != nil {
		return err
	}
	if lock != nil {
		defer func() { _ = lock.Unlock() }()
	}
	if options.RootsDigest != "" {
		digest, err := driftpolicy.RootsDigest(options.Out)
		if err != nil {
			return err
		}
		if digest != options.RootsDigest {
			return fmt.Errorf("the roots in %s changed since they were checked: a file Terraform reads under it isn't as it was", options.Out)
		}
	}
	check := options.check
	if check == nil {
		if check, err = engineDriftCheck(ctx, options); err != nil {
			return err
		}
	}
	own := driftAdapters(ctx, options.Modules)

	results := make([]report.DriftRoot, len(targets))
	changes := make([][]report.DriftChange, len(targets))
	slots := make(chan struct{}, options.Parallel)
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				results[i] = report.DriftRoot{Path: relativePath(options.Out, target.dir), Environment: target.env, Error: ctx.Err().Error()}
				return
			}
			defer func() { <-slots }()
			results[i], changes[i] = checkDriftRoot(ctx, options, decider, check, own, target)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}

	d := &report.Drift{Roots: results}
	var failed []string
	for i, r := range results {
		d.Changes = append(d.Changes, changes[i]...)
		if r.Error != "" {
			failed = append(failed, r.Name()+": "+r.Error)
		}
	}
	d.Finish()
	if err := d.WriteFiles(options.Out); err != nil {
		return err
	}
	var written string
	if merge != nil {
		if written, err = writeDriftDecisions(driftDecisionsPath(options), merge, d); err != nil {
			return err
		}
	}
	if err := writeDriftSummary(w, d, options); err != nil {
		return err
	}
	if written != "" && options.Format == FormatText {
		if _, err := fmt.Fprintln(w, written); err != nil {
			return err
		}
	}
	if len(failed) > 0 {
		return &ExitError{Code: report.ExitCouldNotRun, Err: fmt.Errorf("%d roots couldn't be checked:\n%s", len(failed), strings.Join(failed, "\n"))}
	}
	where := filepath.Join(options.Out, report.Dir, report.DriftMarkdownFile)
	if rejected := d.Totals.RejectedByPlan + d.Totals.RejectedByCheck; options.DetailedExitCode && rejected > 0 {
		return &ExitError{Code: report.ExitDriftRejected, Err: fmt.Errorf("%d accepted changes failed verification, so they weren't written (see %s)", rejected, where)}
	}
	if options.DetailedExitCode && d.DriftFound {
		left := d.Totals.Kept + d.Totals.Manual + d.Totals.Secret
		return &ExitError{Code: report.ExitDrift, Err: fmt.Errorf("drift found: %d changes the next apply reverts, %d not assessable (see %s)", left, d.Totals.NotAssessable, where)}
	}
	return nil
}

func checkDriftOptions(options DriftOptions) error {
	if err := checkEngine(options.Engine); err != nil {
		return err
	}
	if options.Format != FormatText && options.Format != FormatJSON {
		return fmt.Errorf("--format must be %s or %s, not %q", FormatText, FormatJSON, options.Format)
	}
	if options.EngineTimeout < 0 {
		return errors.New("--engine-timeout can't be negative")
	}
	if options.Parallel < 1 {
		return errors.New("--parallel must be at least 1")
	}
	for _, p := range options.Roots {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("--root %q: %w", p, err)
		}
	}
	for _, r := range options.Rules {
		if _, err := driftpolicy.ParseRule(r.String(), r.Accept); err != nil {
			return err
		}
	}
	if options.WriteDecisionsTo != "" && options.Decisions != "" && filepath.Clean(options.WriteDecisionsTo) != filepath.Clean(options.Decisions) {
		return fmt.Errorf("--write-decisions=%s: with --decisions, it writes the file --decisions names, %s", options.WriteDecisionsTo, options.Decisions)
	}
	return nil
}

// checkDriftCatalogue loads options.Catalogue for each provider, so that
// a directory that doesn't load fails the run before any root is
// checked, rather than each root.
func checkDriftCatalogue(options DriftOptions) error {
	if options.Catalogue == "" || options.Provider == nil {
		return nil
	}
	for _, name := range options.Names {
		provider := options.Provider(name)
		if provider == nil {
			continue
		}
		if _, err := providerCatalogue(provider, options.Catalogue); err != nil {
			return fmt.Errorf("--catalogue: %w", err)
		}
	}
	return nil
}

// matchesRoot reports whether one of patterns matches rel, the path of a
// root, or a directory it is in; no patterns match every root.
func matchesRoot(patterns []string, rel string) bool {
	if len(patterns) == 0 {
		return true
	}
	segments := strings.Split(rel, "/")
	for _, p := range patterns {
		p = strings.Trim(filepath.ToSlash(p), "/")
		for i := 1; i <= len(segments); i++ {
			if ok, _ := path.Match(p, strings.Join(segments[:i], "/")); ok {
				return true
			}
		}
	}
	return false
}

// engineDriftCheck returns the check of a root through Terraform or
// OpenTofu, found or installed as for verify, with the environment's
// credentials.
func engineDriftCheck(ctx context.Context, options DriftOptions) (driftCheck, error) {
	binary := engineBinary(options.Engine)
	cacheDir, err := infraharvestCacheDir()
	if err != nil {
		return nil, err
	}
	execPath, err := binary.Find(ctx, options.TerraformPath, filepath.Join(cacheDir, binary.Name))
	if err != nil {
		return nil, err
	}
	plugins := filepath.Join(cacheDir, "plugins")
	return func(ctx context.Context, dir, staging string, opts engine.DriftOptions) (*engine.RootDrift, error) {
		var env map[string]string
		var rootEnv map[string]string
		if options.UntrustedRoots {
			private, err := os.MkdirTemp("", "infraharvest-drift-")
			if err != nil {
				return nil, err
			}
			defer func() { _ = os.RemoveAll(private) }()
			if env, err = untrustedTerraform(private); err != nil {
				return nil, err
			}
			opts.DataDir = filepath.Join(private, "data")
			rootEnv = maps.Clone(env)
			rootEnv["TF_DATA_DIR"] = opts.DataDir
		}
		newTF := func(dir string) (tfrun.DriftTerraform, error) {
			tf, err := tfrun.New(dir, execPath, plugins, env)
			if err != nil {
				return nil, err
			}
			return tf, nil
		}
		tf, err := tfrun.New(dir, execPath, plugins, rootEnv)
		if err != nil {
			return nil, err
		}
		return engine.Drift(ctx, tf, dir, staging, newTF, opts)
	}, nil
}

// untrustedTerraform returns the environment Terraform runs with on roots
// that may come from someone else, writing its CLI configuration into
// private, a directory of the run's own: providers are installed from
// their registries alone, which verify what they download against the
// lock file and their signatures, never from a filesystem mirror, such as
// a terraform.d directory the roots could carry. The root's own init
// keeps its working files in private too (see engine.DriftOptions.DataDir),
// rather than in the root's .terraform, whose providers and modules it
// would use as they are. The plugin cache is the user's, as ever.
func untrustedTerraform(private string) (map[string]string, error) {
	config := filepath.Join(private, "terraform.rc")
	if err := os.WriteFile(config, []byte("provider_installation {\n  direct {}\n}\n"), 0o600); err != nil {
		return nil, err
	}
	return map[string]string{"TF_CLI_CONFIG_FILE": config}, nil
}

// driftTarget is a root drift checks, and, for a root environments
// share, the environment it checks it as, and the lock the checks of its
// environments share (see engine.DriftOptions.WithRootLock).
type driftTarget struct {
	dir, env string
	lock     *sync.Mutex
}

// withRootLock runs f holding the lock of t's root.
func (t driftTarget) withRootLock(f func() error) error {
	t.lock.Lock()
	defer t.lock.Unlock()
	return f()
}

// driftTargets returns what drift checks of roots: each root, but a root
// environments share once per environment under its vars/ that envs
// names, or every one with envs config.AllEnvironments. Each environment
// is checked with the credentials drift runs with, so without envs,
// roots of more than one environment are an error: with the credentials
// of one, the resources of the others would look deleted. An environment
// of envs that no root has is an error too.
func driftTargets(roots, envs []string) ([]driftTarget, error) {
	all := slices.Contains(envs, config.AllEnvironments)
	if all && len(envs) > 1 {
		return nil, fmt.Errorf("--environment %s checks every environment, so it takes no other", config.AllEnvironments)
	}
	var targets []driftTarget
	found := map[string]bool{}
	for _, dir := range roots {
		merged, err := engine.MergedEnvironments(dir)
		if err != nil {
			return nil, err
		}
		if len(merged) == 0 {
			targets = append(targets, driftTarget{dir: dir})
			continue
		}
		lock := &sync.Mutex{}
		for _, env := range merged {
			found[env] = true
			if all || len(envs) == 0 || slices.Contains(envs, env) {
				targets = append(targets, driftTarget{dir: dir, env: env, lock: lock})
			}
		}
	}
	if len(envs) == 0 && len(found) > 1 {
		names := slices.Sorted(maps.Keys(found))
		return nil, fmt.Errorf("roots that the environments %s share are checked with the credentials drift runs with: set --environment to the one they are for, or to %s if they reach every one", strings.Join(names, ", "), config.AllEnvironments)
	}
	if all {
		return targets, nil
	}
	for _, env := range envs {
		if !found[env] {
			return nil, fmt.Errorf("--environment %s: no root the environments share has it under vars/", env)
		}
	}
	return targets, nil
}

// driftAdapters returns the adapters of those of modules, the
// organisation's own, that are directories (see fetchAdapters): the
// checks of the edited code leave only those to their owners (see
// engine.DriftOptions.Adapters), so drift doesn't fetch the others, from
// git or a registry, as the import does.
func driftAdapters(ctx context.Context, modules []config.Module) []adapters.Adapter {
	var local []config.Module
	for _, m := range modules {
		if config.IsLocalModule(m.Source) {
			local = append(local, m)
		}
	}
	// A directory is no other provider's (see forOtherProvider): the
	// adapters of each root's provider are picked from these by their
	// resources (see catalogueAdapters).
	return fetchAdapters(ctx, "", "", "", local)
}

// checkDriftRoot checks the root of target, staged under the output
// directory's checkpoints, which .gitignore excludes, with own, the
// adapters of the organisation's modules, and those of the curated
// modules of the root's provider, and returns it and its changes as the
// report has them.
func checkDriftRoot(ctx context.Context, options DriftOptions, decider driftpolicy.Decider, check driftCheck, own []adapters.Adapter, target driftTarget) (report.DriftRoot, []report.DriftChange) {
	dir, env := target.dir, target.env
	rel := relativePath(options.Out, dir)
	root := report.DriftRoot{Path: rel, Environment: env}
	var opts engine.DriftOptions
	if provider := rootProvider(options.Out, dir, VerifyOptions{Provider: options.Provider, Names: options.Names}); provider != nil {
		cat, err := providerCatalogue(provider, options.Catalogue)
		if err != nil {
			root.Error = err.Error()
			return root, nil
		}
		opts = catalogueDriftOptions(cat, lockedProviderVersion(dir, engineBinary(options.Engine).Registry, provider))
		// As the import passes its provider's modules alone, its own
		// first, then the curated ones of its family (see Run.toolchain),
		// whose adapters trace changes into their calls.
		own = slices.Concat(catalogueAdapters(own, provider.Name()), adapters.For(provider.Profile().Modules))
	}
	opts.CommandTimeout, opts.Environment, opts.Adapters, opts.Backends = options.EngineTimeout, env, own, options.Backends
	if target.lock != nil {
		opts.WithRootLock = target.withRootLock
	}
	// The decisions taken, by change, for the report to say where each
	// came from. Accept is called for one change at a time.
	decided := newDecidedDrift()
	if decider.Active() {
		opts.Accept = func(c engine.DriftChange) bool {
			d := decider.Decide(reportDriftChange(rel, env, c))
			decided.record(c, d)
			return d.Accept
		}
	}
	result, err := check(ctx, dir, driftStagingDir(options.Out, root.Name()), opts)
	if err != nil {
		root.Error = err.Error()
		return root, nil
	}
	root.Backend, root.Resources = result.Backend, result.Resources
	root.NotApplied, root.Placeholders = result.NotApplied, result.Placeholders
	changes := make([]report.DriftChange, 0, len(result.Changes))
	for _, c := range result.Changes {
		decision := report.DriftKeep
		if c.Decision == engine.DriftAccept {
			decision = report.DriftAccept
		}
		rc := reportDriftChange(rel, env, c)
		rc.Decision, rc.Outcome, rc.Reason, rc.ForcesReplacement = decision, c.Outcome, c.Reason, c.ForcesReplacement
		d := decided.of(c, func() driftpolicy.Decision { return decider.Decide(rc) })
		rc.DecisionSource, rc.DecisionStale = d.Source, d.Stale
		if c.Source != nil {
			rc.Source = &report.DriftSource{File: c.Source.File, Line: c.Source.Line, Expression: c.Source.Expression, ModulePath: c.Source.ModulePath}
		}
		changes = append(changes, rc)
	}
	return root, changes
}

// reportDriftChange is c, a change of the root at rel as environment env,
// as the report has it, before it is decided.
func reportDriftChange(rel, env string, c engine.DriftChange) report.DriftChange {
	return report.DriftChange{
		Root: rel, Environment: env, Address: c.Address, Type: c.Type, ModuleCall: c.ModuleAddress, Attribute: c.Attribute,
		Before: c.Before, After: c.After, Kind: c.Kind, Covers: c.Covers,
	}
}

// driftStagingDir is where Drift stages the root at rel, or, for a root
// environments share, rel and the environment (see
// report.DriftRoot.Name): a directory of its own, named by a hash of
// rel, so that a root's never holds another's.
func driftStagingDir(out, rel string) string {
	sum := sha256.Sum256([]byte(rel))
	return filepath.Join(out, CheckpointDir, "drift", hex.EncodeToString(sum[:])[:16])
}

// writeDriftSummary prints d: as drift.json with --format json, else a
// line per root and where the report is.
func writeDriftSummary(w io.Writer, d *report.Drift, options DriftOptions) error {
	if options.Format == FormatJSON {
		return d.WriteJSON(w)
	}
	counts := map[string]map[string]int{}
	for _, c := range d.Changes {
		if counts[c.Name()] == nil {
			counts[c.Name()] = map[string]int{}
		}
		counts[c.Name()][c.Outcome]++
	}
	for _, r := range d.Roots {
		status := "no drift"
		switch {
		case r.Error != "":
			status = "not checked: " + strings.Join(strings.Fields(r.Error), " ")
		case r.Resources == 0:
			status = "not applied yet"
		case len(counts[r.Name()]) > 0:
			var parts []string
			for _, o := range []struct{ outcome, label string }{
				{report.DriftWritten, "accepted into the code"},
				{report.DriftKept, "reverted by the next apply"},
				{report.DriftRejectedByPlan, "rejected by the plan"},
				{report.DriftRejectedByCheck, "rejected by a check"},
				{report.DriftManual, "need a hand edit"},
				{report.DriftSecret, "secret"},
				{report.DriftNotAssessable, "not assessable"},
				{report.DriftAbsorbed, "absorbed"},
			} {
				if n := counts[r.Name()][o.outcome]; n > 0 {
					parts = append(parts, fmt.Sprintf("%d %s", n, o.label))
				}
			}
			status = strings.Join(parts, ", ")
		}
		if _, err := fmt.Fprintf(w, "%s: %s\n", r.Name(), status); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "report: %s\n", filepath.Join(options.Out, report.Dir, report.DriftMarkdownFile))
	return err
}
