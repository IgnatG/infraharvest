// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	goversion "github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/envmerge"
	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// verifiedRoot is one root's gate results, as --format json prints them.
type verifiedRoot struct {
	Path string `json:"path"`
	// Environment is, for a root environments share, the environment it
	// was planned as.
	Environment string         `json:"environment,omitempty"`
	Checks      []report.Check `json:"checks"`
	// Findings are what its scanners found, each about the resource it
	// imports at the address the finding names, as report.md lists them.
	Findings []report.Finding `json:"findings,omitempty"`
	// Error is why the root couldn't be checked, if it couldn't: its
	// checks are then empty.
	Error string `json:"error,omitempty"`
}

// name is the root's path, with the environment it was planned as in
// brackets for a root environments share.
func (r verifiedRoot) name() string {
	if r.Environment != "" {
		return r.Path + " (" + r.Environment + ")"
	}
	return r.Path
}

func Verify(ctx context.Context, options VerifyOptions, w io.Writer) error {
	out, engineName, terraformPath, format, catalogueDir := options.Out, options.Engine, options.TerraformPath, options.Format, options.Catalogue
	if err := checkEngine(engineName); err != nil {
		return err
	}
	if format != FormatText && format != FormatJSON {
		return fmt.Errorf("--format must be %s or %s, not %q", FormatText, FormatJSON, format)
	}
	if options.EngineTimeout < 0 {
		return errors.New("--engine-timeout can't be negative")
	}
	if options.EngineParallelism < 0 {
		return errors.New("--engine-parallelism can't be negative")
	}
	// Left unset, as by a caller from before the flag, one at a time.
	if options.Parallel == 0 {
		options.Parallel = 1
	}
	if options.Parallel < 1 {
		return errors.New("--parallel must be at least 1")
	}
	policies, err := resolvePolicies(options.Policies)
	if err != nil {
		return err
	}
	roots, err := generatedRoots(out)
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		return fmt.Errorf("no generated roots in %s", out)
	}
	// Its inits and plans write into the roots, as an import does.
	lock, err := lockOutput(out, options.ForceUnlock)
	if err != nil {
		return err
	}
	if lock != nil {
		defer func() { _ = lock.Unlock() }()
	}
	binary := engineBinary(engineName)
	cacheDir, err := infraharvestCacheDir()
	if err != nil {
		return err
	}
	execPath, err := binary.Find(ctx, terraformPath, filepath.Join(cacheDir, binary.Name))
	if err != nil {
		return err
	}
	scanners := gate.DefaultScanners(policies)
	// The roots' checks share the providers' schemas they read.
	schemas := &tfrun.SchemaCache{}
	check := func(ctx context.Context, t verifyTarget) verifiedRoot {
		root := verifiedRoot{Path: t.path, Environment: t.env}
		if t.err != nil {
			root.Error = t.err.Error()
			return root
		}
		var opts engine.Options
		if provider := rootProvider(out, t.dir, options); provider != nil {
			cat, err := providerCatalogue(provider, catalogueDir)
			if err != nil {
				root.Error = err.Error()
				return root
			}
			opts = catalogueVerifyOptions(cat, lockedProviderVersion(t.dir, binary.Registry, provider))
		}
		opts.Scanners, opts.CommandTimeout = scanners, options.EngineTimeout
		// Each check runs in a private copy of the root (see engine.Verify).
		newTF := func(d string) (tfrun.Terraform, error) {
			return tfrun.New(d, execPath, filepath.Join(cacheDir, "plugins"), nil, schemas, tfrun.Parallelism(options.EngineParallelism))
		}
		checks, managed, err := verifyRoot(ctx, newTF, t, opts)
		if err != nil {
			root.Error = err.Error()
			return root
		}
		root.Findings = report.GateFindings(root.Path, root.Environment, managed, checks)
		for _, c := range checks {
			root.Checks = append(root.Checks, report.Check{Name: c.Name, Passed: c.Passed, Skipped: c.Skipped, Details: c.Details})
		}
		return root
	}
	results := verifyTargets(ctx, options.Parallel, verifyTargetsOf(out, roots, options.Environments), check)
	if err := writeVerified(w, results, format); err != nil {
		return err
	}
	return verifyExit(results)
}

// verifyRoot runs the gate on the root t names, and returns its checks
// with the resources it imports. A root environments share is planned as
// t's environment, with its var files.
func verifyRoot(ctx context.Context, newTF func(string) (tfrun.Terraform, error), t verifyTarget, opts engine.Options) (gate.Gate, []engine.Managed, error) {
	if t.env == "" {
		checks, err := engine.Verify(ctx, newTF, t.dir, opts)
		if err != nil {
			return nil, nil, err
		}
		managed, err := engine.ImportedAddresses(t.dir)
		return checks, managed, err
	}
	checks, err := envmerge.VerifyMerged(ctx, newTF, t.dir, t.env, envmerge.MergeOptions{Gate: engine.FullGate(opts), StateOnly: opts.StateOnly, CommandTimeout: opts.CommandTimeout})
	if err != nil {
		return nil, nil, err
	}
	ids, err := hclfile.MergedImportIDs(t.dir, t.env)
	if err != nil {
		return nil, nil, err
	}
	return checks, engine.MergedManaged(ids), nil
}

// verifyTarget is a root verify checks, as the environment env for a root
// environments share; err, if set, is why it can't be checked.
type verifyTarget struct {
	// dir is the root's directory, and path its path in the output
	// directory, as the results name it.
	dir, path, env string
	err            error
}

// verifyTargetsOf returns what verify checks of roots, the roots in out:
// each root, or, for a root environments share, each of its environments
// that environments names (every one when it names none).
func verifyTargetsOf(out string, roots, environments []string) []verifyTarget {
	var targets []verifyTarget
	for _, dir := range roots {
		path := relativePath(out, dir)
		envs, err := hclfile.MergedEnvironments(dir)
		if err != nil || len(envs) == 0 {
			targets = append(targets, verifyTarget{dir: dir, path: path, err: err})
			continue
		}
		for _, env := range envs {
			if len(environments) == 0 || slices.Contains(environments, env) {
				targets = append(targets, verifyTarget{dir: dir, path: path, env: env})
			}
		}
	}
	return targets
}

// verifyTargets checks each of targets with check, up to parallel at a
// time, as drift checks its roots (see checkDriftRoots), and returns their
// results in the targets' order. A root that can't be checked has why in
// its result, so the others go on; one that panics has the panic, and
// stops the others. A target that wasn't started has the context's error
// when the context ended, or else names the root that stopped the others.
func verifyTargets(ctx context.Context, parallel int, targets []verifyTarget, check func(context.Context, verifyTarget) verifiedRoot) []verifiedRoot {
	parallel = max(parallel, 1)
	results := make([]verifiedRoot, len(targets))
	names := make([]string, len(targets))
	for i, t := range targets {
		results[i] = verifiedRoot{Path: t.path, Environment: t.env}
		names[i] = results[i].name()
	}
	name := func(i int) string { return names[i] }
	started := make([]bool, len(targets))
	var mu sync.Mutex
	var panicked string
	err := eachCall(ctx, make(chan struct{}, parallel), len(targets), 0, parallel, name, func(ctx context.Context, i int) error {
		started[i] = true
		err := importSafely(names[i], func() error {
			results[i] = check(ctx, targets[i])
			return nil
		})
		if err != nil {
			results[i].Error = err.Error()
			mu.Lock()
			if panicked == "" {
				panicked = names[i]
			}
			mu.Unlock()
		}
		return err
	})
	if err == nil {
		return results
	}
	reason := "not started, as " + panicked + " panicked"
	if ctx.Err() != nil {
		reason = ctx.Err().Error()
	}
	for i := range targets {
		if !started[i] {
			results[i].Error = reason
		}
	}
	return results
}

// verifyExit returns the error verify exits with: report.ExitCouldNotRun
// when a root couldn't be checked, else report.ExitIncomplete when a
// check failed, naming each root and why.
func verifyExit(results []verifiedRoot) error {
	var couldNot, failed []string
	for _, r := range results {
		if r.Error != "" {
			couldNot = append(couldNot, r.name()+": "+r.Error)
			continue
		}
		for _, c := range r.Checks {
			if !c.Passed {
				failed = append(failed, fmt.Sprintf("%s: %s failed: %s", r.name(), c.Name, strings.Join(c.Details, "; ")))
			}
		}
	}
	if len(couldNot) > 0 {
		return &ExitError{Code: report.ExitCouldNotRun, Err: fmt.Errorf("%d roots couldn't be checked:\n%s", len(couldNot), strings.Join(append(couldNot, failed...), "\n"))}
	}
	if len(failed) > 0 {
		return &ExitError{Code: report.ExitIncomplete, Err: errors.New(strings.Join(failed, "\n"))}
	}
	return nil
}

func writeVerified(w io.Writer, results []verifiedRoot, format string) error {
	if format == FormatJSON {
		content, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s\n", content)
		return err
	}
	var findings []report.Finding
	for _, r := range results {
		findings = append(findings, r.Findings...)
		status := "passed"
		var failedNames, skippedNames []string
		for _, c := range r.Checks {
			switch {
			case !c.Passed:
				failedNames = append(failedNames, c.Name)
			case c.Skipped:
				skippedNames = append(skippedNames, c.Name)
			}
		}
		if len(failedNames) > 0 {
			status = "failed " + strings.Join(failedNames, ", ")
		}
		if len(skippedNames) > 0 {
			status += "; skipped " + strings.Join(skippedNames, ", ")
		}
		if r.Error != "" {
			status = "couldn't be checked: " + r.Error
		}
		if _, err := fmt.Fprintf(w, "%s: %s\n", r.name(), status); err != nil {
			return err
		}
	}
	if len(findings) == 0 {
		return nil
	}
	_, err := fmt.Fprintf(w, "\nFindings by resource:\n%s", report.ListFindings(findings))
	return err
}

// generatedRoots returns the directories under out that an import
// generated, sorted: those with a providers.tf, outside .terraform and
// the copies verify checks roots in (see gate.IsVerifyCopy), the
// generated modules, the bootstrap root and the checkpoints, where an
// incremental import stages what it adds (see stagingDir).
func generatedRoots(out string) ([]string, error) {
	var roots []string
	err := filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		topLevel := path != out && filepath.Dir(path) == filepath.Clean(out)
		if d.IsDir() && (d.Name() == ".terraform" || gate.IsVerifyCopy(d.Name()) || (topLevel && (d.Name() == hclfile.ModulesDirName || d.Name() == config.BootstrapDir || d.Name() == CheckpointDir))) {
			return filepath.SkipDir
		}
		// Every root configures its provider; modules don't.
		if !d.IsDir() && d.Name() == rootfile.ProvidersFileName {
			roots = append(roots, filepath.Dir(path))
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no output directory %s", out)
	}
	sort.Strings(roots)
	return roots, err
}

// VerifyOptions say what Verify checks and how.
type VerifyOptions struct {
	Out               string        // the output directory of the import
	Engine            string        // EngineTerraform or EngineTofu
	TerraformPath     string        // the binary, or "" to find or install one
	EngineTimeout     time.Duration // how long each engine command may run; 0 means no limit
	EngineParallelism int           // -parallelism of each plan and apply; 0 means Terraform's default
	Format            string        // FormatText or FormatJSON
	Catalogue         string        // directory of catalogue files the import used, if any
	Policies          []string      // directories of OPA policies conftest checks the roots against, if any
	ForceUnlock       bool          // verify even when another run holds the output directory's lock
	Parallel          int           // how many roots to check at once; at least 1
	// Provider returns a new provider of the given name, or nil when it
	// isn't compiled in: each root's catalogue comes from its provider.
	Provider func(name string) harvest.Provider
	// Environments are, for the roots environments share, the environments
	// to plan each as; none plans every environment it has.
	Environments []string
	// Names are the providers Provider knows, by which a root's provider
	// blocks find the provider it belongs to (see rootProvider).
	Names []string
}

// rootProvider returns the provider a root belongs to, or nil: the one
// whose Terraform provider the root's providers.tf configures, of those
// options.Names names, else the first directory of the root's path that
// names a provider, as {provider} does in the default layout and in that
// of roots per environment (envs/<env>/{provider}/...).
func rootProvider(out, dir string, options VerifyOptions) harvest.Provider {
	if options.Provider == nil {
		return nil
	}
	if configured := configuredProviders(dir); len(configured) > 0 {
		for _, name := range options.Names {
			p := options.Provider(name)
			if p != nil && slices.Contains(configured, catalogueName(p.Name())) {
				return p
			}
		}
	}
	for _, segment := range strings.Split(relativePath(out, dir), "/") {
		if p := options.Provider(segment); p != nil {
			return p
		}
	}
	return nil
}

// configuredProviders returns the catalogues (see catalogueName) of the
// Terraform providers the provider blocks of dir's providers.tf configure,
// such as google for google-beta; none if it doesn't parse.
func configuredProviders(dir string) []string {
	path := filepath.Join(dir, rootfile.ProvidersFileName)
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	f, diags := hclsyntax.ParseConfig(content, path, hcl.InitialPos)
	if diags.HasErrors() {
		return nil
	}
	body, ok := f.Body.(*hclsyntax.Body)
	if !ok {
		return nil
	}
	var names []string
	for _, b := range body.Blocks {
		if b.Type == "provider" && len(b.Labels) == 1 {
			names = append(names, catalogueName(b.Labels[0]))
		}
	}
	return names
}

// lockedProviderVersion returns the version of provider that the lock file
// of dir selected, through registry, or nil if it records none: the
// catalogue entries that depend on the provider's version then don't
// apply.
func lockedProviderVersion(dir, registry string, provider harvest.Provider) *goversion.Version {
	lock, err := os.ReadFile(filepath.Join(dir, tfrun.LockFileName))
	if err != nil {
		return nil
	}
	v, err := goversion.NewVersion(engine.LockedVersion(lock, qualifiedSource(registry, engineProvider(provider).Source)))
	if err != nil {
		return nil
	}
	return v
}
