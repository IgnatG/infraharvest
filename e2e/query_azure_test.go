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
)

// queryResourceGroup is the resource group testdata/azure-query creates
// its resources in, which no other test uses.
const queryResourceGroup = "infraharvest-e2e-query"

// TestAzureQuery creates an application security group in floci-az
// (testdata/azure-query) and takes it through the query service, which
// lists it through Terraform's list resources (see checkQueryRoundTrip).
// floci-az lists network resources by resource group only, so the
// service lists the group --resource-group names, which its list blocks
// then name.
func TestAzureQuery(t *testing.T) {
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
	_, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	seed(ctx, t, "azure-query", execPath, plugins)

	checkQueryRoundTrip(ctx, t, queryRoundTrip{
		command:  "azure",
		scope:    []string{"--resource-group=" + queryResourceGroup},
		typ:      "azurerm_application_security_group",
		identity: map[string]string{"resource_group_name": queryResourceGroup, "name": "infraharvest-e2e-query-web"},
		file:     "network.tf",
		plugins:  plugins,
	})
}
