// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/infraharvest/infraharvest-cli/internal/version"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print which version of infraharvest this is",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Println("infraharvest " + version.Version)
	},
}
