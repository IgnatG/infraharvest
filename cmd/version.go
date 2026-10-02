package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

const version = "v0.1.0-dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number of infraharvest",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("infraharvest " + version)
	},
}
