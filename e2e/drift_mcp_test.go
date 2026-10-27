// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/driftpolicy"
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

// driftMCPRulesAccount is the account TestAWSDriftMCPRules creates
// testdata/aws-drift-queues in, which no other test uses.
const driftMCPRulesAccount = "757575757575"

// driftMCPRulesProfile is the profile the test imports
// driftMCPRulesAccount as.
const driftMCPRulesProfile = "infraharvest-e2e-drift-mcp-rules"

// TestAWSDriftMCPRules drives the MCP server's drift_accept tool with
// ordered rules, and with a decisions file it writes and the user edits.
// testdata/aws-drift-queues is imported and applied, and the orders
// queue's team tag and the jobs queue's owner tag change out of band.
// drift_accept with the rules keep attribute=tags.team, then accept
// attribute=tags.*, would accept both, as the last rule that matches
// decides: the user, asked, declines, and the code stays as it was. With
// the rules the other way round, it would accept the owner alone, keeping
// the team: the user confirms, and the owner is written into the code.
// drift_accept with write_decisions alone, which changes no code, asks
// nobody and writes drift.yaml, with the team's change undecided. The
// user accepts it there, and the owner tag changes again. drift_accept
// with the decisions file and write_decisions asks the user, accepts the
// team by its entry and keeps the new owner, and copies the merged
// decisions back to drift.yaml: the team's entry still accepted, and an
// undecided entry for the owner's new change.
func TestAWSDriftMCPRules(t *testing.T) {
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
	account := tfexec.Var("account=" + driftMCPRulesAccount)
	fixture := seedApplied(ctx, t, "aws-drift-queues", execPath, plugins, account)
	useAWSAccount(t, driftMCPRulesAccount, driftMCPRulesProfile)

	// The tools' files stay under root.
	root := t.TempDir()
	out := filepath.Join(root, "generated")
	runMovesImport(ctx, t,
		"import", "aws",
		"--engine="+engineName,
		"--terraform-path="+execPath,
		"--profile="+driftMCPRulesProfile,
		"--regions=us-east-1",
		"--services=sqs",
		"--out="+out,
		"--all",
	)
	checkCoverage(t, "the import", coverageOf(t, out), nil)
	applyGenerated(ctx, t, out, execPath, plugins)
	fixture.apply(ctx, t, account, tfexec.Var("team=ops"), tfexec.Var("owner=data"))

	var asked []string
	answer := "decline"
	session := mcpSession(ctx, t, root, execPath, func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, req.Params.Message)
		return &mcp.ElicitResult{Action: answer}, nil
	})
	accept := func(more map[string]any) map[string]any {
		args := map[string]any{"out": out, "engine": engineName}
		maps.Copy(args, more)
		return args
	}

	// Keep the team, then accept every tag: the last rule that matches
	// decides, so both would be accepted. The user declines.
	code := codeFiles(t, out)
	if text := callToolError(ctx, t, session, "drift_accept", accept(map[string]any{"rules": []string{"keep attribute=tags.team", "accept attribute=tags.*"}})); !strings.Contains(text, "didn't confirm") {
		t.Errorf("drift_accept, declined: %s", text)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "found 2 changes") || !strings.Contains(asked[0], "accept 2 of them") || !strings.Contains(asked[0], "keep 0") {
		t.Errorf("drift_accept asked the user %q, want a confirmation of accepting both changes", asked)
	}
	checkCodeUnchanged(t, "drift_accept, declined", code, out)

	// Accept every tag, then keep the team: the owner alone is accepted.
	answer = "accept"
	r := callDriftTool(ctx, t, session, "drift_accept", accept(map[string]any{"rules": []string{"accept attribute=tags.*", "keep attribute=tags.team"}}))
	if len(asked) != 2 || !strings.Contains(asked[1], "accept 1 of them") || !strings.Contains(asked[1], "keep 1") || !strings.Contains(asked[1], `"accept attribute=tags.*", "keep attribute=tags.team"`) {
		t.Errorf("drift_accept asked the user %q, want a confirmation of accepting the owner and keeping the team", asked[1:])
	}
	if r.ExitCode != report.ExitDrift || r.Totals.Written != 1 || r.Totals.Kept != 1 {
		t.Errorf("drift_accept: %+v", r)
	}
	checkDriftChange(t, "drift_accept", mcpChange(t, "drift_accept", r, "tags.owner"), "batch", "data", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	checkDriftChange(t, "drift_accept", mcpChange(t, "drift_accept", r, "tags.team"), "web", "ops", report.DriftKeep, report.DriftDecidedByFlag, report.DriftKept)
	if !codeMatches(t, out, `owner\s*=\s*"data"`) || !codeMatches(t, out, `team\s*=\s*"web"`) || codeMatches(t, out, `team\s*=\s*"ops"`) {
		t.Errorf("drift_accept: the code doesn't have owner = \"data\" with team = \"web\"")
	}

	// write_decisions alone changes no code, so nobody is asked.
	code = codeFiles(t, out)
	decisions := filepath.Join(out, driftpolicy.DefaultDecisionsFile)
	r = callDriftTool(ctx, t, session, "drift_accept", accept(map[string]any{"write_decisions": true}))
	if len(asked) != 2 {
		t.Errorf("drift_accept with write_decisions asked the user %q", asked[2:])
	}
	if r.Decisions != decisions || r.ExitCode != report.ExitDrift {
		t.Errorf("drift_accept with write_decisions: %+v, want the decisions in %s", r, decisions)
	}
	checkCodeUnchanged(t, "drift_accept with write_decisions", code, out)
	decisionEntry(t, decisions, "tags.team", driftpolicy.Undecided)

	// The user accepts the team's change, and the owner changes again.
	setDecision(t, decisions, "tags.team", report.DriftAccept)
	fixture.apply(ctx, t, account, tfexec.Var("team=ops"), tfexec.Var("owner=ml"))
	r = callDriftTool(ctx, t, session, "drift_accept", accept(map[string]any{"decisions": decisions, "write_decisions": true}))
	if len(asked) != 3 || !strings.Contains(asked[2], `the decisions file "`+decisions+`"`) || !strings.Contains(asked[2], "accept 1 of them") ||
		!strings.Contains(asked[2], `written into the decisions file "`+decisions+`"`) {
		t.Errorf("drift_accept with decisions asked the user %q, want a confirmation of accepting the team by the decisions file", asked[2:])
	}
	if r.ExitCode != report.ExitDrift || r.Totals.Written != 1 || r.Decisions != decisions {
		t.Errorf("drift_accept with decisions: %+v", r)
	}
	checkDriftChange(t, "drift_accept with decisions", mcpChange(t, "drift_accept with decisions", r, "tags.team"), "web", "ops", report.DriftAccept, report.DriftDecidedByEntry, report.DriftWritten)
	checkDriftChange(t, "drift_accept with decisions", mcpChange(t, "drift_accept with decisions", r, "tags.owner"), "batch", "ml", report.DriftKeep, report.DriftDecidedByDefaults, report.DriftKept)
	if !codeMatches(t, out, `team\s*=\s*"ops"`) || !codeMatches(t, out, `owner\s*=\s*"data"`) {
		t.Errorf("drift_accept with decisions: the code doesn't have team = \"ops\" with owner = \"data\"")
	}
	// The run merged into a private copy of the file, which is copied
	// back: the user's decision stays, and the owner's new change is
	// added, undecided.
	decisionEntry(t, decisions, "tags.team", report.DriftAccept)
	decisionEntry(t, decisions, "tags.owner", driftpolicy.Undecided)
}

// mcpChange returns the change of a queue to attribute that r lists,
// failing the test if there is none.
func mcpChange(t *testing.T, what string, r mcpserver.DriftResult, attribute string) report.DriftChange {
	t.Helper()
	return driftChange(t, what, report.Drift{Changes: r.Changes}, "aws_sqs_queue", attribute)
}
