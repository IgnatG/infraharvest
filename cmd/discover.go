// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"github.com/spf13/cobra"
)

// newDiscoverCmd lists resources into a selection file, which people review
// and edit before import --selection imports what it includes.
func newDiscoverCmd() *cobra.Command {
	options := ImportOptions{Discover: true}
	cmd := &cobra.Command{
		Use:   "discover",
		Short: "List resources into a selection file to review before importing",
		Long: "List resources into a selection file (--selection, default selection.yaml), each\n" +
			"marked included or not by the default rules, which leave out resources the cloud\n" +
			"manages itself. Review it, then import with --engine=terraform --selection <file>.",
		SilenceUsage: true,
	}
	for _, subcommand := range providerImporterSubcommands() {
		providerCommand := subcommand(options)
		providerCommand.Short = "List " + providerCommand.Name() + " resources into a selection file"
		providerCommand.Long = providerCommand.Short
		_ = providerCommand.MarkPersistentFlagRequired("resources")
		if providerCommand.RunE != nil {
			providerCommand.RunE = withEngineRun(providerCommand.RunE)
		}
		cmd.AddCommand(providerCommand)
	}
	return cmd
}
