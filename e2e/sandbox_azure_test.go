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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// sandboxAzureResourceGroup is the resource group the sandbox bootstrap
// (testdata/sandbox/azure-bootstrap) creates for the estate, unless
// SANDBOX_AZURE_RESOURCE_GROUP names another.
const sandboxAzureResourceGroup = "infraharvest-sandbox"

// sandboxAzureFound are the types of the estate whose every resource the
// import must find, by ID: what floci-az can't serve, the lock, which
// Resource Graph doesn't index, and the role assignments, which only its
// authorizationresources table has, and what a team runs, which floci-az
// doesn't serve either: the PostgreSQL server, the registry, the messaging namespaces with their queue and event hub, the
// load balancer with its parts, the private endpoint with its DNS zone
// and link, and the virtual machine with its network interface.
var sandboxAzureFound = []string{
	"azurerm_key_vault", "azurerm_management_lock", "azurerm_role_assignment", "azurerm_route_table",
	"azurerm_storage_account", "azurerm_storage_container",
	"azurerm_postgresql_flexible_server", "azurerm_container_registry",
	"azurerm_servicebus_namespace", "azurerm_servicebus_queue", "azurerm_eventhub_namespace", "azurerm_eventhub",
	"azurerm_public_ip", "azurerm_lb", "azurerm_lb_backend_address_pool", "azurerm_lb_probe", "azurerm_lb_rule",
	"azurerm_private_dns_zone", "azurerm_private_dns_zone_virtual_network_link", "azurerm_private_endpoint",
	"azurerm_network_interface", "azurerm_linux_virtual_machine",
}

// sandboxAzureSecrets are the attributes of the estate's secrets, by
// type, that no file of an import's output may hold: the PostgreSQL
// server's administrator password, which Azure never returns, and the
// virtual machine's private key, which Azure never has.
var sandboxAzureSecrets = map[string][]string{
	"random_password": {"result"},
	"tls_private_key": {"private_key_pem", "private_key_openssh", "private_key_pem_pkcs8"},
}

// administratorPassword matches a generated argument that sets a
// PostgreSQL server's administrator password, which the import leaves
// out, as Azure never returns it, rather than read from a variable.
var administratorPassword = regexp.MustCompile(`(?m)^\s*administrator_password\s*=`)

// checkAzureSecrets checks that no file of the import's output in out
// holds a secret the estate created (see sandboxAzureSecrets), and that
// the configuration doesn't set the PostgreSQL server's administrator
// password.
func checkAzureSecrets(t *testing.T, out string, created []*tfjson.StateResource) {
	t.Helper()
	var secrets []string
	for _, r := range created {
		for _, attribute := range sandboxAzureSecrets[r.Type] {
			if v, _ := r.AttributeValues[attribute].(string); strings.TrimSpace(v) != "" {
				secrets = append(secrets, strings.TrimSpace(v))
			}
		}
	}
	if len(secrets) == 0 {
		t.Fatal("the estate has no secrets to look for: it makes a password and a private key")
	}
	for name, content := range outputFiles(t, out) {
		for _, secret := range secrets {
			if strings.Contains(content, secret) {
				t.Errorf("%s holds a secret of the estate", name)
			}
		}
		if strings.HasSuffix(name, ".tf") && administratorPassword.MatchString(content) {
			t.Errorf("%s sets the PostgreSQL server's administrator_password, which Azure never returns", name)
		}
	}
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
// tags, and what the estate created, and that no file of the output
// holds the estate's secrets (see checkAzureSecrets). Applied, the root
// must take a tag added out of band through infraharvest drift (see
// checkAzureDrift). It destroys the estate when it is done. The
// sandbox-azure workflow runs it, after approval; see e2e/README.md.
func TestSandboxAzure(t *testing.T) {
	// The PostgreSQL server takes most of the estate's apply.
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Minute)
	defer cancel()
	s := newAzureSandbox(ctx, t)

	estate := seedSandbox(ctx, t, "azure", s.fixtureDir, s.execPath, s.plugins,
		map[string]string{"ARM_CLIENT_ID": s.fixtureClientID},
		"suffix="+s.engineName, "resource_group="+s.resourceGroup)
	created := estate.apply(ctx, t)
	group := sandboxResourceGroup(ctx, t, estate)

	logged := s.importAzure(ctx, t, s.out)
	coverage := readCoverage(t, s.out)
	checkNoAuthorizationErrors(t, logged, coverage, azureAuthorizationErrors)
	root, imported := s.checkAzureImport(ctx, t, coverage, created)
	checkAzureFound(t, coverage, created, group)
	checkAzureSecrets(t, s.out, created)
	checkGroupTags(t, root, group)
	checkAzureRoles(t, coverage, created, root)
	s.checkAzureQuery(ctx, t, created)
	if imported["azurerm_resource_group"] == 0 {
		t.Errorf("the import doesn't import the resource group %s", s.resourceGroup)
	}
	s.checkAzureDrift(ctx, t, estate, root)
	s.checkAzureAzapi(ctx, t, created, imported)
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
	var binary tfrun.Binary
	s.engineName, binary = e2eEngine(t)
	var err error
	if s.execPath, err = binary.Find(ctx, "", filepath.Join(s.plugins, binary.Name)); err != nil {
		t.Fatal(err)
	}
	return s
}

// importAzure imports the estate's resource group, every service, as the
// read-only identity, into out, with the flags extra adds, and returns
// what the import logged. From then on the test's own Azure calls, such
// as the plans, are read-only too.
func (s *azureSandbox) importAzure(ctx context.Context, t *testing.T, out string, extra ...string) string {
	t.Helper()
	t.Setenv("ARM_CLIENT_ID", s.readonlyClientID)
	logged := &syncBuffer{}
	logs := log.Writer()
	log.SetOutput(io.MultiWriter(logs, logged))
	defer log.SetOutput(logs)
	root := cli.NewCmdRoot()
	// The output's own layout isn't named here: checkAzureImport finds
	// the root.
	root.SetArgs(append([]string{
		"import", "azure",
		"--engine=" + s.engineName,
		"--terraform-path=" + s.execPath,
		"--resource-group=" + s.resourceGroup,
		"--services=*",
		"--out=" + out,
		"--all",
		"--allow-partial",
	}, extra...))
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
	if rejected, err := os.ReadFile(filepath.Join(root, rootfile.RejectedFileName)); err == nil {
		t.Errorf("%s: resources left out:\n%s", root, rejected)
	}
	imported := checkNoChanges(ctx, t, root, s.execPath, s.plugins, nil)
	t.Logf("imported: %v", imported)
	checkApplied(ctx, t, s.out, []string{root}, s.execPath, s.plugins, nil, func(string) []tfexec.PlanOption { return nil })

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

// sandboxAzureQueried is a type of the estate no lister of its own
// lists: the query service lists it, through Terraform's list resources
// (see checkAzureQuery), and resource_graph too, which lists whatever
// Resource Graph returns.
const sandboxAzureQueried = "azurerm_application_security_group"

// checkAzureQuery takes the estate's application security group through
// the query service as the read-only identity, in the estate's resource
// group (see checkQueryRoundTrip). Call it once importAzure has signed in
// as that identity.
func (s *azureSandbox) checkAzureQuery(ctx context.Context, t *testing.T, created []*tfjson.StateResource) {
	t.Helper()
	name := ""
	for _, r := range created {
		if r.Type == sandboxAzureQueried {
			name, _ = r.AttributeValues["name"].(string)
		}
	}
	if name == "" {
		t.Fatalf("the estate has no %s", sandboxAzureQueried)
	}
	checkQueryRoundTrip(ctx, t, queryRoundTrip{
		command:  "azure",
		scope:    []string{"--resource-group=" + s.resourceGroup},
		typ:      sandboxAzureQueried,
		identity: map[string]string{"subscription_id": s.subscriptionID, "resource_group_name": s.resourceGroup, "name": name},
		file:     "network.tf",
		plugins:  s.plugins,
	})
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

// checkAzureDrift does what a team does when someone changes a resource
// of an applied root outside Terraform: it applies root, in a copy of the
// output, as the read-only identity, as the apply only imports; adds a
// drift tag to the estate's network security group, as the fixture
// identity; and runs infraharvest drift, as the read-only identity. Drift
// must report the tag as kept, exit with 4 with --detailed-exitcode, and
// leave the code as it was; accepted, the tag must be written into the
// code, which then plans no change. Only the group, a resource of the
// estate, changes in the cloud; the estate's destroy deletes it as it is.
func (s *azureSandbox) checkAzureDrift(ctx context.Context, t *testing.T, estate *sandboxEstate, root string) {
	t.Helper()
	rel, err := filepath.Rel(s.out, root)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := copyOutput(s.out, work); err != nil {
		t.Fatalf("copy %s: %v", s.out, err)
	}
	applied := filepath.Join(work, rel)
	applyRoot(ctx, t, applied, s.execPath, s.plugins, nil)
	checkPlanClean(t, rel+": after the apply", planRoot(ctx, t, applied, s.execPath, s.plugins))

	estate.apply(ctx, t, tfexec.Var("drift_tag=reported"), tfexec.Target("azurerm_network_security_group.app"))
	drift := driftRun{ctx: ctx, engineName: s.engineName, execPath: s.execPath, out: work}
	code := codeFiles(t, work)
	d := drift.mustExit(t, report.ExitDrift, "--detailed-exitcode")
	checkCodeUnchanged(t, "drift", code, work)
	checkDriftChange(t, "drift", driftChange(t, "drift", d, "azurerm_network_security_group", "tags.drift"), "<nil>", "reported", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)

	d = drift.mustExit(t, report.ExitOK, "--accept", "type=azurerm_network_security_group")
	checkDriftChange(t, "drift --accept", driftChange(t, "drift --accept", d, "azurerm_network_security_group", "tags.drift"), "<nil>", "reported", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	if !codeMatches(t, work, `drift\s*=\s*"reported"`) {
		t.Errorf("drift --accept: the code doesn't have the group's tag drift = \"reported\"")
	}
	checkPlanClean(t, rel+": drift accepted", planRoot(ctx, t, applied, s.execPath, s.plugins))
}

// checkAzureAzapi imports the estate's resource group again, with
// --azapi, into an output of its own, as the read-only identity: the
// template spec, which azurerm has no resource type for, must be imported
// as an azapi_resource, by its ID with the newest stable API version, which
// the configuration's type pins too; nothing may fail or be left out,
// every check must pass, and the root must plan as imports with no changes.
// What azurerm has a type for, the role assignments and custom roles of the
// group among them, stays as the first import, before, imported it: each
// azurerm type as many times.
func (s *azureSandbox) checkAzureAzapi(ctx context.Context, t *testing.T, created []*tfjson.StateResource, before map[string]int) {
	t.Helper()
	var spec string
	for _, r := range created {
		if r.Type == "azapi_resource" && r.Name == "template_spec" {
			spec, _ = r.AttributeValues["id"].(string)
		}
	}
	if spec == "" {
		t.Fatal("the estate has no template spec")
	}
	out := t.TempDir()
	s.importAzure(ctx, t, out, "--azapi")
	coverage := readCoverage(t, out)
	checkAzureSecrets(t, out, created)
	for _, f := range coverage.Failures {
		t.Errorf("--azapi: failure: %+v", f)
	}
	var importedAs, importID string
	for _, d := range coverage.Directories {
		for _, l := range d.LeftOut {
			t.Errorf("--azapi: %s: %s (%s) left out: %v", d.Path, l.Address, l.ID, l.Errors)
		}
		for _, c := range d.Checks {
			if !c.Passed {
				t.Errorf("--azapi: %s: %s failed: %v", d.Path, c.Name, c.Details)
			}
		}
		for _, r := range d.Imported {
			if id, _, _ := strings.Cut(r.ID, "?"); strings.EqualFold(id, spec) {
				importedAs, importID = r.Address, r.ID
			}
		}
	}
	if !strings.HasPrefix(importedAs, "azapi_resource.") {
		t.Fatalf("--azapi: the template spec %s is imported as %q, want an azapi_resource", spec, importedAs)
	}
	_, version, ok := strings.Cut(importID, "?api-version=")
	if !ok || strings.Contains(version, "preview") {
		t.Errorf("--azapi: the template spec's import ID %q has no stable API version", importID)
	}
	dirs := generatedDirs(t, out)
	if len(dirs) != 1 {
		t.Fatalf("--azapi: roots %v, want one, for the resource group %s", dirs, s.resourceGroup)
	}
	root := filepath.Clean(dirs[0])
	if rejected, err := os.ReadFile(filepath.Join(root, rootfile.RejectedFileName)); err == nil {
		t.Errorf("--azapi: %s: resources left out:\n%s", root, rejected)
	}
	if generated := strings.ToLower(configuration(t, root)); !strings.Contains(generated, strings.ToLower("Microsoft.Resources/templateSpecs@"+version)) {
		t.Errorf("--azapi: the configuration doesn't pin the template spec's type to Microsoft.Resources/templateSpecs@%s", version)
	}
	imported := checkNoChanges(ctx, t, root, s.execPath, s.plugins, nil)
	if imported["azapi_resource"] == 0 {
		t.Errorf("--azapi: the root's plan imports no azapi_resource: %v", imported)
	}
	for _, typ := range sortedKeys(before) {
		if strings.HasPrefix(typ, "azurerm_") && imported[typ] != before[typ] {
			t.Errorf("--azapi: %s: imported %d, %d without --azapi", typ, imported[typ], before[typ])
		}
	}
}

// Role assignments refer to what the root has: the principal of the
// estate's identity, and a custom role the import lists.
var (
	principalReference  = regexp.MustCompile(`principal_id\s*=\s*azurerm_user_assigned_identity\.[a-z0-9_]+\.principal_id`)
	customRoleReference = regexp.MustCompile(`role_definition_id\s*=\s*azurerm_role_definition\.[a-z0-9_]+\.role_definition_resource_id`)
	builtInRoleByName   = regexp.MustCompile(`role_definition_name\s*=\s*"Reader"`)
)

// checkAzureRoles checks that the import imports the estate's custom role,
// by its GUID, which ends the role definition's ID in azurerm's import ID
// (the scope follows a |), and that the configuration refers the estate's two
// role assignments to its identity, the custom role's to the
// azurerm_role_definition, and the built-in role by name. The plan with
// no changes, which checkAzureImport checks, says they are the same.
func checkAzureRoles(t *testing.T, coverage report.Report, created []*tfjson.StateResource, root string) {
	t.Helper()
	var imported []string
	for _, d := range coverage.Directories {
		for _, r := range d.Imported {
			imported = append(imported, strings.ToLower(r.ID))
		}
	}
	assignments := 0
	for _, r := range created {
		switch r.Type {
		case "azurerm_role_assignment":
			assignments++
		case "azurerm_role_definition":
			id, _ := r.AttributeValues["role_definition_id"].(string)
			found := false
			for _, i := range imported {
				if id != "" && strings.Contains(i, "/roledefinitions/"+strings.ToLower(id)+"|") {
					found = true
				}
			}
			if !found {
				t.Errorf("the import doesn't import the custom role %s %q", r.Address, id)
			}
		}
	}
	if assignments < 2 {
		t.Fatalf("the estate made %d role assignments, want 2", assignments)
	}
	generated := configuration(t, root)
	if n := len(principalReference.FindAllString(generated, -1)); n < assignments {
		t.Errorf("%d role assignments refer to the identity's principal, want %d", n, assignments)
	}
	if !customRoleReference.MatchString(generated) {
		t.Error("no role assignment refers to the custom role's azurerm_role_definition")
	}
	if !builtInRoleByName.MatchString(generated) {
		t.Error(`no role assignment names the built-in role by name, role_definition_name = "Reader"`)
	}
}
