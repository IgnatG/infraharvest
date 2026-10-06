// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/synapse/armsynapse"
)

// fakeCredential hands out a token without signing in.
type fakeCredential struct{}

func (fakeCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

// fakeTransport answers every request with body and records the paths.
type fakeTransport struct {
	body  string
	paths []string
}

func (f *fakeTransport) Do(r *http.Request) (*http.Response, error) {
	f.paths = append(f.paths, r.URL.Path)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Request:    r,
	}, nil
}

func fakeArgs(transport *fakeTransport, resourceGroup string) map[string]interface{} {
	return map[string]interface{}{
		"subscription_id": "sub",
		"resource_group":  resourceGroup,
		"credential":      azcore.TokenCredential(fakeCredential{}),
		"client_options":  &arm.ClientOptions{ClientOptions: policy.ClientOptions{Transport: transport}},
	}
}

// --resource-group limits Redis caches to that resource group, as it does
// for the other listers.
func TestRedisListsTheResourceGroup(t *testing.T) {
	for resourceGroup, wantScope := range map[string]string{
		"rg1": "/subscriptions/sub/resourcegroups/rg1/providers/microsoft.cache/redis",
		"":    "/subscriptions/sub/providers/microsoft.cache/redis",
	} {
		transport := &fakeTransport{body: `{"value":[{"id":"/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Cache/Redis/cache1","name":"cache1"}]}`}
		g := &RedisGenerator{}
		g.Args = fakeArgs(transport, resourceGroup)

		if err := g.InitResources(); err != nil {
			t.Fatal(err)
		}

		if len(transport.paths) != 1 || strings.ToLower(transport.paths[0]) != wantScope {
			t.Errorf("resource group %q: requested %v, want %s", resourceGroup, transport.paths, wantScope)
		}
		if len(g.Resources) != 1 || g.Resources[0].InstanceInfo.Type != "azurerm_redis_cache" {
			t.Errorf("resource group %q: got %v", resourceGroup, g.Resources)
		}
	}
}

// Workspaces with a managed virtual network (always named "default") have
// managed private endpoints; workspaces without one are skipped.
func TestSynapseListsManagedPrivateEndpointsOfManagedNetworks(t *testing.T) {
	const endpointID = "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Synapse/workspaces/ws/managedVirtualNetworks/default/managedPrivateEndpoints/pe1"
	workspace := func(network string) *armsynapse.Workspace {
		return &armsynapse.Workspace{
			Name: to.Ptr("ws"),
			Properties: &armsynapse.WorkspaceProperties{
				ManagedVirtualNetwork: to.Ptr(network),
				ConnectivityEndpoints: map[string]*string{"dev": to.Ptr("https://ws.dev.azuresynapse.net")},
			},
		}
	}

	transport := &fakeTransport{body: `{"value":[{"id":"` + endpointID + `","name":"pe1"}]}`}
	g := &SynapseGenerator{}
	g.Args = fakeArgs(transport, "")
	if err := g.appendManagedPrivateEndpoint(workspace("default")); err != nil {
		t.Fatal(err)
	}
	if len(transport.paths) != 1 || transport.paths[0] != "/managedVirtualNetworks/default/managedPrivateEndpoints" {
		t.Errorf("requested %v", transport.paths)
	}
	if len(g.Resources) != 1 || g.Resources[0].InstanceState.ID != endpointID {
		t.Errorf("got %v, want %s", g.Resources, endpointID)
	}

	transport = &fakeTransport{body: `{"value":[]}`}
	g = &SynapseGenerator{}
	g.Args = fakeArgs(transport, "")
	if err := g.appendManagedPrivateEndpoint(workspace("")); err != nil {
		t.Fatal(err)
	}
	if len(transport.paths) != 0 || len(g.Resources) != 0 {
		t.Errorf("workspace without a managed network: requested %v, got %v", transport.paths, g.Resources)
	}
}
