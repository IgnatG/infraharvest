// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/version"
)

// newVersionCmd returns the version command, a new one for each tree of
// commands: adding a command to a root sets its parent, and the MCP server
// builds roots at the same time (see CheckRun).
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print which version of infraharvest this is",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), "infraharvest "+version.Version)
		},
	}
}
