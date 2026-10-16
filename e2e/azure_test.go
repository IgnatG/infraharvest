// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || azure)

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// azureMetadataHostEnv names the metadata host of floci-az, an Azure
// emulator, such as localhost:4577.
const azureMetadataHostEnv = "E2E_AZURE_METADATA_HOST"

// azureResourceGroup is the resource group testdata/azure creates its
// resources in.
const azureResourceGroup = "infraharvest-e2e"

// azureServices are the services testdata/azure covers.
var azureServices = []string{"managed_identity", "network_interface", "network_security_group", "public_ip", "resource_group", "subnet", "virtual_network"}

// TestAzureRoundTrip creates resources in floci-az, an Azure emulator,
// imports them with --engine=terraform (or E2E_ENGINE=tofu) and checks the
// engine plans every one as an import with no changes.
func TestAzureRoundTrip(t *testing.T) {
	host := os.Getenv(azureMetadataHostEnv)
	if host == "" {
		skipUnconfigured(t, azureMetadataHostEnv+" is not set; start floci-az in e2e/compose.yaml")
	}
	if !isLoopback("https://" + host) {
		t.Fatalf("%s=%s: the test creates resources, so it only runs against a local emulator", azureMetadataHostEnv, host)
	}
	isolateAzureConfig(t, host)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")

	state := seed(ctx, t, "azure", execPath, plugins)

	out := t.TempDir()
	root := cli.NewCmdRoot()
	root.SetArgs([]string{
		"import", "azure",
		"--engine=" + engineName,
		"--terraform-path=" + execPath,
		// floci-az lists network resources by resource group only.
		"--resource-group=" + azureResourceGroup,
		"--services=" + strings.Join(azureServices, ","),
		"--out=" + out,
		"--all",
		"--allow-partial",
	})
	if err := root.ExecuteContext(ctx); importer.ExitCode(err) != report.ExitOK && importer.ExitCode(err) != report.ExitPartial {
		t.Fatalf("infraharvest import azure: %v", err)
	}

	var coverage report.Report
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(out, report.Dir, "coverage.json"))), &coverage); err != nil {
		t.Fatal(err)
	}
	if coverage.Totals.LeftOut != 0 || coverage.Totals.Failed != 0 || len(coverage.Failures) != 0 {
		t.Errorf("coverage.json: %+v, failures %v", coverage.Totals, coverage.Failures)
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

	imported := map[string]int{}
	// One root, for the subscription and the resource group.
	wantDir := filepath.Join(out, "azure", os.Getenv("ARM_SUBSCRIPTION_ID"), azureResourceGroup)
	checkAzureTags(t, wantDir)
	for _, dir := range generatedDirs(t, out) {
		if filepath.Clean(dir) != wantDir {
			t.Errorf("root %s, want %s", dir, wantDir)
		}
		for typ, n := range checkNoChanges(ctx, t, dir, execPath, plugins, nil) {
			imported[typ] += n
		}
	}
	seeded := map[string]int{}
	for _, r := range state {
		seeded[r.Type]++
	}
	for _, typ := range sortedKeys(seeded) {
		if imported[typ] < seeded[typ] {
			t.Errorf("%s: created %d, imported %d", typ, seeded[typ], imported[typ])
		}
	}
	// The identity, with its federated credential, becomes a call of Azure
	// Verified Modules' managed identity module when the module accepts
	// azurerm's newest major release; when it doesn't, manifest.json says
	// the module isn't called, and both stay in the root, on the newest
	// azurerm, without the module's azapi provider. The plans above check
	// that either changes nothing.
	var manifest report.Manifest
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(out, report.Dir, "manifest.json"))), &manifest); err != nil {
		t.Fatal(err)
	}
	unused := slices.ContainsFunc(manifest.Unused, func(m report.UnusedModule) bool {
		return m.Source == adapters.AzureUserAssignedIdentity.Source
	})
	var members []string
	for _, d := range coverage.Directories {
		for _, m := range d.Modules {
			if m.Source == adapters.AzureUserAssignedIdentity.Source && m.Declined == "" {
				members = append(members, m.Resources...)
			}
		}
	}
	if unused {
		if len(members) > 0 {
			t.Errorf("%s isn't called, yet has %v", adapters.AzureUserAssignedIdentity.Source, members)
		}
		if versions := readFile(t, filepath.Join(wantDir, engine.VersionsFileName)); strings.Contains(versions, "azapi") {
			t.Errorf("no module needs azapi, yet %s requires it:\n%s", engine.VersionsFileName, versions)
		}
		return
	}
	for _, typ := range []string{"azurerm_user_assigned_identity", "azurerm_federated_identity_credential"} {
		if !slices.ContainsFunc(members, func(address string) bool { return strings.HasPrefix(address, typ+".") }) {
			t.Errorf("no %s in a call of %s; modules:", typ, adapters.AzureUserAssignedIdentity.Source)
			for _, d := range coverage.Directories {
				for _, m := range d.Modules {
					t.Logf("%s: %+v", d.Path, m)
				}
			}
		}
	}
}

// checkAzureTags checks the tags the fixture's resources share became
// local.tags: the resources refer to it, and so does the identity, or its
// module call, merged with the identity's own tag.
func checkAzureTags(t *testing.T, dir string) {
	t.Helper()
	if locals := strings.Join(strings.Fields(readFile(t, filepath.Join(dir, engine.LocalsFileName))), " "); !strings.Contains(locals, `env = "test"`) || !strings.Contains(locals, `project = "infraharvest-e2e"`) {
		t.Errorf("%s doesn't have the shared tags:\n%s", engine.LocalsFileName, locals)
	}
	generated := configuration(t, dir)
	// The virtual network, network security group, public IP and network
	// interface: floci-az doesn't keep a resource group's tags.
	if strings.Count(generated, "= local.tags\n") < 4 {
		t.Errorf("the resources don't refer to local.tags:\n%s", generated)
	}
	if !strings.Contains(strings.Join(strings.Fields(generated), " "), `tags = merge(local.tags, { role = "ci" })`) {
		t.Errorf("the identity doesn't merge local.tags:\n%s", generated)
	}
}

// isolateAzureConfig points the listers and the azurerm provider at the
// emulator through its metadata host (ARM_ENVIRONMENT=stack), signed in
// through the emulator's managed identity endpoint: the test never uses a
// real subscription. Not with a service principal: floci-az advertises an
// http issuer over https (floci-io/floci-az#255), which azidentity rejects.
func isolateAzureConfig(t *testing.T, host string) {
	t.Helper()
	for k, v := range map[string]string{
		"ARM_ENVIRONMENT":       "stack",
		"ARM_METADATA_HOSTNAME": host,
		"ARM_SUBSCRIPTION_ID":   "00000000-0000-0000-0000-000000000001",
		"ARM_TENANT_ID":         "00000000-0000-0000-0000-000000000002",
		"ARM_USE_MSI":           "true",
		"ARM_MSI_ENDPOINT":      "https://" + host + "/metadata/identity/oauth2/token",
		// The system-assigned identity: a client ID would ask for a
		// user-assigned one.
		"ARM_CLIENT_ID":                       "",
		"ARM_CLIENT_SECRET":                   "",
		"ARM_CLIENT_CERTIFICATE_PATH":         "",
		"ARM_USE_OIDC":                        "",
		"ARM_USE_CLI":                         "false",
		"ARM_AUXILIARY_TENANT_IDS":            "",
		"ARM_RESOURCE_PROVIDER_REGISTRATIONS": "none",
	} {
		t.Setenv(k, v)
	}
}
