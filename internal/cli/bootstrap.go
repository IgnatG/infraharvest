// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/importer"
)

func newBootstrapCmd() *cobra.Command {
	var configFile, out string
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Write a root that creates the state backend named in the configuration file",
		Long: "Write a Terraform root into <out>/bootstrap that creates the storage of\n" +
			"the state backend the configuration file names: an S3 bucket, an Azure storage\n" +
			"account and container, or a Cloud Storage bucket. It is versioned, encrypted, not\n" +
			"public, TLS-only and protected from destroy. Apply it once, with rights to create\n" +
			"storage, before planning the generated roots.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return importer.WriteBootstrap(configFile, out, cmd.OutOrStdout(), providers.Profiles())
		},
	}
	cmd.Flags().StringVar(&configFile, "config", "", "configuration file with the backend (required)")
	cmd.Flags().StringVarP(&out, "out", "o", importer.DefaultOut, "directory to write the bootstrap root under")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}
