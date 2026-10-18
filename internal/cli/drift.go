// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/importer"
)

func newDriftCmd() *cobra.Command {
	options := importer.DriftOptions{Provider: providers.New, Names: providers.Names()}
	cmd := &cobra.Command{
		Use:   "drift [output-directory]",
		Short: "Find what changed in the cloud since the generated roots were applied",
		Long: "Check every root an import generated (default directory: " + importer.DefaultOut + ")\n" +
			"for what changed in the cloud since it was applied. Each root is initialised\n" +
			"with its backend and its state pulled; a private copy is then refreshed once,\n" +
			"and planned again with the code, to tell the changes the next apply would\n" +
			"revert from those the code absorbs. Nothing is written to the state or the\n" +
			"code, and plans don't lock the state, so read access to it is enough. S3, GCS\n" +
			"and azurerm backends and local state are supported; HCP Terraform and other\n" +
			"remote backends are refused. The report goes into report/drift.json and\n" +
			"report/drift.md. Exit code 0 means every root was checked, 2 that one\n" +
			"couldn't be; with --detailed-exitcode, 4 means drift was found.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			options.Out = importer.DefaultOut
			if len(args) == 1 {
				options.Out = args[0]
			}
			ctx, stop := importer.InterruptContext(cmd.Context())
			defer stop()
			return importer.Interrupted(ctx, importer.Drift(ctx, options, cmd.OutOrStdout()))
		},
	}
	cmd.Flags().StringVar(&options.Engine, "engine", importer.EngineTerraform, "terraform or tofu")
	cmd.Flags().StringVar(&options.TerraformPath, "terraform-path", "", "Terraform or OpenTofu binary (default: on PATH, else installed as for import)")
	cmd.Flags().DurationVar(&options.EngineTimeout, "engine-timeout", importer.DefaultEngineTimeout, "longest time one Terraform or OpenTofu command, such as the refresh of one root, may run (0: no limit)")
	cmd.Flags().StringVar(&options.Format, "format", importer.FormatText, "text prints a line per root; json prints drift.json")
	cmd.Flags().IntVar(&options.Parallel, "parallel", 1, "how many roots to check at once; each runs its own Terraform, so memory use grows with it")
	cmd.Flags().StringArrayVar(&options.Roots, "root", nil, "check only the roots whose path in the output directory, or a directory it is in, matches this pattern, such as aws/*/eu-west-2 (repeatable)")
	cmd.Flags().BoolVar(&options.DetailedExitCode, "detailed-exitcode", false, "exit with 4 when a change would be reverted by the next apply or can't be assessed, 0 when none")
	cmd.Flags().BoolVar(&options.ForceUnlock, "force-unlock", false, "check even when another run holds the output directory's lock, as when that run is stuck, or when the file system can't lock")
	return cmd
}
