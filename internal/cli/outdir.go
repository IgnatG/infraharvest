// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/report"
)

// addOutFlag adds --out (-o) to cmd, a command that reads an import's
// output directory, as import and discover write it with --out. The
// directory can also be given as the command's argument, as before
// --out existed; outDir reads either.
func addOutFlag(cmd *cobra.Command, out *string) {
	cmd.Flags().StringVarP(out, "out", "o", report.DefaultOut, "output directory of the import, as import's --out; the same as the argument")
}

// outDir returns the output directory cmd reads: its argument, or --out
// (flag, whose default is report.DefaultOut). Both, naming different
// directories, is an error.
func outDir(cmd *cobra.Command, args []string, flag string) (string, error) {
	if len(args) == 0 {
		return flag, nil
	}
	if cmd.Flags().Changed("out") && flag != args[0] {
		return "", fmt.Errorf("the output directory is %q as the argument and %q as --out: give one", args[0], flag)
	}
	return args[0], nil
}
