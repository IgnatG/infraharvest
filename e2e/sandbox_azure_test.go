// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && sandbox && (!minimal || azure)

package e2e

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// sandboxAzureResourceGroup is the resource group the sandbox bootstrap
// (testdata/sandbox/azure-bootstrap) creates for the estate, unless
// SANDBOX_AZURE_RESOURCE_GROUP names another.
const sandboxAzureResourceGroup = "infraharvest-sandbox"

// sandboxAzureFound are the types of the estate whose every resource the
// import must find, by ID: what floci-az can't serve, and the lock, which
// Resource Graph doesn't index.
var sandboxAzureFound = []string{
	"azurerm_key_vault", "azurerm_management_lock", "azurerm_route_table",
	"azurerm_storage_account", "azurerm_storage_container",
}

// azureAuthorizationErrors are what Azure, the SDK and the azurerm
// provider say when the caller's role doesn't allow a call: the read-only
// template grants too little.
var azureAuthorizationErrors = []string{
	"AuthorizationFailed", "AuthorizationPermissionMismatch", "LinkedAuthorizationFailed",
	"does not have authorization", "RESPONSE 403", "403 Forbidden", "status 403",
}

// TestSandboxAzure creates testdata/sandbox/azure in a real subscription,
// in the estate's resource group, as the fixture identity
// (SANDBOX_AZURE_FIXTURE_CLIENT_ID), imports the resource group as the
// read-only identity (SANDBOX_AZURE_READONLY_CLIENT_ID), which has what
// permissions/azure grants, as a user would, and checks that nothing was
// denied, that the engine plans every generated resource as an import
// with no changes, and that the import found the resource group, with its
// tags, and what the estate created. It destroys the estate when it is
// done. The sandbox-azure workflow runs it, after approval; see
// e2e/README.md.
func TestSandboxAzure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Minute)
	defer cancel()
	s := newAzureSandbox(ctx, t)

	estate := seedSandbox(ctx, t, "azure", s.fixtureDir, s.execPath, s.plugins,
		map[string]string{"ARM_CLIENT_ID": s.fixtureClientID},
		"suffix="+s.engineName, "resource_group="+s.resourceGroup)
	created := estate.apply(ctx, t)
	group := sandboxResourceGroup(ctx, t, estate)

	logged := s.importAzure(ctx, t)
	coverage := readCoverage(t, s.out)
	checkNoAuthorizationErrors(t, logged, coverage, azureAuthorizationErrors)
	root, imported := s.checkAzureImport(ctx, t, coverage, created)
	checkAzureFound(t, coverage, created, group)
	checkGroupTags(t, root, group)
	if imported["azurerm_resource_group"] == 0 {
		t.Errorf("the import doesn't import the resource group %s", s.resourceGroup)
	}
}

// azureSandbox is what the Azure sandbox test runs with: the subscription,
// the estate's resource group and the client IDs of the two identities,
// the engine, and the directories that outlive the test, so that the
// workflow can destroy what a failed run left and upload the import.
type azureSandbox struct {
	subscriptionID, resourceGroup     string
	fixtureClientID, readonlyClientID string
	engineName, execPath              string
	fixtureDir, plugins, out          string
}

// newAzureSandbox returns what the test runs with, from the environment
// the sandbox-azure workflow sets: ARM_TENANT_ID, ARM_SUBSCRIPTION_ID and
// the way to sign in (ARM_USE_OIDC in the workflow), which both identities
// share, and the client ID of each. It skips the test if no subscription
// is named.
func newAzureSandbox(ctx context.Context, t *testing.T) *azureSandbox {
	t.Helper()
	s := &azureSandbox{
		subscriptionID:   os.Getenv("ARM_SUBSCRIPTION_ID"),
		resourceGroup:    envOr("SANDBOX_AZURE_RESOURCE_GROUP", sandboxAzureResourceGroup),
		fixtureClientID:  os.Getenv("SANDBOX_AZURE_FIXTURE_CLIENT_ID"),
		readonlyClientID: os.Getenv("SANDBOX_AZURE_READONLY_CLIENT_ID"),
	}
	if s.subscriptionID == "" || os.Getenv("ARM_TENANT_ID") == "" || s.fixtureClientID == "" || s.readonlyClientID == "" {
		skipUnconfigured(t, "ARM_TENANT_ID, ARM_SUBSCRIPTION_ID, SANDBOX_AZURE_FIXTURE_CLIENT_ID and SANDBOX_AZURE_READONLY_CLIENT_ID name a sandbox subscription; see the sandbox-azure workflow")
	}
	if os.Getenv(azureMetadataHostEnv) != "" || strings.EqualFold(strings.TrimSpace(os.Getenv("ARM_ENVIRONMENT")), "stack") {
		t.Fatal("an emulator is set up (E2E_AZURE_METADATA_HOST or ARM_ENVIRONMENT=stack): the sandbox test runs against Azure")
	}
	if strings.EqualFold(s.fixtureClientID, s.readonlyClientID) {
		t.Fatal("the fixture and read-only identities are the same: the import must run with what permissions/azure grants alone")
	}
	onEmulator = false
	s.fixtureDir = envOr("SANDBOX_FIXTURE_DIR", t.TempDir())
	s.plugins = envOr("SANDBOX_PLUGIN_CACHE", t.TempDir())
	s.out = envOr("SANDBOX_OUTPUT_DIR", t.TempDir())
	var binary engine.Binary
	s.engineName, binary = e2eEngine(t)
	var err error
	if s.execPath, err = binary.Find(ctx, "", filepath.Join(s.plugins, binary.Name)); err != nil {
		t.Fatal(err)
	}
	return s
}

// importAzure imports the estate's resource group, every service, as the
// read-only identity, into the output directory, and returns what the
// import logged. From then on the test's own Azure calls, such as the
// plans, are read-only too.
func (s *azureSandbox) importAzure(ctx context.Context, t *testing.T) string {
	t.Helper()
	t.Setenv("ARM_CLIENT_ID", s.readonlyClientID)
	logged := &syncBuffer{}
	logs := log.Writer()
	log.SetOutput(io.MultiWriter(logs, logged))
	defer log.SetOutput(logs)
	root := cli.NewCmdRoot()
	// The output's own layout isn't named here: checkAzureImport finds
	// the root.
	root.SetArgs([]string{
		"import", "azure",
		"--engine=" + s.engineName,
		"--terraform-path=" + s.execPath,
		"--resource-group=" + s.resourceGroup,
		"--services=*",
		"--out=" + s.out,
		"--all",
		"--allow-partial",
	})
	if err := root.ExecuteContext(ctx); importer.ExitCode(err) != report.ExitOK && importer.ExitCode(err) != report.ExitPartial {
		t.Fatalf("infraharvest import azure: %v", err)
	}
	return logged.String()
}

// checkAzureImport checks the import in the output directory: nothing
// failed or was left out, every check passed, the one root, the resource
// group's, plans as imports with no changes, and each azurerm type the
// estate created is imported at least as many times as it was created. It
// returns the root and how many resources of each type it imports.
func (s *azureSandbox) checkAzureImport(ctx context.Context, t *testing.T, coverage report.Report, created []*tfjson.StateResource) (string, map[string]int) {
	t.Helper()
	t.Logf("totals: %+v", coverage.Totals)
	for _, f := range coverage.Failures {
		t.Errorf("failure: %+v", f)
	}
	for _, d := range coverage.Directories {
		for _, l := range d.LeftOut {
			t.Errorf("%s: %s (%s) left out: %v", d.Path, l.Address, l.ID, l.Errors)
		}
		for _, c := range d.Checks {
			if !c.Passed {
				t.Errorf("%s: %s failed: %v", d.Path, c.Name, c.Details)
			}
		}
	}

	// One root, for the subscription and the resource group:
	// <out>/<provider>/<subscription>/<resource group>.
	dirs := generatedDirs(t, s.out)
	if len(dirs) != 1 {
		t.Fatalf("roots %v, want one, for the resource group %s", dirs, s.resourceGroup)
	}
	root := filepath.Clean(dirs[0])
	if !strings.EqualFold(filepath.Base(root), s.resourceGroup) || !strings.EqualFold(filepath.Base(filepath.Dir(root)), s.subscriptionID) {
		t.Errorf("root %s, want one named after the subscription %s and the resource group %s", root, s.subscriptionID, s.resourceGroup)
	}
	if rejected, err := os.ReadFile(filepath.Join(root, engine.RejectedFileName)); err == nil {
		t.Errorf("%s: resources left out:\n%s", root, rejected)
	}
	imported := checkNoChanges(ctx, t, root, s.execPath, s.plugins, nil)
	t.Logf("imported: %v", imported)

	seeded := map[string]int{}
	for _, r := range created {
		// Not random_string, which only names the estate's resources.
		if strings.HasPrefix(r.Type, "azurerm_") {
			seeded[r.Type]++
		}
	}
	for _, typ := range sortedKeys(seeded) {
		if imported[typ] < seeded[typ] {
			t.Errorf("%s: created %d, imported %d", typ, seeded[typ], imported[typ])
		}
	}
	return root, imported
}

// sandboxResourceGroup returns the estate's resource group, as its data
// source read it when the estate was applied: its ID and tags.
func sandboxResourceGroup(ctx context.Context, t *testing.T, estate *sandboxEstate) *tfjson.StateResource {
	t.Helper()
	state, err := estate.tf.Show(ctx)
	if err != nil {
		t.Fatalf("seed: show: %v", err)
	}
	for _, r := range state.Values.RootModule.Resources {
		if r.Mode == tfjson.DataResourceMode && r.Type == "azurerm_resource_group" {
			return r
		}
	}
	t.Fatal("the estate has no data source for its resource group")
	return nil
}

// checkAzureFound checks that the import imports the resource group and
// every resource of the estate of the types sandboxAzureFound names, by
// ID. Azure IDs ignore case.
func checkAzureFound(t *testing.T, coverage report.Report, created []*tfjson.StateResource, group *tfjson.StateResource) {
	t.Helper()
	imported := map[string]bool{}
	for _, d := range coverage.Directories {
		for _, r := range d.Imported {
			imported[strings.ToLower(r.ID)] = true
		}
	}
	want := []*tfjson.StateResource{group}
	for _, r := range created {
		for _, typ := range sandboxAzureFound {
			if r.Type == typ {
				want = append(want, r)
			}
		}
	}
	for _, r := range want {
		id, _ := r.AttributeValues["id"].(string)
		if id == "" || !imported[strings.ToLower(id)] {
			t.Errorf("the import doesn't import %s %q", r.Address, id)
		}
	}
}

// checkGroupTags checks that the generated configuration keeps the resource
// group's tags, which floci-az drops: each value is in it, on the group or
// in local.tags. The plan with no changes checks they are the group's.
func checkGroupTags(t *testing.T, root string, group *tfjson.StateResource) {
	t.Helper()
	tags, _ := group.AttributeValues["tags"].(map[string]any)
	if len(tags) == 0 {
		t.Fatalf("the resource group %v has no tags: the bootstrap gives it some", group.AttributeValues["name"])
	}
	generated := configuration(t, root)
	for key, value := range tags {
		if v, _ := value.(string); !strings.Contains(generated, `"`+v+`"`) {
			t.Errorf("the configuration doesn't have the resource group's tag %s = %q", key, v)
		}
	}
}
