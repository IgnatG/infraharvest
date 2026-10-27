// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/mcpserver"
	"github.com/IgnatG/infraharvest/internal/version"
)

// newMCPCmd serves infraharvest to AI agents over MCP on stdin and stdout.
func newMCPCmd() *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve infraharvest to AI agents over the Model Context Protocol (stdio)",
		Long: "Serve infraharvest to AI agents over the Model Context Protocol, on stdin and\n" +
			"stdout. The tools run this binary: discover lists resources into a selection file,\n" +
			"import generates configuration from one after the user confirms it, report\n" +
			"reads an import's report or the drift report, drift reports what changed in\n" +
			"the cloud since the roots were applied, and drift_accept takes such changes\n" +
			"into the code after the user confirms it. The files they read and write stay under --root.\n" +
			"Nothing is ever applied, and the state is never changed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			self, err := os.Executable()
			if err != nil {
				return err
			}
			if root == "" {
				if root, err = os.Getwd(); err != nil {
					return err
				}
				// Some hosts start servers in / or the home directory.
				home, _ := os.UserHomeDir()
				if err := mcpserver.CheckDefaultRoot(root, home); err != nil {
					return err
				}
			}
			// Standard output is the protocol's.
			fmt.Fprintf(cmd.ErrOrStderr(), "infraharvest mcp: the tools read and write files under %s\n", root)
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return mcpserver.New(version.Version, root, execRunner(self), checkRun).Run(ctx, &mcp.StdioTransport{})
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "directory the tools may read and write files in: selection files, output directories, reports and drift decisions files (default: the current directory, unless that is the root of a filesystem or the home directory)")
	return cmd
}

// childEnv returns env, the server's environment, without the variables
// that profile a run (see importer.StartProfiles): a tool's child would
// write its profiles over the server's, at the same paths, and over those
// of the other children running at the same time.
func childEnv(env []string) []string {
	kept := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(name) {
		case importer.CPUProfileEnv, importer.MemProfileEnv, importer.GoroutineLeakProfileEnv:
			continue
		}
		kept = append(kept, kv)
	}
	return kept
}

// childWaitDelay is how long a tool's child process has to stop once
// interrupted before it is killed.
const childWaitDelay = 30 * time.Second

// execRunner runs binary with the tool's arguments. Its standard output
// is the protocol's, so the tools never write to it directly.
func execRunner(binary string) mcpserver.Runner {
	return func(ctx context.Context, args []string) ([]byte, []byte, int, error) {
		var stdout, stderr bytes.Buffer
		c := exec.CommandContext(ctx, binary, args...)
		c.Env = childEnv(os.Environ())
		// An interrupt lets the child stop as on Ctrl+C, removing the
		// secrets it holds outside the output; Windows can't send one, so
		// the child is killed there, as it is after childWaitDelay.
		c.Cancel = func() error {
			if runtime.GOOS == "windows" {
				return c.Process.Kill()
			}
			return c.Process.Signal(os.Interrupt)
		}
		c.WaitDelay = childWaitDelay
		c.Stdout, c.Stderr = &stdout, &stderr
		err := c.Run()
		if ctx.Err() != nil {
			// The child was killed: its exit code says nothing.
			return stdout.Bytes(), stderr.Bytes(), 0, ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
		}
		return stdout.Bytes(), stderr.Bytes(), 0, err
	}
}
