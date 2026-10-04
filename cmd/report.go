// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/report"
)

// newReportCmd prints the report of the import in an output directory.
func newReportCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "report [output-directory]",
		Short: "Print the report of an import (default directory: generated)",
		Long: "Print the report an import wrote into its output directory (default:\n" +
			"generated): what was imported, left out and excluded, with the checks. --output\n" +
			"json prints it as JSON, as import --output json does.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := DefaultPathOutput
			if len(args) == 1 {
				out = args[0]
			}
			return printReport(out, output, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&output, "output", "O", outputHCL, "hcl prints the report as Markdown; json as JSON")
	return cmd
}

func printReport(out, format string, w io.Writer) error {
	if format != outputHCL && format != outputJSON {
		return fmt.Errorf("--output must be %s or %s, not %q", outputHCL, outputJSON, format)
	}
	r, err := report.Read(out)
	if err != nil {
		return fmt.Errorf("no report in %s: %w", out, err)
	}
	if format == outputJSON {
		return r.WriteJSON(w)
	}
	_, err = io.WriteString(w, r.Markdown())
	return err
}
