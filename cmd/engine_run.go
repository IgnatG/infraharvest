// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"errors"
	"log"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/report"
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
	used bool
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
		activeRun = run
		defer func() { activeRun = nil }()
		return run.finish(runE(c, args))
	}
}

// finish writes the run's report, prints it for --output json, and returns
// err if the command couldn't run, or the error for what wasn't imported
// (see checkFailures). A run no engine import used returns err as is.
func (r *engineRun) finish(err error) error {
	if !r.used {
		return err
	}
	for typ, n := range r.skipped {
		r.report.Skipped = append(r.report.Skipped, report.Skipped{Type: typ, Count: n, Reason: "Terraform can't import this resource type"})
	}
	r.report.Finish(r.discovered, r.failed, r.options.AllowPartial)
	if writeErr := r.report.WriteFiles(r.options.PathOutput); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	log.Printf("imported %d of %d resources; report in %s", r.report.Totals.Imported, r.report.Totals.Discovered, filepath.Join(r.options.PathOutput, report.Dir, "report.md"))
	if r.options.Output == outputJSON {
		if writeErr := r.report.WriteJSON(os.Stdout); writeErr != nil {
			return errors.Join(err, writeErr)
		}
	}
	if err != nil {
		return err
	}
	return checkFailures(r.failures, r.options.AllowPartial)
}
