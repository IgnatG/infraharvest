// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"errors"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/magodo/armid"
	"github.com/magodo/azlist/azlist"
	"github.com/magodo/aztft/aztft"

	"github.com/IgnatG/infraharvest/terraformutils"
)

func TestGraphPredicate(t *testing.T) {
	sub := "00000000-0000-0000-0000-000000000001"
	for _, tc := range []struct {
		subscription, resourceGroup, want string
		ok                                bool
	}{
		{sub, "", "subscriptionId =~ '" + sub + "'", true},
		{sub, "rg-app_1.(eu)", "subscriptionId =~ '" + sub + "' and resourceGroup =~ 'rg-app_1.(eu)'", true},
		{sub, "rg' or 1==1 or '", "", false},
		{"sub' or '", "", "", false},
	} {
		got, err := graphPredicate(tc.subscription, tc.resourceGroup)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("%q %q: got %q, %v", tc.subscription, tc.resourceGroup, got, err)
		}
	}
}

func listed(t *testing.T, id string, properties map[string]interface{}) azlist.AzureResource {
	t.Helper()
	rid, err := armid.ParseResourceId(id)
	if err != nil {
		t.Fatal(err)
	}
	return azlist.AzureResource{Id: rid, Properties: properties}
}

// aztft's own mappings, without asking Resource Manager, give the azurerm
// types and import IDs of resources an ID says enough about.
func TestGraphResourcesMapsTypes(t *testing.T) {
	const rg = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg1"
	vnet := rg + "/providers/Microsoft.Network/virtualNetworks/net1"
	resources := graphResources([]azlist.AzureResource{
		listed(t, rg, nil),
		listed(t, vnet, map[string]interface{}{"tags": map[string]interface{}{"team": "net", "cost": 3}}),
		listed(t, vnet+"/subnets/web", nil),
	}, func(id string) ([]aztft.Type, []string, bool, error) {
		return aztft.QueryTypeAndId(id, nil)
	})

	want := map[string]string{
		"azurerm_resource_group":  rg,
		"azurerm_virtual_network": vnet,
		"azurerm_subnet":          vnet + "/subnets/web",
	}
	if len(resources) != len(want) {
		t.Fatalf("got %d resources: %+v", len(resources), resources)
	}
	for _, r := range resources {
		id, ok := want[r.InstanceInfo.Type]
		if !ok || r.InstanceState.ID != id {
			t.Errorf("%s %s: want %q", r.InstanceInfo.Type, r.InstanceState.ID, id)
		}
		if r.InstanceInfo.Type == "azurerm_virtual_network" {
			if r.RawName != "net1" || r.InstanceState.Attributes["tags.team"] != "net" {
				t.Errorf("network: name %q, attributes %v", r.RawName, r.InstanceState.Attributes)
			}
			if _, ok := r.InstanceState.Attributes["tags.cost"]; ok {
				t.Error("a tag that isn't a string was recorded")
			}
		}
	}
}

// What aztft can't map, or can't choose a type for, is left out.
func TestGraphResourcesLeavesOutUnmapped(t *testing.T) {
	const id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg1/providers/Microsoft.Web/sites/app"
	for name, query := range map[string]queryType{
		"error": func(string) ([]aztft.Type, []string, bool, error) { return nil, nil, false, errors.New("unknown") },
		"none":  func(string) ([]aztft.Type, []string, bool, error) { return nil, nil, true, nil },
		"several": func(string) ([]aztft.Type, []string, bool, error) {
			rid, _ := armid.ParseResourceId(id)
			return []aztft.Type{{AzureId: rid, TFType: "azurerm_linux_web_app"}, {AzureId: rid, TFType: "azurerm_windows_web_app"}}, []string{id, id}, false, nil
		},
	} {
		if got := graphResources([]azlist.AzureResource{listed(t, id, nil)}, query); len(got) != 0 {
			t.Errorf("%s: got %+v", name, got)
		}
	}
}

func TestResourceGraphIsOptIn(t *testing.T) {
	p := &AzureProvider{}
	if _, ok := p.GetSupportedService()[resourceGraphService]; !ok {
		t.Fatal("resource_graph isn't a service")
	}
	if got := p.OptInServices(); len(got) != 1 || got[0] != resourceGraphService {
		t.Errorf("opt-in services: %v", got)
	}
}

func TestTagsFromResourceGraph(t *testing.T) {
	const sub = "00000000-0000-0000-0000-000000000001"
	transport := &fakeTransport{body: `{"totalRecords":2,"count":2,"resultTruncated":"false","data":[
		{"id":"/subscriptions/` + sub + `/resourceGroups/RG1/providers/Microsoft.Network/virtualNetworks/net1","tags":{"team":"net","n":1}},
		{"id":"/subscriptions/` + sub + `/resourceGroups/rg1","tags":{"env":"prod"}},
		{"id":"/subscriptions/` + sub + `/resourceGroups/rg1/providers/Microsoft.Storage/storageAccounts/sa","tags":{}}
	]}`}
	p := &AzureProvider{
		subscriptionID: sub,
		resourceGroup:  "rg1",
		credential:     fakeCredential{},
		clientOptions:  &arm.ClientOptions{ClientOptions: policy.ClientOptions{Transport: transport}},
	}
	vnet := terraformutils.NewSimpleResource("/subscriptions/"+sub+"/resourceGroups/rg1/providers/Microsoft.Network/virtualNetworks/net1", "net1", "azurerm_virtual_network", "azurerm")
	rg := terraformutils.NewSimpleResource("/subscriptions/"+sub+"/resourceGroups/rg1", "rg1", "azurerm_resource_group", "azurerm")
	sa := terraformutils.NewSimpleResource("/subscriptions/"+sub+"/resourceGroups/rg1/providers/Microsoft.Storage/storageAccounts/sa", "sa", "azurerm_storage_account", "azurerm")
	tags, err := p.Tags(t.Context(), []terraformutils.Resource{vnet, rg, sa})
	if err != nil {
		t.Fatal(err)
	}
	if got := tags["azurerm_virtual_network "+vnet.InstanceState.ID]; len(got) != 1 || got["team"] != "net" {
		t.Errorf("network tags, matched whatever the case: %v", got)
	}
	if got := tags["azurerm_resource_group "+rg.InstanceState.ID]; got["env"] != "prod" {
		t.Errorf("resource group tags: %v", got)
	}
	if _, ok := tags["azurerm_storage_account "+sa.InstanceState.ID]; ok {
		t.Error("an untagged account has tags")
	}
	if len(transport.paths) != 1 || !strings.HasSuffix(transport.paths[0], "/providers/Microsoft.ResourceGraph/resources") {
		t.Errorf("requests: %v", transport.paths)
	}
}
