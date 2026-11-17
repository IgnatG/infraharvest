// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// infraharvest imports existing cloud infrastructure into Terraform code.
// The command line lives in internal/cli; main only sets up logging and
// turns the outcome into the process's exit code.
package main

import (
	"os"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/logging"
)

func main() {
	// Terraform checks for a newer release, sending HashiCorp its version
	// and platform, each time it starts, unless CHECKPOINT_DISABLE is set.
	// infraharvest sends nothing anywhere a run doesn't need (see
	// docs/privacy.md), so every Terraform it runs inherits the variable,
	// unless it is set already.
	if _, ok := os.LookupEnv("CHECKPOINT_DISABLE"); !ok {
		_ = os.Setenv("CHECKPOINT_DISABLE", "1")
	}
	// Logs go to stderr: stdout carries only results, such as the list of
	// services or the JSON report, and the MCP server reads it as data.
	logging.Setup(os.Stderr)
	if err := cli.Execute(); err != nil {
		logging.Errorf("%v", err)
		os.Exit(importer.ExitCode(err))
	}
}
