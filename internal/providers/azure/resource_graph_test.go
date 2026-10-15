// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/magodo/armid"
	"github.com/magodo/azlist/azlist"
	"github.com/magodo/aztft/aztft"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
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
	}, catalogue.MustBuiltin("azurerm"), func(id string) ([]aztft.Type, []string, bool, error) {
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
		id, ok := want[r.Address.Type]
		if !ok || r.Observed.ID != id {
			t.Errorf("%s %s: want %q", r.Address.Type, r.Observed.ID, id)
		}
		if r.Address.Type == "azurerm_virtual_network" {
			if r.RawName != "net1" || r.Observed.Attributes["tags.team"] != "net" {
				t.Errorf("network: name %q, attributes %v", r.RawName, r.Observed.Attributes)
			}
			if _, ok := r.Observed.Attributes["tags.cost"]; ok {
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
		if got := graphResources([]azlist.AzureResource{listed(t, id, nil)}, catalogue.MustBuiltin("azurerm"), query); len(got) != 0 {
			t.Errorf("%s: got %+v", name, got)
		}
	}
}

// * lists resource_graph and the services that list what Resource Graph
// can't. The services that list again what resource_graph lists are
// opt-in, so * never imports the same resource twice.
func TestWildcardListsResourceGraph(t *testing.T) {
	p := &Provider{}
	services := p.Services()
	optIn := p.OptInServices()
	for _, name := range optIn {
		if _, ok := services[name]; !ok {
			t.Errorf("opt-in service %s isn't a service", name)
		}
	}
	var wildcard []string
	for name := range services {
		if !slices.Contains(optIn, name) {
			wildcard = append(wildcard, name)
		}
	}
	slices.Sort(wildcard)
	want := []string{"management_lock", resourceGraphService, "security_center_contact", "security_center_subscription_pricing"}
	if !slices.Equal(wildcard, want) {
		t.Errorf("* lists %v, want %v", wildcard, want)
	}
}

// resource_graph lists the scope's resource groups from Resource Graph
// too, so that groups with no resources, which azlist doesn't add, are
// imported as well; a group azlist already added isn't listed twice,
// whatever the case of its ID.
func TestResourceGraphListsEmptyResourceGroups(t *testing.T) {
	const sub = "00000000-0000-0000-0000-000000000001"
	const (
		app   = "/subscriptions/" + sub + "/resourceGroups/rg-app"
		empty = "/subscriptions/" + sub + "/resourceGroups/rg-empty"
		nodes = "/subscriptions/" + sub + "/resourceGroups/MC_rg-aks_aks_eastus"
		aks   = "/subscriptions/" + sub + "/resourceGroups/rg-aks/providers/Microsoft.ContainerService/managedClusters/aks"
	)
	transport := &fakeTransport{body: `{"totalRecords":3,"count":3,"resultTruncated":"false","data":[
		{"id":"` + strings.ToUpper(app) + `","tags":{},"managedBy":""},
		{"id":"` + empty + `","tags":{"team":"platform"},"managedBy":""},
		{"id":"` + nodes + `","tags":{},"managedBy":"` + aks + `"},
		{"id":"not an ID"}
	]}`}
	options := &arm.ClientOptions{Transport: transport}
	predicate, err := graphPredicate(sub, "")
	if err != nil {
		t.Fatal(err)
	}
	groups, err := graphResourceGroups(t.Context(), fakeCredential{}, options, sub, predicate)
	if err != nil {
		t.Fatal(err)
	}
	if len(transport.paths) != 1 || !strings.HasSuffix(transport.paths[0], "/providers/Microsoft.ResourceGraph/resources") {
		t.Errorf("requests: %v", transport.paths)
	}
	if len(transport.sent) != 1 || !strings.Contains(transport.sent[0], "resourcecontainers") || !strings.Contains(transport.sent[0], predicate) {
		t.Errorf("query: %v, want the resource groups %s selects", transport.sent, predicate)
	}

	vnet := app + "/providers/Microsoft.Network/virtualNetworks/net1"
	found := withResourceGroups([]azlist.AzureResource{listed(t, app, nil), listed(t, vnet, nil)}, groups)
	resources := graphResources(found, catalogue.MustBuiltin("azurerm"), func(id string) ([]aztft.Type, []string, bool, error) {
		return aztft.QueryTypeAndId(id, nil)
	})
	got := map[string]string{}
	for _, r := range resources {
		got[r.Observed.ID] = r.Address.Type
		switch r.Observed.ID {
		case empty:
			if r.RawName != "rg-empty" || r.Observed.Attributes["tags.team"] != "platform" {
				t.Errorf("empty group: name %q, attributes %v", r.RawName, r.Observed.Attributes)
			}
		case nodes:
			if r.Observed.Attributes["managed_by"] != aks {
				t.Errorf("node resource group: attributes %v", r.Observed.Attributes)
			}
		}
	}
	want := map[string]string{
		app:   "azurerm_resource_group",
		vnet:  "azurerm_virtual_network",
		empty: "azurerm_resource_group",
		nodes: "azurerm_resource_group",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// With --resource-group, only that group is asked for.
func TestResourceGroupsQueryHonoursTheResourceGroup(t *testing.T) {
	predicate, err := graphPredicate("00000000-0000-0000-0000-000000000001", "rg1")
	if err != nil {
		t.Fatal(err)
	}
	const want = "resourcecontainers | where type =~ 'microsoft.resources/subscriptions/resourcegroups'" +
		" and subscriptionId =~ '00000000-0000-0000-0000-000000000001' and resourceGroup =~ 'rg1'" +
		" | project id, tags, managedBy | order by id asc"
	if got := resourceGroupsQuery(predicate); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestTagsFromResourceGraph(t *testing.T) {
	const sub = "00000000-0000-0000-0000-000000000001"
	transport := &fakeTransport{body: `{"totalRecords":2,"count":2,"resultTruncated":"false","data":[
		{"id":"/subscriptions/` + sub + `/resourceGroups/RG1/providers/Microsoft.Network/virtualNetworks/net1","tags":{"team":"net","n":1}},
		{"id":"/subscriptions/` + sub + `/resourceGroups/rg1","tags":{"env":"prod"}},
		{"id":"/subscriptions/` + sub + `/resourceGroups/rg1/providers/Microsoft.Storage/storageAccounts/sa","tags":{}}
	]}`}
	p := &Provider{
		subscriptionID: sub,
		resourceGroup:  "rg1",
		credential:     fakeCredential{},
		clientOptions:  &arm.ClientOptions{Transport: transport},
	}
	vnet := harvest.RecordBare("/subscriptions/"+sub+"/resourceGroups/rg1/providers/Microsoft.Network/virtualNetworks/net1", "net1", "azurerm_virtual_network", "azurerm")
	rg := harvest.RecordBare("/subscriptions/"+sub+"/resourceGroups/rg1", "rg1", "azurerm_resource_group", "azurerm")
	sa := harvest.RecordBare("/subscriptions/"+sub+"/resourceGroups/rg1/providers/Microsoft.Storage/storageAccounts/sa", "sa", "azurerm_storage_account", "azurerm")
	tags, err := p.Tags(t.Context(), []harvest.Resource{vnet, rg, sa})
	if err != nil {
		t.Fatal(err)
	}
	if got := tags["azurerm_virtual_network "+vnet.Observed.ID]; len(got) != 1 || got["team"] != "net" {
		t.Errorf("network tags, matched whatever the case: %v", got)
	}
	if got := tags["azurerm_resource_group "+rg.Observed.ID]; got["env"] != "prod" {
		t.Errorf("resource group tags: %v", got)
	}
	if _, ok := tags["azurerm_storage_account "+sa.Observed.ID]; ok {
		t.Error("an untagged account has tags")
	}
	if len(transport.paths) != 1 || !strings.HasSuffix(transport.paths[0], "/providers/Microsoft.ResourceGraph/resources") {
		t.Errorf("requests: %v", transport.paths)
	}
}

// The catalogue's inventory entries, built in or from --catalogue, map a
// resource before aztft: a type aztft doesn't know, or one it maps
// otherwise.
func TestGraphResourcesCatalogueOverridesAztft(t *testing.T) {
	dir := t.TempDir()
	overlay := `provider: azurerm
types:
  azurerm_network_intent_policy_example:
    inventory: { type: Microsoft.Network/networkIntentPolicies }
  azurerm_linux_web_app:
    inventory: { type: Microsoft.Web/sites, name: "/subscriptions/*/resourceGroups/*/providers/Microsoft.Web/sites/linux-*" }
`
	if err := os.WriteFile(filepath.Join(dir, "overrides.yaml"), []byte(overlay), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := catalogue.Load("azurerm", dir)
	if err != nil {
		t.Fatal(err)
	}
	const rg = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg1"
	intent := rg + "/providers/Microsoft.Network/networkIntentPolicies/policy1"
	linux := rg + "/providers/Microsoft.Web/sites/linux-app"
	other := rg + "/providers/Microsoft.Web/sites/app"
	var asked []string
	resources := graphResources([]azlist.AzureResource{listed(t, intent, nil), listed(t, linux, nil), listed(t, other, nil)}, cat, func(id string) ([]aztft.Type, []string, bool, error) {
		asked = append(asked, id)
		rid, _ := armid.ParseResourceId(id)
		return []aztft.Type{{AzureId: rid, TFType: "azurerm_windows_web_app"}}, []string{id}, true, nil
	})
	got := map[string]string{}
	for _, r := range resources {
		got[r.Observed.ID] = r.Address.Type
	}
	want := map[string]string{intent: "azurerm_network_intent_policy_example", linux: "azurerm_linux_web_app", other: "azurerm_windows_web_app"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if len(asked) != 1 || asked[0] != other {
		t.Errorf("asked aztft about %v, want only what the catalogue doesn't map", asked)
	}
}

// What Azure creates itself is recorded with what the built-in skip rules
// need, and they leave it out.
func TestGraphResourcesSkipsWhatAzureCreates(t *testing.T) {
	const sub = "/subscriptions/00000000-0000-0000-0000-000000000001"
	aks := sub + "/resourceGroups/rg-aks/providers/Microsoft.ContainerService/managedClusters/aks"
	nodes := sub + "/resourceGroups/MC_rg-aks_aks_eastus"
	watchers := sub + "/resourceGroups/NetworkWatcherRG"
	app := sub + "/resourceGroups/rg-app"
	listedResources := []azlist.AzureResource{
		listed(t, nodes, map[string]interface{}{"managedBy": aks}),
		listed(t, nodes+"/providers/Microsoft.Network/loadBalancers/kubernetes", nil),
		listed(t, watchers, nil),
		listed(t, sub+"/resourceGroups/networkwatcherrg/providers/Microsoft.Network/networkWatchers/NetworkWatcher_eastus", nil),
		listed(t, app, nil),
		listed(t, app+"/providers/Microsoft.Network/virtualNetworks/net1", nil),
		listed(t, sub+"/resourceGroups/DefaultResourceGroup-EUS/providers/Microsoft.OperationalInsights/workspaces/DefaultWorkspace-1-EUS", nil),
	}
	cat := catalogue.MustBuiltin("azurerm")
	resources := graphResources(listedResources, cat, func(id string) ([]aztft.Type, []string, bool, error) {
		return aztft.QueryTypeAndId(id, nil)
	})
	if len(resources) != len(listedResources) {
		t.Fatalf("got %d resources, want %d: %+v", len(resources), len(listedResources), resources)
	}
	for _, r := range resources {
		reason := cat.Skipped(r.Address.Type, r.Observed.ID, r.Observed.Attributes)
		kept := strings.HasPrefix(r.Observed.ID, app)
		if kept != (reason == "") {
			t.Errorf("%s %s (%v): skipped %q", r.Address.Type, r.Observed.ID, r.Observed.Attributes, reason)
		}
	}
	for _, r := range resources {
		a := r.Observed.Attributes
		switch r.Observed.ID {
		case nodes + "/providers/Microsoft.Network/loadBalancers/kubernetes":
			if a["name"] != "kubernetes" || a["resource_group_name"] != "MC_rg-aks_aks_eastus" || a["resource_group_managed_by"] != aks {
				t.Errorf("load balancer: %v", a)
			}
		case nodes:
			if a["managed_by"] != aks || a["resource_group_name"] != "MC_rg-aks_aks_eastus" {
				t.Errorf("node resource group: %v", a)
			}
		}
	}
}

// The tables and saved searches every Log Analytics workspace has built
// in are recorded with what tells them apart, and the built-in skip rules
// leave them out; a table whose plan or retention was changed, a custom
// table and a user's saved search are kept. Before the fix, one
// workspace in the Azure sandbox imported 697 built-in tables and 39
// LogManagement saved searches.
func TestGraphResourcesSkipsBuiltInWorkspaceTablesAndSearches(t *testing.T) {
	const workspace = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-logs/providers/Microsoft.OperationalInsights/workspaces/Logs"
	table := func(tableType, plan string, retentionAsDefault, totalAsDefault bool) map[string]interface{} {
		return map[string]interface{}{"properties": map[string]interface{}{
			"plan":                          plan,
			"retentionInDaysAsDefault":      retentionAsDefault,
			"totalRetentionInDaysAsDefault": totalAsDefault,
			"schema":                        map[string]interface{}{"tableType": tableType},
		}}
	}
	search := map[string]interface{}{"properties": map[string]interface{}{"category": "General Exploration"}}
	builtIn := map[string]bool{
		workspace + "/tables/AACAudit":                                          true,
		workspace + "/savedSearches/LogManagement(logs)_General|StaleComputers": true,
		workspace + "/tables/AppTraces":                                         false,
		workspace + "/tables/ContainerLogV2":                                    false,
		workspace + "/tables/Heartbeat":                                         false,
		workspace + "/tables/Orders_CL":                                         false,
		workspace + "/savedSearches/failed-sign-ins":                            false,
		workspace + "/savedSearches/Ops(other)_General|Errors":                  false,
	}
	listedResources := []azlist.AzureResource{
		listed(t, workspace+"/tables/AACAudit", table("Microsoft", "Analytics", true, true)),
		listed(t, workspace+"/tables/AppTraces", table("Microsoft", "Analytics", false, true)),
		listed(t, workspace+"/tables/ContainerLogV2", table("Microsoft", "Basic", true, true)),
		// Without the properties, the table can't be told apart: kept.
		listed(t, workspace+"/tables/Heartbeat", nil),
		listed(t, workspace+"/tables/Orders_CL", table("CustomLog", "Analytics", true, true)),
		listed(t, workspace+"/savedSearches/LogManagement(logs)_General|StaleComputers", search),
		listed(t, workspace+"/savedSearches/failed-sign-ins", search),
		// Another workspace's name: not this workspace's solution.
		listed(t, workspace+"/savedSearches/Ops(other)_General|Errors", search),
	}
	cat := catalogue.MustBuiltin("azurerm")
	resources := graphResources(listedResources, cat, func(id string) ([]aztft.Type, []string, bool, error) {
		rid, _ := armid.ParseResourceId(id)
		typ := "azurerm_log_analytics_saved_search"
		switch {
		case strings.HasSuffix(id, "_CL"):
			typ = "azurerm_log_analytics_workspace_table_custom_log"
		case strings.Contains(id, "/tables/"):
			typ = "azurerm_log_analytics_workspace_table_microsoft"
		}
		return []aztft.Type{{AzureId: rid, TFType: typ}}, []string{id}, true, nil
	})
	if len(resources) != len(listedResources) {
		t.Fatalf("got %d resources, want %d: %+v", len(resources), len(listedResources), resources)
	}
	for _, r := range resources {
		want, ok := builtIn[r.Observed.ID]
		if !ok {
			t.Errorf("unexpected %s %s", r.Address.Type, r.Observed.ID)
			continue
		}
		if reason := cat.Skipped(r.Address.Type, r.Observed.ID, r.Observed.Attributes); want != (reason != "") {
			t.Errorf("%s %s (%v): skipped %q, want skipped %v", r.Address.Type, r.Observed.ID, r.Observed.Attributes, reason, want)
		}
	}
	for _, r := range resources {
		a := r.Observed.Attributes
		switch r.Observed.ID {
		case workspace + "/tables/AppTraces":
			if a["table_type"] != "Microsoft" || a["plan"] != "Analytics" || a["retention_in_days_as_default"] != "false" || a["total_retention_in_days_as_default"] != "true" {
				t.Errorf("table: %v", a)
			}
		case workspace + "/savedSearches/LogManagement(logs)_General|StaleComputers":
			if a["solution"] != "LogManagement" || a["from_solution"] != "true" {
				t.Errorf("solution's saved search: %v", a)
			}
		case workspace + "/savedSearches/failed-sign-ins":
			if _, ok := a["solution"]; ok {
				t.Errorf("user's saved search: %v", a)
			}
		}
	}
}

func TestSavedSearchSolution(t *testing.T) {
	for _, tc := range []struct{ workspace, name, want string }{
		{"logs", "LogManagement(logs)_General|StaleComputers", "LogManagement"},
		{"Logs", "LogManagement(logs)_LogManagement|AllEvents", "LogManagement"},
		{"ih-sbx-terraform-lzoug5", "Security(ih-sbx-terraform-lzoug5)_SecurityAlert|Alerts", "Security"},
		{"logs", "failed-sign-ins", ""},
		{"logs", "LogManagement(other)_General|StaleComputers", ""},
		{"logs", "LogManagement(logs)_", ""},
		{"logs", "LogManagement(logs)", ""},
		{"logs", "(logs)_General|StaleComputers", ""},
		{"logs", "my search(logs)_x", ""},
	} {
		if got := savedSearchSolution(tc.workspace, tc.name); got != tc.want {
			t.Errorf("savedSearchSolution(%q, %q): got %q, want %q", tc.workspace, tc.name, got, tc.want)
		}
	}
}
