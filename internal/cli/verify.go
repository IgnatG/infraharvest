// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

func newVerifyCmd() *cobra.Command {
	options := importer.VerifyOptions{Provider: providers.New, Names: providers.Names()}
	cmd := &cobra.Command{
		Use:   "verify [output-directory]",
		Short: "Check generated roots again: format, validate, plan, standards, scanners, secrets, determinism",
		Long: "Run the verification gate again on every root an import generated (default\n" +
			"directory: " + report.DefaultOut + "), for example after editing them. Roots are\n" +
			"initialised without their backend, so no access to the state is needed; the\n" +
			"plan reads the cloud. Set secret variables first, with TF_VAR_ environment\n" +
			"variables or a .auto.tfvars file. A root environments share is planned once\n" +
			"per environment, with its var files (--environment names which). Exit code 0\n" +
			"means every check passed, 1 that one failed.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			options.Out = report.DefaultOut
			if len(args) == 1 {
				options.Out = args[0]
			}
			ctx, stop := interruptContext(cmd.Context())
			defer stop()
			return importer.Interrupted(ctx, importer.Verify(ctx, options, cmd.OutOrStdout()))
		},
	}
	providers.AddEngineFlags(cmd.Flags(), providers.EngineFlags{
		Engine: &options.Engine, TerraformPath: &options.TerraformPath, EngineTimeout: &options.EngineTimeout,
		Format: &options.Format, Catalogue: &options.Catalogue, ForceUnlock: &options.ForceUnlock, Environments: &options.Environments,
		FormatHelp:      "text prints a summary; json prints each root's checks as JSON",
		CatalogueHelp:   ": those the import used",
		EnvironmentHelp: "for a root environments share, the environments to plan it as, each with its var files, with the credentials verify runs with (default: every environment under its vars/)",
	})
	return cmd
}
