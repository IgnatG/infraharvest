// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/mcpserver"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// mcpAccounts are the accounts TestAWSMCP creates testdata/aws-mcp in,
// which no other test uses, by environment.
var mcpAccounts = map[string]string{"dev": "797979797979", "prod": "898989898989"}

// mcpProfile is the profile TestAWSMCP lists dev's account as.
const mcpProfile = "infraharvest-e2e-mcp"

// TestAWSMCP drives the MCP server's discover, import and report tools,
// and its drift tool with environments, as an agent does, over a session
// with a client that asks its user to confirm. testdata/aws-mcp has two
// queues in each of two accounts, dev's and prod's. discover lists dev's
// queues into a selection file, both included, saving what it lists under
// the root; the agent excludes one,
// and import, once the user confirms, imports the other alone, and report
// returns the import's report.md. Then the two accounts, imported at once
// with a configuration file naming them as the environments dev and
// prod, share one root, which each environment applies into its
// workspace. With a tag of dev's queue changed out of band, drift with
// environments dev, run with dev's credentials, checks the root as dev
// alone and reports the change, kept; as prod, with prod's, it finds
// none. report with kind drift returns drift.md.
func TestAWSMCP(t *testing.T) {
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	fixtureVars := func(env string, more ...tfexec.ApplyOption) []tfexec.ApplyOption {
		return append([]tfexec.ApplyOption{tfexec.Var("account=" + mcpAccounts[env]), tfexec.Var("env=" + env)}, more...)
	}
	fixtures := map[string]*appliedFixture{}
	for _, env := range []string{"dev", "prod"} {
		fixtures[env] = seedApplied(ctx, t, "aws-mcp", execPath, plugins, fixtureVars(env)...)
	}
	useAWSAccount(t, mcpAccounts["dev"], mcpProfile)

	// The tools' files stay under root.
	root := t.TempDir()
	var asked []string
	session := mcpSession(ctx, t, root, execPath, func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, req.Params.Message)
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	scope := func(more map[string]any) map[string]any {
		args := map[string]any{"provider": "aws", "services": []string{"sqs"}, "regions": []string{"us-east-1"}, "profile": mcpProfile}
		maps.Copy(args, more)
		return args
	}

	// discover lists dev's two queues, both included.
	var summary mcpserver.SelectionSummary
	callTool(ctx, t, session, "discover", scope(map[string]any{"selection": "selection.yaml"}), &summary)
	selectionFile := filepath.Join(root, "selection.yaml")
	queues := mcpserver.TypeCount{Type: "aws_sqs_queue", Included: 2}
	if summary.Selection != selectionFile || summary.Included != 2 || summary.Excluded != 0 || len(summary.Types) != 1 || summary.Types[0] != queues {
		t.Errorf("discover: %+v, want %s with dev's two queues included", summary, selectionFile)
	}
	if len(asked) != 0 {
		t.Errorf("discover asked the user: %q", asked)
	}
	// What it listed is saved under the root, not where the server runs.
	if _, err := os.Stat(filepath.Join(root, "infraharvest-out", ".infraharvest")); err != nil {
		t.Errorf("discover saved nothing under the root's infraharvest-out: %v", err)
	}

	// The agent leaves the jobs queue out, as the user asked.
	f, err := selection.Load(selectionFile)
	if err != nil {
		t.Fatal(err)
	}
	jobs := selectionEntry(t, f, "aws_sqs_queue", "/mcp-dev-jobs")
	orders := selectionEntry(t, f, "aws_sqs_queue", "/mcp-dev-orders")
	f.Resources[jobs].Include = false
	if err := f.Save(selectionFile); err != nil {
		t.Fatal(err)
	}

	// import asks the user, then imports the orders queue alone.
	var imported mcpserver.ImportResult
	callTool(ctx, t, session, "import", scope(map[string]any{"selection": "selection.yaml", "out": "generated", "engine": engineName}), &imported)
	out := filepath.Join(root, "generated")
	if len(asked) != 1 || !strings.Contains(asked[0], "Import the 1 resources") || !strings.Contains(asked[0], "(1 excluded)") {
		t.Errorf("import asked the user %q, want one confirmation of importing the one queue", asked)
	}
	if imported.ExitCode != report.ExitOK || imported.Totals.Imported != 1 || imported.Totals.Excluded != 1 || len(imported.FailedChecks) != 0 || len(imported.Failures) != 0 {
		t.Errorf("import: %+v", imported)
	}
	if want := filepath.Join(out, report.Dir, "report.md"); imported.Report != want {
		t.Errorf("import: report %s, want %s", imported.Report, want)
	}
	coverage := coverageOf(t, out)
	checkCoverage(t, "the import tool", coverage, nil)
	if !excludedIn(coverage, "aws_sqs_queue", f.Resources[jobs].ID) {
		t.Errorf("the import tool: the jobs queue the selection file excludes isn't reported left out: %+v", coverage.Excluded)
	}
	importsFile := ""
	for _, dir := range generatedDirs(t, out) {
		importsFile += readFile(t, filepath.Join(dir, engine.ImportsFileName))
	}
	if !strings.Contains(importsFile, f.Resources[orders].ID) || strings.Contains(importsFile, f.Resources[jobs].ID) {
		t.Errorf("the import tool's import blocks import other than the orders queue alone:\n%s", importsFile)
	}

	// report returns the import's report as it is.
	if text := callTool(ctx, t, session, "report", map[string]any{"out": "generated"}, nil); text != readFile(t, imported.Report) {
		t.Errorf("report returned:\n%s\nwant %s:\n%s", text, imported.Report, readFile(t, imported.Report))
	}

	// The two accounts as environments, imported at once by the CLI, as the
	// import tool takes no configuration file, share one root.
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	config := writeConfig(t, fmt.Sprintf(`version: 1
environments:
  dev:
    aws: { accounts: [%q] }
  prod:
    aws: { accounts: [%q] }
`, mcpAccounts["dev"], mcpAccounts["prod"]))
	merged := filepath.Join(root, "environments")
	runMovesImport(ctx, t,
		"import", "aws",
		"--config="+config,
		"--engine="+engineName,
		"--terraform-path="+execPath,
		"--accounts="+mcpAccounts["dev"]+","+mcpAccounts["prod"],
		"--regions=us-east-1",
		"--services=sqs",
		"--out="+merged,
		"--all",
	)
	coverage = coverageOf(t, merged)
	checkCoverage(t, "the import of the environments", coverage, nil)
	checkMerged(t, merged, coverage, "aws/us-east-1", "dev", "prod")
	credentials := func(env string) { t.Setenv("AWS_ACCESS_KEY_ID", mcpAccounts[env]) }
	applyMergedWorkspaces(ctx, t, filepath.Join(merged, "aws", "us-east-1"), execPath, plugins, credentials, "dev", "prod")
	fixtures["dev"].apply(ctx, t, fixtureVars("dev", tfexec.Var("team=ops"))...)

	// drift as dev, with dev's credentials, checks the root as dev alone.
	args := func(env string) map[string]any {
		return map[string]any{"out": "environments", "engine": engineName, "environments": []string{env}}
	}
	credentials("dev")
	r := callDriftTool(ctx, t, session, "drift", args("dev"))
	if r.ExitCode != report.ExitDrift || !r.DriftFound || r.Totals.Roots != 1 || r.Totals.Failed != 0 || len(r.FailedRoots) != 0 {
		t.Errorf("drift as dev: %+v", r)
	}
	c := mcpChange(t, "drift as dev", r, "tags.team")
	checkDriftChange(t, "drift as dev", c, "web", "ops", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	if c.Environment != "dev" {
		t.Errorf("drift as dev: the change is of %q", c.Environment)
	}
	for _, change := range r.Changes {
		if change.Environment != "dev" {
			t.Errorf("drift as dev: a change of %q: %+v", change.Environment, change)
		}
	}

	// As prod, with prod's credentials, it finds nothing.
	credentials("prod")
	r = callDriftTool(ctx, t, session, "drift", args("prod"))
	if r.ExitCode != report.ExitOK || r.DriftFound || r.Totals.Roots != 1 || r.Totals.Failed != 0 || len(r.Changes) != 0 {
		t.Errorf("drift as prod: %+v", r)
	}
	if text := callTool(ctx, t, session, "report", map[string]any{"out": "environments", "kind": "drift"}, nil); text != readFile(t, r.Report) {
		t.Errorf("report with kind drift returned:\n%s\nwant %s:\n%s", text, r.Report, readFile(t, r.Report))
	}
	// Only the import asked: drift changes no code.
	if len(asked) != 1 {
		t.Errorf("the tools asked the user %q, want the import's confirmation alone", asked)
	}
}
