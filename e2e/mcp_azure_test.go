// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || azure)

package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/mcpserver"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// TestAzureMCP drives the MCP server's discover and import tools on the
// resource group testdata/azure creates, as an agent does: discover lists
// it into a selection file, which leaves out what another tool manages,
// and the agent excludes the public IP as well. import
// asks the user, naming the resource group and the subscription, and
// imports what the file includes, and nothing else, into the one root of
// the subscription and the resource group, which plans as imports with no
// changes.
func TestAzureMCP(t *testing.T) {
	e := seedAzure(t)
	root := t.TempDir()
	var asked []string
	session := mcpSession(e.ctx, t, root, e.execPath, func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked = append(asked, req.Params.Message)
		return &mcp.ElicitResult{Action: "accept"}, nil
	})
	scope := map[string]any{"provider": "azure", "resource_group": azureResourceGroup, "services": azureServices, "selection": "selection.yaml"}

	var summary mcpserver.SelectionSummary
	callTool(e.ctx, t, session, "discover", scope, &summary)
	selectionFile := filepath.Join(root, "selection.yaml")
	if summary.Selection != selectionFile || summary.Included == 0 {
		t.Errorf("discover: %+v, want the resource group's resources included in %s", summary, selectionFile)
	}
	if len(asked) != 0 {
		t.Errorf("discover asked the user: %q", asked)
	}
	f, err := selection.Load(selectionFile)
	if err != nil {
		t.Fatal(err)
	}
	ip := selectionEntry(t, f, "azurerm_public_ip", "/infraharvest-e2e-app")
	f.Resources[ip].Include = false
	if err := f.Save(selectionFile); err != nil {
		t.Fatal(err)
	}

	var imported mcpserver.ImportResult
	importArgs := map[string]any{"provider": "azure", "resource_group": azureResourceGroup, "services": azureServices, "selection": "selection.yaml", "out": "generated", "engine": e.engineName}
	callTool(e.ctx, t, session, "import", importArgs, &imported)
	reads := "resource group " + azureResourceGroup + " of the azure subscription of ARM_SUBSCRIPTION_ID (" + strings.Join(azureServices, ", ") + ")"
	if len(asked) != 1 || !strings.Contains(asked[0], reads) {
		t.Errorf("import asked the user %q, want one confirmation that it reads %s", asked, reads)
	}
	if imported.ExitCode != report.ExitOK || imported.Totals.Imported == 0 || imported.Totals.Excluded != summary.Excluded+1 || len(imported.FailedChecks) != 0 || len(imported.Failures) != 0 {
		t.Errorf("import: %+v", imported)
	}
	out := filepath.Join(root, "generated")
	coverage := coverageOf(t, out)
	checkCoverage(t, "the import tool", coverage, nil)
	checkImportsSelected(t, f, coverage)
	if !excludedIn(coverage, "azurerm_public_ip", f.Resources[ip].ID) {
		t.Errorf("the import tool: the public IP the selection file excludes isn't reported left out: %+v", coverage.Excluded)
	}
	for _, dir := range generatedDirs(t, out) {
		if filepath.Clean(dir) != azureRoot(out) {
			t.Errorf("root %s, want %s", dir, azureRoot(out))
		}
		checkNoChanges(e.ctx, t, dir, e.execPath, e.plugins, nil)
	}
}
