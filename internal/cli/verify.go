// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// verifySettings are the settings of the import's configuration file
// that verify applies: how it runs the engine, the catalogue and the
// policies the import used. It leaves the import's others to the import.
var verifySettings = []string{"engine", "terraform-path", "engine-timeout", "catalogue", "policy"}

func newVerifyCmd() *cobra.Command {
	options := importer.VerifyOptions{Provider: providers.New, Names: providers.Names()}
	var configFile string
	cmd := &cobra.Command{
		Use:   "verify [output-directory]",
		Short: "Check generated roots again: format, validate, plan, standards, scanners, secrets, determinism",
		Long: "Run the verification gate again on every root an import generated (default\n" +
			"directory: " + report.DefaultOut + "), for example after editing them. Roots are\n" +
			"initialised without their backend, so no access to the state is needed; the\n" +
			"plan reads the cloud. Set secret variables first, with TF_VAR_ environment\n" +
			"variables or a .auto.tfvars file. A root environments share is planned once\n" +
			"per environment, with its var files (--environment names which). A root that\n" +
			"can't be checked is reported as such, and the others are still checked.\n" +
			"--config takes the import's configuration file: its settings verify has,\n" +
			"engine, terraform-path, engine-timeout, catalogue and policy, apply where the\n" +
			"command line doesn't give them.\n" +
			"Exit code 0 means every check passed, 1 that one failed, 2 that a root\n" +
			"couldn't be checked.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if configFile != "" {
				f, err := config.Load(configFile, providers.Profiles())
				if err != nil {
					return err
				}
				if err := f.ApplyShared(cmd.Flags(), cmd.Name(), verifySettings); err != nil {
					return fmt.Errorf("%s: %w", configFile, err)
				}
			}
			var err error
			if options.Out, err = outDir(cmd, args, options.Out); err != nil {
				return err
			}
			ctx, stop := interruptContext(cmd.Context())
			defer stop()
			return importer.Interrupted(ctx, importer.Verify(ctx, options, cmd.OutOrStdout()))
		},
	}
	addOutFlag(cmd, &options.Out)
	cmd.Flags().StringVar(&configFile, "config", "", "the import's configuration file, whose settings verify has, engine, terraform-path, engine-timeout, catalogue and policy, apply where not given on the command line")
	providers.AddEngineFlags(cmd.Flags(), providers.EngineFlags{
		Engine: &options.Engine, TerraformPath: &options.TerraformPath, EngineTimeout: &options.EngineTimeout,
		Format: &options.Format, Catalogue: &options.Catalogue, ForceUnlock: &options.ForceUnlock, Environments: &options.Environments, Policies: &options.Policies,
		FormatHelp:      "text prints a summary; json prints each root's checks as JSON",
		CatalogueHelp:   ": those the import used",
		EnvironmentHelp: "for a root environments share, the environments to plan it as, each with its var files, with the credentials verify runs with (default: every environment under its vars/)",
	})
	cmd.Flags().IntVar(&options.Parallel, "parallel", 1, "how many roots to check at once; each runs its own Terraform, so memory use grows with it")
	return cmd
}
