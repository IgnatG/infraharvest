// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"io"
	"slices"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/importer"
)

// withEngineRun makes runE a provider command's RunE, in a run of the
// importer it is passed: the configuration file applies to the command's
// flags first, the run collects what every import the command makes did,
// and finishes with the report, the selection file and the exit code.
// With --check, the command only checks its flags (see checkRun), and the
// run is never done: it lists nothing, writes nothing, and catches no
// signals.
func withEngineRun(runE providers.RunFunc) func(*cobra.Command, []string) error {
	return func(c *cobra.Command, args []string) error {
		run := importer.NewRun()
		// Without a command, nothing catches signals.
		ctx := context.Background()
		if c != nil {
			if f := c.Flag("config"); f != nil && f.Value.String() != "" {
				if err := run.ApplyConfig(f.Value.String(), c.Flags(), c.Name()); err != nil {
					return err
				}
			}
			if err := run.CheckMergeEnvironments(c.Flags()); err != nil {
				return err
			}
			if f := c.Flag(providers.CheckFlag); f != nil && f.Value.String() == "true" {
				return runE(c, args, run)
			}
			if f := c.Flag("pick"); f != nil {
				run.OpenPicker = f.Value.String() == "true"
			}
			// Signals are caught once for the command, not per Import call.
			parent := c.Context()
			if parent == nil {
				parent = context.Background()
			}
			var stop context.CancelFunc
			ctx, stop = importer.InterruptContext(parent)
			defer stop()
			c.SetContext(ctx)
		}
		// An interrupted run exits with the signal's code, in the report too.
		return run.Do(func() error { return importer.Interrupted(ctx, runE(c, args, run)) })
	}
}

// checkRun checks the run of the provider command args name, such as
// import aws --services=vpc --all, as that command's --check does: its
// options are built from args and the configuration file as for the run,
// and checked as the run checks them, without listing anything. The MCP
// server checks an import here before the user is asked to confirm it.
func checkRun(ctx context.Context, args []string) error {
	cmd := NewCmdRoot()
	cmd.SetArgs(append(slices.Clone(args), "--"+providers.CheckFlag))
	// Nothing to print: the MCP server's standard output is the protocol's.
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.ExecuteContext(ctx)
}
