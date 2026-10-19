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
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/IgnatG/infraharvest/internal/engine"
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
	// Roots are patterns of the roots' paths in Out, as path.Match takes
	// them, such as aws/*/eu-west-2: a root is checked if one matches its
	// path or a directory it is in. None checks every root.
	Roots []string
	// Rules decide which changes the next apply would revert to accept
	// into the code, and which to keep (see DriftRule): the last that
	// matches a change decides it, and a change none matches is kept.
	// None edits nothing, as detection alone does.
	Rules []DriftRule
	// DetailedExitCode exits with report.ExitDrift when drift is found,
	// and with report.ExitDriftRejected when an accepted change's edit
	// failed verification.
	DetailedExitCode bool
	ForceUnlock      bool // check even when another run holds the output directory's lock
	// Provider and Names find each root's provider, for its catalogue
	// (see rootProvider).
	Provider func(name string) harvest.Provider
	Names    []string

	// check checks one root, in place of the engine, for tests.
	check driftCheck
}

// driftCheck checks the root in dir, staging it in staging.
type driftCheck func(ctx context.Context, dir, staging string, opts engine.DriftOptions) (*engine.RootDrift, error)

// Drift checks the roots an import generated in options.Out for what
// changed in the cloud since they were applied (see engine.Drift), writes
// report/drift.json and report/drift.md, and prints a summary, or with
// --format json drift.json. It changes no state; it changes the code only
// to take the cloud's values of the changes options.Rules accept, once
// the plan of the edited code says it takes them. A root it can't check,
// such as one with a remote backend, is reported and makes it exit with
// report.ExitCouldNotRun; with DetailedExitCode, an accepted change the
// plan rejected makes it exit with report.ExitDriftRejected, and drift
// left makes it exit with report.ExitDrift.
func Drift(ctx context.Context, options DriftOptions, w io.Writer) error {
	if err := checkDriftOptions(options); err != nil {
		return err
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
	// Its inits write into the roots, as verify's do.
	lock, err := lockOutput(options.Out, options.ForceUnlock)
	if err != nil {
		return err
	}
	if lock != nil {
		defer func() { _ = lock.Unlock() }()
	}
	check := options.check
	if check == nil {
		if check, err = engineDriftCheck(ctx, options); err != nil {
			return err
		}
	}

	results := make([]report.DriftRoot, len(roots))
	changes := make([][]report.DriftChange, len(roots))
	slots := make(chan struct{}, options.Parallel)
	var wg sync.WaitGroup
	for i, dir := range roots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				results[i] = report.DriftRoot{Path: relativePath(options.Out, dir), Error: ctx.Err().Error()}
				return
			}
			defer func() { <-slots }()
			results[i], changes[i] = checkDriftRoot(ctx, options, check, dir)
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
			failed = append(failed, r.Path+": "+r.Error)
		}
	}
	d.Finish()
	if err := d.WriteFiles(options.Out); err != nil {
		return err
	}
	if err := writeDriftSummary(w, d, options); err != nil {
		return err
	}
	if len(failed) > 0 {
		return &ExitError{Code: report.ExitCouldNotRun, Err: fmt.Errorf("%d roots couldn't be checked:\n%s", len(failed), strings.Join(failed, "\n"))}
	}
	where := filepath.Join(options.Out, report.Dir, report.DriftMarkdownFile)
	if options.DetailedExitCode && d.Totals.RejectedByPlan > 0 {
		return &ExitError{Code: report.ExitDriftRejected, Err: fmt.Errorf("%d accepted changes failed verification, so they weren't written (see %s)", d.Totals.RejectedByPlan, where)}
	}
	if options.DetailedExitCode && d.DriftFound {
		left := d.Totals.Kept + d.Totals.Manual + d.Totals.Secret
		return &ExitError{Code: report.ExitDrift, Err: fmt.Errorf("drift found: %d changes the next apply reverts, %d not assessable (see %s)", left, d.Totals.NotAssessable, where)}
	}
	return nil
}

func checkDriftOptions(options DriftOptions) error {
	if options.Engine != EngineTerraform && options.Engine != EngineTofu {
		return fmt.Errorf("--engine must be %s or %s, not %q", EngineTerraform, EngineTofu, options.Engine)
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
		if _, err := ParseDriftRule(r.String(), r.Accept); err != nil {
			return err
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
	newTF := func(dir string) (engine.DriftTerraform, error) {
		tf, err := engine.NewTerraform(dir, execPath, plugins, nil)
		if err != nil {
			return nil, err
		}
		return tf, nil
	}
	return func(ctx context.Context, dir, staging string, opts engine.DriftOptions) (*engine.RootDrift, error) {
		tf, err := newTF(dir)
		if err != nil {
			return nil, err
		}
		return engine.Drift(ctx, tf, dir, staging, newTF, opts)
	}, nil
}

// checkDriftRoot checks the root in dir, staged under the output
// directory's checkpoints, which .gitignore excludes, and returns it and
// its changes as the report has them.
func checkDriftRoot(ctx context.Context, options DriftOptions, check driftCheck, dir string) (report.DriftRoot, []report.DriftChange) {
	rel := relativePath(options.Out, dir)
	root := report.DriftRoot{Path: rel}
	opts := engine.DriftOptions{CommandTimeout: options.EngineTimeout}
	if len(options.Rules) > 0 {
		opts.Accept = func(c engine.DriftChange) bool { return acceptsDrift(options.Rules, rel, c) }
	}
	if provider := rootProvider(options.Out, dir, VerifyOptions{Provider: options.Provider, Names: options.Names}); provider != nil {
		cat, err := providerCatalogue(provider, "")
		if err != nil {
			root.Error = err.Error()
			return root, nil
		}
		v := lockedProviderVersion(dir, engineBinary(options.Engine).Registry, provider)
		opts.StateOnly, opts.Defaults, opts.Omit = cat.StateOnlyFor(v), cat.DefaultsFor(v), cat.OmitFor(v)
	}
	result, err := check(ctx, dir, driftStagingDir(options.Out, rel), opts)
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
		rc := report.DriftChange{
			Root: rel, Address: c.Address, Type: c.Type, ModuleCall: c.ModuleAddress, Attribute: c.Attribute,
			Before: c.Before, After: c.After, Kind: c.Kind, Decision: decision, Outcome: c.Outcome, Reason: c.Reason,
			ForcesReplacement: c.ForcesReplacement, Covers: c.Covers,
		}
		if c.Source != nil {
			rc.Source = &report.DriftSource{File: c.Source.File, Line: c.Source.Line, Expression: c.Source.Expression, ModulePath: c.Source.ModulePath}
		}
		changes = append(changes, rc)
	}
	return root, changes
}

// driftStagingDir is where Drift stages the root at rel: a directory of
// its own, named by a hash of rel, so that a root's never holds
// another's.
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
		if counts[c.Root] == nil {
			counts[c.Root] = map[string]int{}
		}
		counts[c.Root][c.Outcome]++
	}
	for _, r := range d.Roots {
		status := "no drift"
		switch {
		case r.Error != "":
			status = "not checked: " + strings.Join(strings.Fields(r.Error), " ")
		case r.Resources == 0:
			status = "not applied yet"
		case len(counts[r.Path]) > 0:
			var parts []string
			for _, o := range []struct{ outcome, label string }{
				{report.DriftWritten, "accepted into the code"},
				{report.DriftKept, "reverted by the next apply"},
				{report.DriftRejectedByPlan, "rejected by the plan"},
				{report.DriftManual, "need a hand edit"},
				{report.DriftSecret, "secret"},
				{report.DriftNotAssessable, "not assessable"},
				{report.DriftAbsorbed, "absorbed"},
			} {
				if n := counts[r.Path][o.outcome]; n > 0 {
					parts = append(parts, fmt.Sprintf("%d %s", n, o.label))
				}
			}
			status = strings.Join(parts, ", ")
		}
		if _, err := fmt.Fprintf(w, "%s: %s\n", r.Path, status); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "report: %s\n", filepath.Join(options.Out, report.Dir, report.DriftMarkdownFile))
	return err
}
