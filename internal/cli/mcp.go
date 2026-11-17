// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
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

	"github.com/IgnatG/infraharvest/internal/cli/providers"
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
			return mcpserver.New(version.Version, root, execRunner(self), CheckRun, MCPProviders()).Run(ctx, &mcp.StdioTransport{})
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "directory the tools may read and write files in: selection files, output directories, reports and drift decisions files (default: the current directory, unless that is the root of a filesystem or the home directory)")
	return cmd
}

// MCPProviders returns the providers compiled in, for the MCP server,
// which knows them only through what it is passed (see
// mcpserver.Providers).
func MCPProviders() mcpserver.Providers {
	return mcpserver.Providers{Profiles: providers.Profiles(), Sources: providers.Sources(), Settings: providers.Settings(), Check: providers.Check}
}

// childEnv returns env, the server's environment, without the variables
// that profile a run (see startProfiles): a tool's child would
// write its profiles over the server's, at the same paths, and over those
// of the other children running at the same time.
func childEnv(env []string) []string {
	kept := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(name) {
		case cpuProfileEnv, memProfileEnv, goroutineLeakProfileEnv:
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
// is the protocol's, so the tools never write to it directly. What the
// child prints is kept as mcpserver.ChildOutput keeps it: its standard
// output up to a bound, and the last lines of its standard error, each
// passed to progress as it comes.
func execRunner(binary string) mcpserver.Runner {
	return func(ctx context.Context, args []string, progress func(string)) ([]byte, []byte, int, error) {
		output := mcpserver.NewChildOutput(progress)
		c := exec.CommandContext(ctx, binary, args...) //nolint:gosec // G702: binary is this program's own path and args are the MCP tool's, validated by the tool
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
		c.Stdout, c.Stderr = output.Stdout(), output.Stderr()
		err := c.Run()
		stdout, stderr, tooMuch := output.Result()
		if ctx.Err() != nil {
			// The child was killed: its exit code says nothing.
			return stdout, stderr, 0, ctx.Err()
		}
		if tooMuch != nil {
			return nil, stderr, 0, tooMuch
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout, stderr, exitErr.ExitCode(), nil
		}
		return stdout, stderr, 0, err
	}
}
