// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/internal/config"
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
	// listed are the resources discover lists into a selection file.
	listed []selection.Resource
	// backend is the state backend of the generated roots, if configured.
	backend *config.Backend
	// modules are the organisation's own modules, if configured.
	modules []config.Module
	// OpenPicker opens the picker on the selection file discover writes,
	// when run in a terminal (see pick).
	OpenPicker bool
	// scope is where the provider call being imported lists (see
	// discoveryScope); the report counts by it.
	scope string
	used  bool
}

// activeRun is the run of the provider command being executed, if any.
// A process runs one command at a time.
var activeRun *Run

func NewRun() *Run {
	return &Run{discovered: map[string]int{}, failed: map[string]int{}, skipped: map[string]int{}}
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
	r.report.Failures = append(r.report.Failures, err.Error())
}

// finish writes the run's report, prints it for --format json, and returns
// err if the command couldn't run, or the error for what wasn't imported
// (see checkFailures). A run no import used returns err as is.
func (r *Run) finish(err error) error {
	if !r.used {
		return err
	}
	if r.options.Discover {
		if writeErr := r.writeSelection(); writeErr != nil {
			return couldNotRun(err, writeErr)
		}
		if err != nil {
			return err
		}
		if r.OpenPicker && Interactive() {
			if err := Pick(r.selectionPath()); err != nil {
				return err
			}
		}
		return checkFailures(r.failures, r.options.AllowPartial)
	}
	for typ, n := range r.skipped {
		r.report.Skipped = append(r.report.Skipped, report.Skipped{Type: typ, Count: n, Reason: "Terraform can't import this resource type"})
	}
	r.report.Finish(r.discovered, r.failed, r.options.AllowPartial)
	if writeErr := r.report.WriteFiles(r.options.Out); writeErr != nil {
		return couldNotRun(err, writeErr)
	}
	log.Printf("imported %d of %d resources; report in %s", r.report.Totals.Imported, r.report.Totals.Discovered, filepath.Join(r.options.Out, report.Dir, "report.md"))
	if r.options.Format == FormatJSON {
		if writeErr := r.report.WriteJSON(os.Stdout); writeErr != nil {
			return couldNotRun(err, writeErr)
		}
	}
	if err != nil {
		return err
	}
	return checkFailures(r.failures, r.options.AllowPartial)
}

// couldNotRun returns the error for a run whose report or selection file
// couldn't be written: it exits with report.ExitCouldNotRun, whatever the
// import's own err says.
func couldNotRun(err, writeErr error) error {
	return &ExitError{Code: report.ExitCouldNotRun, Err: errors.Join(err, writeErr)}
}

// child returns a run for one of several accounts or projects of r's
// command, imported alongside the others (see ImportEach): it shares r's
// selection file, state backend, lock file and modules, and keeps
// everything else to itself until r merges it.
func (r *Run) child() *Run {
	c := NewRun()
	c.selection, c.backend, c.lock, c.modules = r.selection, r.backend, r.lock, r.modules
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
	r.listed = append(r.listed, child.listed...)
	if r.lock == nil {
		r.lock = child.lock
	}
}

// ApplyConfig applies the configuration file at path to the command's
// flags, and keeps its backend and modules for the run.
func (r *Run) ApplyConfig(path string, flags *pflag.FlagSet, command string) error {
	f, err := config.Load(path)
	if err != nil {
		return err
	}
	if err := f.Apply(flags, command); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	r.backend = f.Backend
	r.modules = f.Modules
	return nil
}

// Do runs fn as this run: the Import calls fn makes add to the run, which
// then finishes as a provider command does, with the report or the
// selection file written and the exit code in the error.
func (r *Run) Do(fn func() error) error {
	activeRun = r
	defer func() { activeRun = nil }()
	return r.finish(fn())
}
