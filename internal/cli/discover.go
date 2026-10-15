// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/importer"
)

// newDiscoverCmd lists resources into a selection file, which people review
// and edit before import --selection imports what it includes.
func newDiscoverCmd() *cobra.Command {
	options := importer.Options{Discover: true}
	cmd := &cobra.Command{
		Use:   "discover",
		Short: "List resources into a selection file to review before importing",
		Long: "List resources into a selection file (--selection, default selection.yaml), each\n" +
			"marked included or not by the default rules, which leave out resources the cloud\n" +
			"manages itself. Review it, then import with --selection <file>.",
		SilenceUsage: true,
	}
	cmd.PersistentFlags().String("config", "", "configuration file, which sets flags not given on the command line")
	cmd.PersistentFlags().Bool("pick", true, "open the picker on the selection file once it is written, when run in a terminal (see infraharvest pick)")
	for _, providerCommand := range providerCommands(options) {
		providerCommand.Short = "List " + providerCommand.Name() + " resources into a selection file"
		providerCommand.Long = providerCommand.Short
		cmd.AddCommand(providerCommand)
	}
	return cmd
}
