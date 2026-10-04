// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/mcpserver"
)

// newMCPCmd serves infraharvest to AI agents over MCP on stdin and stdout.
func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve infraharvest to AI agents over the Model Context Protocol (stdio)",
		Long: "Serve infraharvest to AI agents over the Model Context Protocol, on stdin and\n" +
			"stdout. The tools run this binary: discover lists resources into a selection file,\n" +
			"import generates configuration from one after the user confirms it, and report\n" +
			"reads an import's report. Nothing is ever applied.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			self, err := os.Executable()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			return mcpserver.New(version, execRunner(self)).Run(ctx, &mcp.StdioTransport{})
		},
	}
}

// execRunner runs binary with the tool's arguments. Its standard output
// is the protocol's, so the tools never write to it directly.
func execRunner(binary string) mcpserver.Runner {
	return func(ctx context.Context, args []string) ([]byte, []byte, int, error) {
		var stdout, stderr bytes.Buffer
		c := exec.CommandContext(ctx, binary, args...)
		c.Stdout, c.Stderr = &stdout, &stderr
		err := c.Run()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
		}
		return stdout.Bytes(), stderr.Bytes(), 0, err
	}
}
