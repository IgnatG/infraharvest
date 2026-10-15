// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/importer"
)

// newImportCmd generates Terraform configuration for existing resources,
// with a subcommand per provider compiled in.
func newImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Generate Terraform configuration for existing cloud resources",
		Long: "List the existing resources of a cloud provider and generate Terraform\n" +
			"configuration for them, with import blocks, so that Terraform or OpenTofu can\n" +
			"take them over. Each provider is a subcommand, and infraharvest services\n" +
			"<provider> prints the services its --services takes. Choose what to import\n" +
			"with --all, or with a selection file infraharvest discover wrote. Nothing is\n" +
			"applied and no state is written.",
		SilenceUsage: true,
	}
	cmd.PersistentFlags().String("config", "", "configuration file, which sets flags not given on the command line and the state backend of the generated roots")
	for _, c := range providerCommands(importer.Options{}) {
		cmd.AddCommand(c)
	}
	return cmd
}

// providerCommands returns the command of every provider compiled in,
// sorted by name, each with options of its own and running in a run of
// the importer (see withEngineRun). import and discover differ only in
// the options they pass.
func providerCommands(options importer.Options) []*cobra.Command {
	names := providers.Names()
	commands := make([]*cobra.Command, 0, len(names))
	for _, name := range names {
		c := providers.Command(name, options)
		if c.RunE != nil {
			c.RunE = withEngineRun(c.RunE)
		}
		commands = append(commands, c)
	}
	return commands
}
