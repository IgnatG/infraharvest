// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/modelcontextprotocol/go-sdk/mcp"

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

	accept := map[string]any{"out": out, "engine": engineName, "rules": []string{"accept attribute=tags.team"}}
	r = callDriftTool(ctx, t, session, "drift_accept", accept)
	if len(asked) != 1 || !strings.Contains(asked[0], "accept 1 of them") || !strings.Contains(asked[0], `"accept attribute=tags.team"`) {
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

// mcpChange returns the change of a queue to attribute that r lists,
// failing the test if there is none.
func mcpChange(t *testing.T, what string, r mcpserver.DriftResult, attribute string) report.DriftChange {
	t.Helper()
	return driftChange(t, what, report.Drift{Changes: r.Changes}, "aws_sqs_queue", attribute)
}
