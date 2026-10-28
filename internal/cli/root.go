// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package cli is infraharvest's command line: the root command and its
// subcommands.
package cli

import (
	"log"
	"os"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/logging"
	"github.com/IgnatG/infraharvest/internal/version"
)

// awsCommands make the root's commands only a build with AWS has, such
// as cfn: each file that makes one carries the build constraint
// `!minimal || aws`, as the AWS provider command does (see package
// providers), and adds its constructor from init(), so that a build
// without AWS has no such command.
var awsCommands []func() *cobra.Command

// NewCmdRoot returns a new tree of infraharvest's commands.
func NewCmdRoot() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "infraharvest",
		Short: "Import existing infrastructure into Terraform code",
		Long: "infraharvest lists the resources that already exist in a cloud account and\n" +
			"generates Terraform code for them: roots with import blocks, ready for\n" +
			"Terraform or OpenTofu to plan and take over. Start with discover to review\n" +
			"what would be imported, or import directly.",
		Version: version.Version,
		// main logs the error, and the exit code says what kind it is, so
		// cobra prints neither the error nor the usage.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(
		newImportCmd(),
		newDiscoverCmd(),
		newPickCmd(),
		newMCPCmd(),
		newServicesCmd(),
		newVerifyCmd(),
		newDriftCmd(),
		newReportCmd(),
		newBootstrapCmd(),
		newVersionCmd(),
	)
	for _, newCmd := range awsCommands {
		cmd.AddCommand(newCmd())
	}
	return cmd
}

// Execute runs the command the process's arguments name, profiled as the
// environment asks (see startProfiles).
func Execute() error {
	stop, err := startProfiles(os.Getenv)
	if err != nil {
		return err
	}
	// Names and values read from a cloud reach the terminal in what the
	// commands print: escape sequences in them are shown, not sent (see
	// logging.Sanitize).
	root := NewCmdRoot()
	root.SetOut(logging.Sanitize(os.Stdout))
	root.SetErr(logging.Sanitize(os.Stderr))
	err = root.Execute()
	if stopErr := stop(); stopErr != nil {
		if err == nil {
			return stopErr
		}
		// The command's error matters more; the profile's is only logged.
		log.Printf("writing the profiles: %v", stopErr)
	}
	return err
}
