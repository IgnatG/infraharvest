// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/zclconf/go-cty/cty"
)

// azapi gets the root's subscription, and a custom cloud's endpoints as
// overrides of the public cloud's.
func TestModuleProvidersConfigureAzapi(t *testing.T) {
	t.Parallel()
	p := &Provider{subscriptionID: "sub-1"}
	azapi := p.ModuleProviders()["azapi"]
	if azapi.Source != "Azure/azapi" || len(azapi.Config) != 1 || azapi.Config["subscription_id"] != "sub-1" {
		t.Errorf("Azure's own cloud: %+v", azapi)
	}

	p.customCloud = &cloud.Configuration{
		ActiveDirectoryAuthorityHost: "https://login.example/",
		Services:                     map[cloud.ServiceName]cloud.ServiceConfiguration{cloud.ResourceManager: {Endpoint: "https://arm.example/", Audience: "https://arm.example/"}},
	}
	azapi = p.ModuleProviders()["azapi"]
	if azapi.Config["environment"] != "public" {
		t.Errorf("environment: %v", azapi.Config["environment"])
	}
	endpoint, ok := azapi.Config["endpoint"].(cty.Value)
	if !ok || endpoint.LengthInt() != 1 {
		t.Fatalf("endpoint: %#v", azapi.Config["endpoint"])
	}
	e := endpoint.Index(cty.NumberIntVal(0))
	if e.GetAttr("resource_manager_endpoint").AsString() != "https://arm.example/" || e.GetAttr("active_directory_authority_host").AsString() != "https://login.example/" {
		t.Errorf("endpoint: %#v", e)
	}
}
