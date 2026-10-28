// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/filelock"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/managed"
	"github.com/IgnatG/infraharvest/internal/ratelimit"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// Run collects what the Import calls of one command do, so the
// command writes one report and ends with one exit code. The AWS command,
// for one, imports global, us-east-1-only and regional resources in
// separate calls.
type Run struct {
	options    Options
	report     report.Report
	discovered map[string]int
	failed     map[string]int
	skipped    map[string]int
	failures   []error
	// lock is the first dependency lock file, shared by every directory
	// (see generateDir).
	lock []byte
	// selection is the selection file the import follows, if any.
	selection *selection.File
	// discoverState is what discover lists into the selection file.
	discoverState
	// backend is the state backend of the generated roots, if configured.
	backend *config.Backend
	// modules are the organisation's own modules, if configured.
	modules []config.Module
	// environments are the estate's environments, if configured (see
	// config.Environments).
	environments config.Environments
	// mergeState is what the run hands its merge phase.
	mergeState
	// shared is what the run and its children work out once.
	shared *shared
	// tools are what the run's Import calls, and its merge phase, reach
	// outside the process through; nil uses the real ones (see callTools).
	tools *callTools
	// scope is where the provider call being imported lists (see
	// discoveryScope); the report counts by it.
	scope string
	// here is what that call's listing found, for an incremental import
	// to tell what the roots have that is no longer in the cloud (see
	// listedHere); nil when it can't tell.
	here *listedHere
	used bool
}

// discoverState is what discover lists into the selection file it writes
// (see Run.writeSelection), apart from what the run counts for its
// report.
type discoverState struct {
	// listed are the resources discover lists into a selection file.
	listed []selection.Resource
	// covered are the services discover listed, by scope and service (see
	// coverKey), and filtered is whether it listed with --id, --tag or
	// --where: the selection file drops only the entries of what discover
	// listed in full (see covers).
	covered  map[string]bool
	filtered bool
	// toPick is the selection file discover wrote for the command to open
	// in the picker, if it wants to (see SelectionToPick).
	toPick string
}

// add adds what a child run listed (see Run.merge) to d.
func (d *discoverState) add(child *discoverState) {
	d.listed = append(d.listed, child.listed...)
	maps.Copy(d.covered, child.covered)
	d.filtered = d.filtered || child.filtered
}

// mergeState is what the run hands its merge phase (see
// Run.MergeEnvironments), apart from what it counts for its report.
type mergeState struct {
	// members are the roots the run generated of the configuration file's
	// environments, which the merge phase merges (see Run.MergeEnvironments).
	members []mergeMember
	// vacating are the handovers of the run's scopes that plan to vacate
	// merged roots, all of whose resources go to other roots now (see
	// handovers.vacating), which the merge phase vacates (see
	// vacateMergedRoots).
	vacating []*handovers
}

// add adds what a child run hands the merge phase (see Run.merge) to m.
func (m *mergeState) add(child *mergeState) {
	m.members = append(m.members, child.members...)
	m.vacating = append(m.vacating, child.vacating...)
}

// shared is what a run and its children (see Run.child) work out once for
// the whole command, whichever needs it first: the Import calls of every
// account and region read the same input.
type shared struct {
	mu sync.Mutex
	// managed is the state --managed-state names, by its sources as
	// resolved (see managedState).
	managed map[string]managed.Resources
	// stated are where the roots earlier imports generated have their
	// resources, as their own state, which --managed-state reads, says,
	// by root directory (see excludeManaged and previousAddressesOf).
	stated map[string][]engine.Managed

	// scopes are the accounts, subscriptions or projects the command
	// imports, where it names them (see Run.ImportEach); none where it imports
	// the one its credentials reach. Set before any scope starts.
	scopes []string

	// own are the adapters of the organisation's own modules, by the
	// provider they were fetched for (see ownAdapters).
	ownMu sync.Mutex
	own   map[string][]adapters.Adapter

	// outLock is the output directory's lock, which outLocked says the
	// command has taken, held until the run finishes (see Run.lockOut). It
	// is nil when --force-unlock went on without it.
	outMu     sync.Mutex
	outLock   *filelock.Lock
	outLocked bool

	// schemas are the providers' schemas the command's roots have read,
	// which every root of the same engine and providers shares (see
	// tfrun.SchemaCache).
	schemas tfrun.SchemaCache

	// calls bounds how many Import calls of the command list or generate
	// at once, over every account and region (see callSlots).
	callsMu sync.Mutex
	calls   chan struct{}

	// limiter limits the requests of the command's listings to each API
	// host, made with --rate-limit on first use (see rateLimiter).
	limiterMu sync.Mutex
	limiter   *ratelimit.Limiter

	// modules counts the roots of the command that call each generated
	// module, which they share (see engine.Options.Modules).
	modules engine.ModuleRefs

	// releases are the releases of the registry modules the command's
	// Import calls may call, each looked up once (see Run.moduleReleases).
	releases moduleReleases

	// scanners are the slots the scanners of the command's roots run in,
	// which they share (see engine.Options.ScannerSlots).
	scanners gate.ScannerSlots
}

// rateLimiter returns the command's limiter, of perSecond requests a
// second, made on first use: every Import call of the command, in every
// account and region, gives its listing the same one (see listCall), and
// no other run in the process changes its rate.
func (s *shared) rateLimiter(perSecond float64) *ratelimit.Limiter {
	s.limiterMu.Lock()
	defer s.limiterMu.Unlock()
	if s.limiter == nil {
		s.limiter = ratelimit.New(perSecond)
	}
	return s.limiter
}

// WithLimiter returns ctx carrying the limiter of the command's listings,
// of perSecond requests a second, which every Import call of the command
// gets too (see shared.rateLimiter): the calls the command makes before
// its Import calls, such as those finding the accounts or the stacks it
// imports, are then of the run, and share with the Import calls what the
// providers keep for the run, such as the credentials they resolved, so
// that a profile whose role needs MFA asks for the code once.
func (r *Run) WithLimiter(ctx context.Context, perSecond float64) context.Context {
	return ratelimit.NewContext(ctx, r.shared.rateLimiter(perSecond))
}

// callSlots returns the slots the Import calls of the command take while
// they list or generate, n of them, made on first use: the regions of
// accounts imported at once share --parallel with the accounts, so no more
// than n calls run at once in all. A call that holds a slot never waits for
// another.
func (s *shared) callSlots(n int) chan struct{} {
	s.callsMu.Lock()
	defer s.callsMu.Unlock()
	if s.calls == nil {
		s.calls = make(chan struct{}, max(n, 1))
	}
	return s.calls
}

// ownAdapters returns the adapters of the organisation's modules for
// provider (see fetchAdapters), fetching them with the engine at
// execPath, into the cache under cacheDir, on first use only: every Import
// call of the command, in every account, uses the same modules.
func (s *shared) ownAdapters(ctx context.Context, execPath, cacheDir, provider string, modules []config.Module) []adapters.Adapter {
	s.ownMu.Lock()
	defer s.ownMu.Unlock()
	if own, ok := s.own[provider]; ok {
		return own
	}
	if s.own == nil {
		s.own = map[string][]adapters.Adapter{}
	}
	s.own[provider] = fetchAdapters(ctx, execPath, cacheDir, provider, modules)
	return s.own[provider]
}

// managedState returns the managed resources of the state in sources,
// resolved (see excludeManaged), loading them on first use only.
func (s *shared) managedState(ctx context.Context, sources []string, stores managed.Stores) (managed.Resources, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.Join(sources, "\n")
	if state, ok := s.managed[key]; ok {
		return state, nil
	}
	state, err := managed.Load(ctx, sources, stores)
	if err != nil {
		return nil, err
	}
	if s.managed == nil {
		s.managed = map[string]managed.Resources{}
	}
	s.managed[key] = state
	return state, nil
}

func NewRun() *Run {
	return &Run{discovered: map[string]int{}, failed: map[string]int{}, skipped: map[string]int{}, discoverState: discoverState{covered: map[string]bool{}}, shared: &shared{}}
}

// recordFailure records that part of the command couldn't run, such as one
// of the accounts --accounts names, so that the rest goes on and the
// command still ends with a report and the exit code for what failed.
func (r *Run) recordFailure(options Options, err error) {
	if !r.used {
		r.used = true
		r.options = options
	}
	r.failures = append(r.failures, err)
	r.report.AddFailure(err)
}

// finish writes the run's report, prints it for --format json, and returns
// err if the command couldn't run, or the error for what wasn't imported
// (see checkFailures). A run no import used returns err as is, after
// writing a report that records err if the import that failed had an
// output directory, so that an earlier run's report isn't taken for this
// one's. It writes only holding the output directory's lock, and
// releases it (see lockOut): a run that couldn't take it writes nothing
// into another run's output.
func (r *Run) finish(err error) error {
	defer r.unlockOut()
	if !r.used && (err == nil || r.options.Out == "" || r.options.Discover) {
		return err
	}
	if lockErr := r.lockOut(r.options); lockErr != nil {
		return couldNotRun(err, lockErr)
	}
	if r.options.Discover {
		// After a failure, what didn't list may be only what failed.
		if writeErr := r.writeSelection(err != nil || len(r.failures) > 0); writeErr != nil {
			return couldNotRun(err, writeErr)
		}
		if err != nil {
			return err
		}
		if r.options.Format == FormatJSON {
			// The summary goes to stdout, where the picker would draw.
			if writeErr := r.summary().WriteJSON(r.stdout()); writeErr != nil {
				return couldNotRun(nil, writeErr)
			}
		} else {
			r.toPick = r.selectionPath()
		}
		return checkFailures(r.failures, r.options.AllowPartial)
	}
	for typ, n := range r.skipped {
		r.report.AddSkipped(typ, n)
	}
	if err != nil {
		// What ended the run, such as an interrupt, is a failure too.
		r.report.AddFailure(err)
	}
	r.report.Finish(r.discovered, r.failed, r.options.AllowPartial)
	if err != nil {
		// The report says what the process exits with.
		r.report.SetExitCode(ExitCode(err))
	}
	if writeErr := r.report.WriteFiles(r.options.Out); writeErr != nil {
		return couldNotRun(err, writeErr)
	}
	log.Printf("imported %d of %d resources; report in %s", r.report.Totals.Imported, r.report.Totals.Discovered, filepath.Join(r.options.Out, report.Dir, "report.md"))
	r.report.CloudFormationStacks.Log()
	if r.options.Format == FormatJSON {
		if writeErr := r.report.WriteJSON(r.stdout()); writeErr != nil {
			return couldNotRun(err, writeErr)
		}
	}
	if err != nil {
		return err
	}
	return checkFailures(r.failures, r.options.AllowPartial)
}

// stdout is where --format json prints: the command's output, os.Stdout if
// none is set.
func (r *Run) stdout() io.Writer {
	if r.options.Stdout != nil {
		return r.options.Stdout
	}
	return os.Stdout
}

// couldNotRun returns the error for a run whose report or selection file
// couldn't be written: it exits with report.ExitCouldNotRun, whatever the
// import's own err says.
func couldNotRun(err, writeErr error) error {
	return &ExitError{Code: report.ExitCouldNotRun, Err: errors.Join(err, writeErr)}
}

// child returns a run for one of several accounts or projects of r's
// command, imported alongside the others (see Run.ImportEach): it shares r's
// selection file, state backend, lock file, modules, environments and
// tools, and keeps everything else to itself until r merges it.
func (r *Run) child() *Run {
	c := NewRun()
	c.selection, c.backend, c.lock, c.modules, c.shared, c.tools = r.selection, r.backend, r.lock, r.modules, r.shared, r.tools
	c.environments = r.environments
	return c
}

// merge adds what child (see child) did to r.
func (r *Run) merge(child *Run) {
	if !child.used {
		return
	}
	if !r.used {
		r.used = true
		r.options = child.options
	}
	r.report.Merge(&child.report)
	for typ, n := range child.discovered {
		r.discovered[typ] += n
	}
	for typ, n := range child.failed {
		r.failed[typ] += n
	}
	for typ, n := range child.skipped {
		r.skipped[typ] += n
	}
	r.failures = append(r.failures, child.failures...)
	r.mergeState.add(&child.mergeState)
	r.discoverState.add(&child.discoverState)
	if r.lock == nil {
		r.lock = child.lock
	}
}

// ApplyConfig applies the configuration file at path to the command's
// flags, and keeps its backend, modules and environments for the run.
// profiles are those of the providers the binary is built with (see
// config.Load).
func (r *Run) ApplyConfig(path string, flags *pflag.FlagSet, command string, profiles []harvest.Profile) error {
	f, err := config.Load(path, profiles)
	if err != nil {
		return err
	}
	if err := f.Apply(flags, command); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := checkEnvironmentFlags(f, flags); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	r.backend = f.Backend
	r.modules = f.Modules
	r.environments = f.Environments
	return nil
}

// Do runs fn, whose Import calls are into this run (see ImportCalls,
// Run.ImportEach and Run.MergeEnvironments), which then finishes as a
// provider command does, with the report or the selection file written
// and the exit code in the error.
func (r *Run) Do(fn func() error) error {
	return r.finish(fn())
}

// SelectionToPick returns the selection file a discover run that Do
// finished wrote, for the command to open in the picker, or "" when there
// is none to open: the run wasn't discover, the listing failed, or
// --format json printed the summary where the picker would draw. It is
// set even when what failed in part makes Do return an error, so that
// what did list can still be picked.
func (r *Run) SelectionToPick() string {
	return r.toPick
}

// CheckMergeEnvironments rejects --merge-environments, or its setting in
// the configuration file, set to true for a run whose configuration file
// names no environments to merge (see config.CheckMergeEnvironments).
func (r *Run) CheckMergeEnvironments(flags *pflag.FlagSet) error {
	flag := flags.Lookup("merge-environments")
	if flag == nil {
		return nil
	}
	return config.CheckMergeEnvironments(r.environments, flag.Value.String() == "true", flag.Changed)
}
