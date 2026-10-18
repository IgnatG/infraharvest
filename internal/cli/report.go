// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"github.com/spf13/cobra"

	"github.com/infraharvest/infraharvest-cli/internal/importer"
)

func newReportCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "report [output-directory]",
		Short: "Print the report of an import (default directory: " + importer.DefaultOut + ")",
		Long: "Print the report an import wrote into its output directory (default:\n" +
			importer.DefaultOut + "): what was imported, left out and excluded, with the\n" +
			"checks. --format json prints it as JSON, as import --format json does.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := importer.DefaultOut
			if len(args) == 1 {
				out = args[0]
			}
			return importer.PrintReport(out, format, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&format, "format", importer.FormatText, "text prints the report as Markdown; json as JSON")
	return cmd
}
