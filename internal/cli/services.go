// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/importer"
)

// newServicesCmd prints the services of a provider, as --services names
// them.
func newServicesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "services <provider>",
		Short: "List the services of a provider that --services takes",
		Long: "Print the services of a provider that import and discover take in --services,\n" +
			"one per line, on standard output. --services=* imports all of them. A few\n" +
			"services list again what others already cover, or a cloud service that has\n" +
			"reached end of support; they aren't printed, and are imported only when\n" +
			"--services names them.",
		Args:         cobra.ExactArgs(1),
		ValidArgs:    providers.Names(),
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			provider := providers.New(args[0])
			if provider == nil {
				return fmt.Errorf("unknown provider %q: this infraharvest has %s", args[0], strings.Join(providers.Names(), ", "))
			}
			for _, service := range importer.Services(provider) {
				if _, err := fmt.Fprintln(c.OutOrStdout(), service); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
