// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/mcpserver"
	"github.com/IgnatG/infraharvest/internal/report"
)

// driftMCPAccount is the account TestAWSDriftMCP creates
// testdata/aws-drift-queues in, which no other test uses.
const driftMCPAccount = "787878787878"

// driftMCPProfile is the profile the test imports driftMCPAccount as.
const driftMCPProfile = "infraharvest-e2e-drift-mcp"

// TestAWSDriftMCP imports testdata/aws-drift-queues, applies the root
// into a local state, and changes a queue's tag out of band, then drives
// the MCP server's drift tools, as an agent does, over a session with a
// client that asks its user to confirm. drift reports the change, kept,
// with exit code 4, and changes no code; drift_accept, with an accept
// rule, asks the user, saying it would accept the change, and once they
// confirm writes it into the code. drift then finds the change absorbed,
// with exit code 0, and the code plans no change.
func TestAWSDriftMCP(t *testing.T) {
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	account := tfexec.Var("account=" + driftMCPAccount)
	fixture := seedApplied(ctx, t, "aws-drift-queues", execPath, plugins, account)
	useAWSAccount(t, driftMCPAccount, driftMCPProfile)

	// The tools' files stay under root.
	root := t.TempDir()
	out := filepath.Join(root, "generated")
	runMovesImport(ctx, t,
		"import", "aws",
		"--engine="+engineName,
		"--terraform-path="+execPath,
		"--profile="+driftMCPProfile,
		"--regions=us-east-1",
		"--services=sqs",
		"--out="+out,
		"--all",
	)
	checkCoverage(t, "the import", coverageOf(t, out), nil)
	dirs := applyGenerated(ctx, t, out, execPath, plugins)
	fixture.apply(ctx, t, account, tfexec.Var("team=ops"))

	var asked []string
	session := mcpSession(ctx, t, root, execPath, func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, req.Params.Message)
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	args := map[string]any{"out": out, "engine": engineName}

	code := codeFiles(t, out)
	r := callDriftTool(ctx, t, session, "drift", args)
	if r.ExitCode != report.ExitDrift || !r.DriftFound || r.Totals.Kept == 0 || r.Totals.Written != 0 || len(r.FailedRoots) != 0 {
		t.Errorf("drift: %+v", r)
	}
	checkDriftChange(t, "drift", mcpChange(t, "drift", r, "tags.team"), "web", "ops", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	if r.Report != filepath.Join(out, report.Dir, report.DriftMarkdownFile) {
		t.Errorf("drift: report %s", r.Report)
	}
	checkCodeUnchanged(t, "drift", code, out)
	if len(asked) != 0 {
		t.Errorf("drift asked the user: %q", asked)
	}

	accept := map[string]any{"out": out, "engine": engineName, "accept": []string{"attribute=tags.team"}}
	r = callDriftTool(ctx, t, session, "drift_accept", accept)
	if len(asked) != 1 || !strings.Contains(asked[0], "accept 1 of them") || !strings.Contains(asked[0], `"attribute=tags.team"`) {
		t.Errorf("drift_accept asked the user %q, want one confirmation of accepting the change", asked)
	}
	if r.ExitCode != report.ExitOK || r.Totals.Written != 1 {
		t.Errorf("drift_accept: %+v", r)
	}
	checkDriftChange(t, "drift_accept", mcpChange(t, "drift_accept", r, "tags.team"), "web", "ops", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	if !codeMatches(t, out, `team\s*=\s*"ops"`) || codeMatches(t, out, `team\s*=\s*"web"`) {
		t.Errorf("drift_accept: the code doesn't have the queue's tag team = \"ops\" in place of \"web\"")
	}

	// Drift doesn't write the state: the change is absorbed now.
	r = callDriftTool(ctx, t, session, "drift", args)
	if r.ExitCode != report.ExitOK || r.DriftFound {
		t.Errorf("drift after drift_accept: %+v", r)
	}
	if c := mcpChange(t, "drift after drift_accept", r, "tags.team"); c.Outcome != report.DriftAbsorbed {
		t.Errorf("drift after drift_accept: the accepted tag is %s, want %s", c.Outcome, report.DriftAbsorbed)
	}
	checkAccepted(ctx, t, dirs, execPath, plugins)
}

// mcpSession returns a client session with the MCP server, whose files
// stay under root, and which runs infraharvest in this process, with the
// engine at execPath; elicit answers its requests for the user's
// confirmation.
func mcpSession(ctx context.Context, t *testing.T, root, execPath string, elicit func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	run := func(ctx context.Context, args []string) ([]byte, []byte, int, error) {
		// The engine the test found, before the arguments after "--".
		if i := slices.Index(args, "--"); i >= 0 {
			args = slices.Insert(slices.Clone(args), i, "--terraform-path="+execPath)
		}
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
	if _, err := mcpserver.New("e2e", root, run).Connect(ctx, serverTransport, nil); err != nil {
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
	if res.IsError {
		t.Fatalf("%s returned an error: %s", name, strings.Join(text, "\n"))
	}
	content, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var r mcpserver.DriftResult
	if err := json.Unmarshal(content, &r); err != nil {
		t.Fatalf("%s: %s: %v", name, content, err)
	}
	return r
}

// mcpChange returns the change of a queue to attribute that r lists,
// failing the test if there is none.
func mcpChange(t *testing.T, what string, r mcpserver.DriftResult, attribute string) report.DriftChange {
	t.Helper()
	return driftChange(t, what, report.Drift{Changes: r.Changes}, "aws_sqs_queue", attribute)
}
