// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || google)

package e2e

import (
	"context"
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/mcpserver"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// TestGoogleMCP drives the MCP server's discover and import tools on the
// project testdata/google creates, in global, through cloud_asset, as an
// agent does: discover lists it into a selection file, and the agent
// excludes the Pub/Sub subscription. import asks the user, naming the
// project and the region, and imports what the file includes, and nothing
// else, and every root plans as imports with no changes.
func TestGoogleMCP(t *testing.T) {
	e := seedGoogle(t)
	useAssetSearch(t, e, e.state)
	root := t.TempDir()
	var asked []string
	session := mcpSession(e.ctx, t, root, e.execPath, func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, req.Params.Message)
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	scope := func(more map[string]any) map[string]any {
		args := map[string]any{"provider": "google", "projects": []string{googleProject}, "services": []string{"cloud_asset"}, "regions": []string{"global"}, "selection": "selection.yaml"}
		maps.Copy(args, more)
		return args
	}

	var summary mcpserver.SelectionSummary
	callTool(e.ctx, t, session, "discover", scope(nil), &summary)
	selectionFile := filepath.Join(root, "selection.yaml")
	if summary.Selection != selectionFile || summary.Included == 0 {
		t.Errorf("discover: %+v, want the project's resources included in %s", summary, selectionFile)
	}
	if len(asked) != 0 {
		t.Errorf("discover asked the user: %q", asked)
	}
	f, err := selection.Load(selectionFile)
	if err != nil {
		t.Fatal(err)
	}
	subscription := selectionEntry(t, f, "google_pubsub_subscription", "infraharvest-e2e-worker")
	f.Resources[subscription].Include = false
	if err := f.Save(selectionFile); err != nil {
		t.Fatal(err)
	}

	var imported mcpserver.ImportResult
	callTool(e.ctx, t, session, "import", scope(map[string]any{"out": "generated", "engine": e.engineName}), &imported)
	reads := "the google projects " + googleProject + " in global (cloud_asset)"
	if len(asked) != 1 || !strings.Contains(asked[0], reads) {
		t.Errorf("import asked the user %q, want one confirmation that it reads %s", asked, reads)
	}
	if imported.ExitCode != report.ExitOK || imported.Totals.Imported == 0 || len(imported.FailedChecks) != 0 || len(imported.Failures) != 0 {
		t.Errorf("import: %+v", imported)
	}
	out := filepath.Join(root, "generated")
	coverage := coverageOf(t, out)
	checkCoverage(t, "the import tool", coverage, nil)
	checkImportsSelected(t, f, coverage)
	if !excludedIn(coverage, "google_pubsub_subscription", f.Resources[subscription].ID) {
		t.Errorf("the import tool: the subscription the selection file excludes isn't reported left out: %+v", coverage.Excluded)
	}
	for _, dir := range generatedDirs(t, out) {
		checkNoChanges(e.ctx, t, dir, e.execPath, e.plugins, nil)
	}
}
