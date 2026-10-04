// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/config"
)

// newBootstrapCmd writes the root that creates the state backend the
// configuration file names.
func newBootstrapCmd() *cobra.Command {
	var configFile, out string
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Write a root that creates the state backend named in the configuration file",
		Long: "Write a Terraform root into <path-output>/bootstrap that creates the storage of\n" +
			"the state backend the configuration file names: an S3 bucket, an Azure storage\n" +
			"account and container, or a Cloud Storage bucket. It is versioned, encrypted, not\n" +
			"public, TLS-only and protected from destroy. Apply it once, with rights to create\n" +
			"storage, before planning the generated roots.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return writeBootstrap(configFile, out, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&configFile, "config", "", "configuration file with the backend (required)")
	cmd.Flags().StringVarP(&out, "path-output", "o", DefaultPathOutput, "output directory")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

func writeBootstrap(configFile, out string, w io.Writer) error {
	f, err := config.Load(configFile)
	if err != nil {
		return err
	}
	if f.Backend == nil {
		return errors.New(configFile + " has no backend to bootstrap")
	}
	files, err := f.Backend.Bootstrap()
	if err != nil {
		return err
	}
	dir := filepath.Join(out, config.BootstrapDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Its state stays local at first: keep it out of version control.
	if err := writeGitignore(out); err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), files[name], 0o644); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "Wrote %s: apply it once (terraform init, terraform apply), then plan the generated roots.\n", dir)
	return err
}
