// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/importer"
)

func newVerifyCmd() *cobra.Command {
	options := importer.VerifyOptions{Provider: providers.New}
	cmd := &cobra.Command{
		Use:   "verify [output-directory]",
		Short: "Check generated roots again: format, validate, plan, standards, scanners, secrets, determinism",
		Long: "Run the verification gate again on every root an import generated (default\n" +
			"directory: " + importer.DefaultOut + "), for example after editing them. Roots are\n" +
			"initialised without their backend, so no access to the state is needed; the\n" +
			"plan reads the cloud. Set secret variables first, with TF_VAR_ environment\n" +
			"variables or a .auto.tfvars file. Exit code 0 means every check passed, 1 that\n" +
			"one failed.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			options.Out = importer.DefaultOut
			if len(args) == 1 {
				options.Out = args[0]
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			return importer.Verify(ctx, options, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&options.Engine, "engine", importer.EngineTerraform, "terraform or tofu")
	cmd.Flags().StringVar(&options.TerraformPath, "terraform-path", "", "Terraform or OpenTofu binary (default: on PATH, else installed as for import)")
	cmd.Flags().StringVar(&options.Format, "format", importer.FormatText, "text prints a summary; json prints each root's checks as JSON")
	cmd.Flags().StringVar(&options.Catalogue, "catalogue", "", "directory of catalogue files the import used to add to or override the built-in catalogue (see "+providers.DocsURL+"catalogue/)")
	return cmd
}
