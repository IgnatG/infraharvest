// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// infraharvest imports existing cloud infrastructure into Terraform code.
// The command line lives in internal/cli; main only sets up logging and
// turns the outcome into the process's exit code.
package main

import (
	"log"
	"os"

	"github.com/infraharvest/infraharvest-cli/internal/cli"
	"github.com/infraharvest/infraharvest-cli/internal/importer"
	"github.com/infraharvest/infraharvest-cli/internal/logging"
)

func main() {
	// Logs go to stderr: stdout carries only results, such as the list of
	// services or the JSON report, and the MCP server reads it as data.
	log.SetOutput(logging.Filter(os.Stderr))
	if err := cli.Execute(); err != nil {
		log.Print(err)
		os.Exit(importer.ExitCode(err))
	}
}
