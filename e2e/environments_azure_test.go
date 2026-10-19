// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || azure)

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// azureEnvironmentsConfig maps the subscriptions of
// testdata/azure-environments to dev and prod.
const azureEnvironmentsConfig = `version: 1
environments:
  dev:
    azure: { subscriptions: ["00000000-0000-0000-0000-000000000001"] }
  prod:
    azure: { subscriptions: ["00000000-0000-0000-0000-000000000003"] }
    tokens: [prd]
`

// TestAzureEnvironments imports testdata/azure-environments, a resource
// group of dev's subscription and one of prod's, each with a network
// security group, which the configuration file maps to environments by
// subscription: the roots of the two environments merge into one root
// they share, with the subscription a variable of each environment, which
// plans as each environment with no changes. floci-az keeps no tags, and
// has no Resource Graph to read them with, so environments are told apart
// by subscription only.
func TestAzureEnvironments(t *testing.T) {
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
	seed(ctx, t, "azure-environments", execPath, plugins)

	out := t.TempDir()
	root := cli.NewCmdRoot()
	root.SetArgs([]string{
		"import", "azure",
		"--config=" + writeConfig(t, azureEnvironmentsConfig),
		"--engine=" + engineName,
		"--terraform-path=" + execPath,
		"--subscriptions=00000000-0000-0000-0000-000000000001,00000000-0000-0000-0000-000000000003",
		"--resource-group=infraharvest-e2e-envs",
		"--services=network_security_group,resource_group",
		"--out=" + out,
		"--all",
		// The tags, which only Resource Graph reads, fail (see
		// azureEmulatorFailure).
		"--allow-partial",
	})
	if err := root.ExecuteContext(ctx); importer.ExitCode(err) != report.ExitOK && importer.ExitCode(err) != report.ExitPartial {
		t.Fatalf("infraharvest import azure: %v", err)
	}
	coverage := coverageOf(t, out)
	checkCoverage(t, "the import", coverage, azureEmulatorFailure)
	checkMerged(t, out, coverage, "azure/infraharvest-e2e-envs", "dev", "prod")
	imported := checkMergedPlans(ctx, t, filepath.Join(out, "azure", "infraharvest-e2e-envs"), execPath, plugins, nil, "dev", "prod")
	for _, env := range []string{"dev", "prod"} {
		if imported[env]["azurerm_network_security_group"] != 1 || imported[env]["azurerm_resource_group"] != 1 {
			t.Errorf("%s imports %v, want its resource group and network security group", env, imported[env])
		}
	}
}
