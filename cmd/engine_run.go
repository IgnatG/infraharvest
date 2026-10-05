// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/config"
	"github.com/IgnatG/infraharvest/report"
	"github.com/IgnatG/infraharvest/selection"
)

// engineRun collects what the Import calls of one command do with
// --engine=terraform or tofu, so the command writes one report and ends
// with one exit code. The AWS command, for one, imports global,
// us-east-1-only and regional resources in separate calls.
type engineRun struct {
	options    ImportOptions
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
	// openPicker opens the picker on the selection file discover writes,
	// when run in a terminal (see pick).
	openPicker bool
	used       bool
}

// activeRun is the run of the provider command being executed, if any.
// A process runs one command at a time.
var activeRun *engineRun

func newEngineRun() *engineRun {
	return &engineRun{discovered: map[string]int{}, failed: map[string]int{}, skipped: map[string]int{}}
}

// withEngineRun makes a provider command's Import calls share one run,
// finished when the command is done.
func withEngineRun(runE func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(c *cobra.Command, args []string) error {
		run := newEngineRun()
		if err := run.applyConfig(c); err != nil {
			return err
		}
		if err := requireResources(c); err != nil {
			return err
		}
		defaultPathPattern(c)
		if c != nil {
			if f := c.Flag("pick"); f != nil {
				run.openPicker = f.Value.String() == "true"
			}
		}
		activeRun = run
		defer func() { activeRun = nil }()
		return run.finish(runE(c, args))
	}
}

// errNoResources says that a provider command needs --resources.
var errNoResources = errors.New("--resources is required (flag or config file)")

// requireResources checks that the command has resources to import, from
// --resources or the configuration file (see applyConfig). Cobra's required
// flags are checked before the file is read, so they can't.
func requireResources(c *cobra.Command) error {
	if c == nil || c.Flags().Lookup("resources") == nil {
		return nil
	}
	resources, err := c.Flags().GetStringSlice("resources")
	if err != nil {
		return err
	}
	if len(resources) == 0 {
		return errNoResources
	}
	return nil
}

// defaultPathPattern lays the roots of --engine=terraform or tofu out by
// account and region (DefaultRootPathPattern) when --path-pattern is not
// given, on the command line or in the configuration file. Given, it is
// followed as it is, even when it equals the legacy default.
func defaultPathPattern(c *cobra.Command) {
	if c == nil {
		return
	}
	pattern, engineFlag := c.Flags().Lookup("path-pattern"), c.Flags().Lookup("engine")
	if pattern == nil || pattern.Changed || engineFlag == nil {
		return
	}
	if v := engineFlag.Value.String(); v == engineTerraform || v == engineTofu {
		_ = pattern.Value.Set(DefaultRootPathPattern)
	}
}

// recordFailure records that part of the command couldn't run, such as one
// of the accounts --accounts names, so that the rest goes on and the
// command still ends with a report and the exit code for what failed.
func (r *engineRun) recordFailure(options ImportOptions, err error) {
	if !r.used {
		r.used = true
		r.options = options
	}
	r.failures = append(r.failures, err)
	r.report.Failures = append(r.report.Failures, err.Error())
}

// applyConfig loads the --config file, if any: it sets the command's flags
// the command line didn't, and the state backend of the generated roots.
func (r *engineRun) applyConfig(c *cobra.Command) error {
	if c == nil {
		return nil
	}
	flag := c.Flag("config")
	if flag == nil || flag.Value.String() == "" {
		return nil
	}
	f, err := config.Load(flag.Value.String())
	if err != nil {
		return err
	}
	if err := f.Apply(c.Flags(), c.Name()); err != nil {
		return fmt.Errorf("%s: %w", flag.Value.String(), err)
	}
	r.backend = f.Backend
	return nil
}

// finish writes the run's report, prints it for --output json, and returns
// err if the command couldn't run, or the error for what wasn't imported
// (see checkFailures). A run no engine import used returns err as is.
func (r *engineRun) finish(err error) error {
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
		if r.openPicker && interactive() {
			if err := pick(r.selectionPath()); err != nil {
				return err
			}
		}
		return checkFailures(r.failures, r.options.AllowPartial)
	}
	for typ, n := range r.skipped {
		r.report.Skipped = append(r.report.Skipped, report.Skipped{Type: typ, Count: n, Reason: "Terraform can't import this resource type"})
	}
	r.report.Finish(r.discovered, r.failed, r.options.AllowPartial)
	if writeErr := r.report.WriteFiles(r.options.PathOutput); writeErr != nil {
		return couldNotRun(err, writeErr)
	}
	log.Printf("imported %d of %d resources; report in %s", r.report.Totals.Imported, r.report.Totals.Discovered, filepath.Join(r.options.PathOutput, report.Dir, "report.md"))
	if r.options.Output == outputJSON {
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
