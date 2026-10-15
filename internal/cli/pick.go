// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/importer"
)

func newPickCmd() *cobra.Command {
	path := importer.DefaultSelectionFile
	cmd := &cobra.Command{
		Use:   "pick",
		Short: "Choose in the terminal what a selection file imports",
		Long: "Open a selection file from infraharvest discover in the terminal: a tree of its\n" +
			"resources by account, region and type, to include or exclude one at a time or a\n" +
			"group at once, with a summary of what an import of it would bring under\n" +
			"Terraform. s saves the file; q leaves it as it was.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(*cobra.Command, []string) error {
			return importer.Pick(path)
		},
	}
	cmd.Flags().StringVar(&path, "selection", importer.DefaultSelectionFile, "selection file to edit, from infraharvest discover")
	return cmd
}
