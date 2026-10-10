// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/cmd"
	"github.com/IgnatG/infraharvest/report"
)

// azureMetadataHostEnv names the metadata host of floci-az, an Azure
// emulator, such as localhost:4577.
const azureMetadataHostEnv = "E2E_AZURE_METADATA_HOST"

// azureResourceGroup is the resource group testdata/azure creates its
// resources in.
const azureResourceGroup = "infraharvest-e2e"

// azureServices are the services testdata/azure covers.
var azureServices = []string{"keyvault", "network_interface", "network_security_group", "public_ip", "resource_group", "route_table", "storage_account", "subnet", "virtual_network"}

// TestAzureRoundTrip creates resources in floci-az, an Azure emulator,
// imports them with --engine=terraform (or E2E_ENGINE=tofu) and checks the
// engine plans every one as an import with no changes.
func TestAzureRoundTrip(t *testing.T) {
	host := os.Getenv(azureMetadataHostEnv)
	if host == "" {
		t.Skip(azureMetadataHostEnv + " is not set; start floci-az in e2e/compose.yaml")
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
	root := cmd.NewCmdRoot()
	root.SetArgs([]string{
		"import", "azure",
		"--engine=" + engineName,
		"--terraform-path=" + execPath,
		// floci-az lists network resources by resource group only.
		"--resource-group=" + azureResourceGroup,
		"--resources=" + strings.Join(azureServices, ","),
		"--path-output=" + out,
		"--all",
		"--allow-partial",
	})
	if err := root.ExecuteContext(ctx); cmd.ExitCode(err) != report.ExitOK && cmd.ExitCode(err) != report.ExitPartial {
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
	wantDir := filepath.Join(out, "azurerm", os.Getenv("ARM_SUBSCRIPTION_ID"), azureResourceGroup)
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
