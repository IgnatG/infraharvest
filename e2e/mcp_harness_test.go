// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/mcpserver"
)

// mcpSession returns a client session with the MCP server, whose files
// stay under root, and which runs and checks infraharvest in this process,
// with the engine at execPath; elicit answers its requests for the user's
// confirmation.
func mcpSession(ctx context.Context, t *testing.T, root, execPath string, elicit func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	// withEngine returns a copy of args with the engine the test found: for
	// drift, before the arguments after "--"; for import, at the end.
	withEngine := func(args []string) []string {
		if i := slices.Index(args, "--"); i >= 0 {
			return slices.Insert(slices.Clone(args), i, "--terraform-path="+execPath)
		}
		if len(args) > 0 && args[0] == "import" {
			return append(slices.Clone(args), "--terraform-path="+execPath)
		}
		return slices.Clone(args)
	}
	run := func(ctx context.Context, args []string) ([]byte, []byte, int, error) {
		args = withEngine(args)
		var stdout, stderr bytes.Buffer
		cmd := cli.NewCmdRoot()
		cmd.SetArgs(args)
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		err := cmd.ExecuteContext(ctx)
		t.Logf("infraharvest %s: exit %d, %v\n%s", strings.Join(args, " "), importer.ExitCode(err), err, stderr.String())
		return stdout.Bytes(), stderr.Bytes(), importer.ExitCode(err), nil
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := mcpserver.New("e2e", root, run, cli.CheckRun).Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e"}, &mcp.ClientOptions{ElicitationHandler: elicit})
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// callDriftTool calls the drift tool name with args and returns its
// result, failing the test if the call fails or returns an error.
func callDriftTool(ctx context.Context, t *testing.T, session *mcp.ClientSession, name string, args map[string]any) mcpserver.DriftResult {
	t.Helper()
	var r mcpserver.DriftResult
	callTool(ctx, t, session, name, args, &r)
	return r
}

// callTool calls the tool name with args, decodes its structured result
// into result unless it is nil, and returns its text, failing the test if
// the call fails or returns an error.
func callTool(ctx context.Context, t *testing.T, session *mcp.ClientSession, name string, args map[string]any, result any) string {
	t.Helper()
	res, text := callToolResult(ctx, t, session, name, args)
	if res.IsError {
		t.Fatalf("%s returned an error: %s", name, text)
	}
	if result != nil {
		content, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(content, result); err != nil {
			t.Fatalf("%s: %s: %v", name, content, err)
		}
	}
	return text
}

// callToolError calls the tool name with args and returns the text of
// the error it returns, failing the test if the call fails or the tool
// returns no error.
func callToolError(ctx context.Context, t *testing.T, session *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, text := callToolResult(ctx, t, session, name, args)
	if !res.IsError {
		t.Fatalf("%s returned no error: %s", name, text)
	}
	return text
}

// callToolResult calls the tool name with args and returns its result and
// text, failing the test if the call fails.
func callToolResult(ctx context.Context, t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var text []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text = append(text, tc.Text)
		}
	}
	t.Logf("%s: %s", name, strings.Join(text, "\n"))
	return res, strings.Join(text, "\n")
}
