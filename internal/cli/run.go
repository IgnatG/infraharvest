// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/importer"
)

// withEngineRun wraps a provider command's RunE in a run of the importer:
// the configuration file applies to the command's flags first, the run
// collects what every import the command makes did, and finishes with the
// report, the selection file and the exit code.
func withEngineRun(runE func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(c *cobra.Command, args []string) error {
		run := importer.NewRun()
		if c != nil {
			if f := c.Flag("config"); f != nil && f.Value.String() != "" {
				if err := run.ApplyConfig(f.Value.String(), c.Flags(), c.Name()); err != nil {
					return err
				}
			}
			if err := requireServices(c); err != nil {
				return err
			}
			if f := c.Flag("pick"); f != nil {
				run.OpenPicker = f.Value.String() == "true"
			}
			// Signals are caught once for the command, not per Import call.
			parent := c.Context()
			if parent == nil {
				parent = context.Background()
			}
			ctx, stop := importer.InterruptContext(parent)
			defer stop()
			c.SetContext(ctx)
		}
		return run.Do(func() error { return runE(c, args) })
	}
}

var errNoServices = errors.New("--services is required (flag or config file)")

// requireServices fails a provider command run without --services, after
// the configuration file has had its chance to set it.
func requireServices(c *cobra.Command) error {
	if c.Flags().Lookup("services") == nil {
		return nil
	}
	services, err := c.Flags().GetStringSlice("services")
	if err != nil {
		return err
	}
	if len(services) == 0 {
		return errNoServices
	}
	return nil
}
