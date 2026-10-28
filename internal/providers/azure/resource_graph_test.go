// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/magodo/armid"
	"github.com/magodo/azlist/azlist"
	"github.com/magodo/aztft/aztft"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

func TestGraphPredicate(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	const rg = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg1"
	vnet := rg + "/providers/Microsoft.Network/virtualNetworks/net1"
	resources := graphResources([]azlist.AzureResource{
		listed(t, rg, nil),
		listed(t, vnet, map[string]interface{}{"tags": map[string]interface{}{"team": "net", "cost": 3}}),
		listed(t, vnet+"/subnets/web", nil),
	}, catalogue.MustBuiltin("azurerm"), func(id string) ([]aztft.Type, []string, bool, error) {
		return aztft.QueryTypeAndId(id, nil)
	}, nil)

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

// What aztft can't map, or can't choose a type for, or maps to a type
// azurerm doesn't have, is left out.
func TestGraphResourcesLeavesOutUnmapped(t *testing.T) {
	t.Parallel()
	const id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg1/providers/Microsoft.Web/sites/app"
	for name, query := range map[string]queryType{
		"error": func(string) ([]aztft.Type, []string, bool, error) { return nil, nil, false, errors.New("unknown") },
		"none":  func(string) ([]aztft.Type, []string, bool, error) { return nil, nil, true, nil },
		"several": func(string) ([]aztft.Type, []string, bool, error) {
			rid, _ := armid.ParseResourceId(id)
			return []aztft.Type{{AzureId: rid, TFType: "azurerm_linux_web_app"}, {AzureId: rid, TFType: "azurerm_windows_web_app"}}, []string{id, id}, false, nil
		},
		"missing": func(string) ([]aztft.Type, []string, bool, error) {
			rid, _ := armid.ParseResourceId(id)
			return []aztft.Type{{AzureId: rid, TFType: "azurerm_postgresql_active_directory_administrator"}}, []string{id}, true, nil
		},
	} {
		if got := graphResources([]azlist.AzureResource{listed(t, id, nil)}, catalogue.MustBuiltin("azurerm"), query, nil); len(got) != 0 {
			t.Errorf("%s: got %+v", name, got)
		}
		// With --azapi, only what azurerm has no type for at all, or only
		// one its release line lacks, comes through azapi: not what aztft
		// fails on or can't choose for.
		viaAzapi := azapiCandidates([]azlist.AzureResource{listed(t, id, nil)})
		got := graphResources([]azlist.AzureResource{listed(t, id, nil)}, catalogue.MustBuiltin("azurerm"), query, viaAzapi)
		if want := map[string]int{"none": 1, "missing": 1}[name]; len(got) != want {
			t.Errorf("%s with --azapi: got %+v", name, got)
		}
	}
}

// With --azapi, what Resource Graph returned that azurerm has no type for
// comes through azapi_resource, imported by its Azure ID, named and with
// the attributes as any other; a child resource Resource Manager listed
// under it is left out, as without --azapi, as is everything without it.
func TestGraphResourcesThroughAzapi(t *testing.T) {
	t.Parallel()
	const rg = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg1"
	fleet := rg + "/providers/Microsoft.AzureFleet/fleets/batch"
	rows := []azlist.AzureResource{
		listed(t, rg, nil),
		listed(t, fleet, map[string]interface{}{"tags": map[string]interface{}{"team": "batch"}}),
	}
	found := append(slices.Clone(rows), listed(t, fleet+"/virtualMachineScaleSets/pool", nil))
	query := func(id string) ([]aztft.Type, []string, bool, error) {
		return aztft.QueryTypeAndId(id, nil)
	}

	resources := graphResources(found, catalogue.MustBuiltin("azurerm"), query, azapiCandidates(rows))
	byType := map[string]harvest.Resource{}
	for _, r := range resources {
		byType[r.Address.Type] = r
	}
	if len(resources) != 2 {
		t.Fatalf("got %d resources, want the group and the fleet: %+v", len(resources), resources)
	}
	if _, ok := byType["azurerm_resource_group"]; !ok {
		t.Errorf("no resource group: %+v", resources)
	}
	r, ok := byType[azapiType]
	if !ok {
		t.Fatalf("no %s: %+v", azapiType, resources)
	}
	if r.Observed.ID != fleet || r.RawName != "batch" || r.Provider != "azurerm" {
		t.Errorf("fleet: ID %q, name %q, provider %q", r.Observed.ID, r.RawName, r.Provider)
	}
	if r.Observed.Attributes["tags.team"] != "batch" || r.Observed.Attributes["resource_group_name"] != "rg1" {
		t.Errorf("fleet attributes: %v", r.Observed.Attributes)
	}
	if n := countType(resources, azapiType); n != 1 {
		t.Errorf("countType: %d", n)
	}

	if got := graphResources(found, catalogue.MustBuiltin("azurerm"), query, nil); countType(got, azapiType) != 0 || len(got) != 1 {
		t.Errorf("without --azapi: got %+v", got)
	}
}

// * lists resource_graph and the services that list what Resource Graph
// can't. The services that list again what resource_graph lists are
// opt-in, so * never imports the same resource twice, and so is
// diagnostic_setting, which takes a request per resource.
func TestWildcardListsResourceGraph(t *testing.T) {
	t.Parallel()
	p := &Provider{}
	services := p.Services()
	optIn := p.Profile().OptInServices
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
	want := []string{"management_lock", resourceGraphService, "role_assignment", "security_center_contact", "security_center_subscription_pricing"}
	if !slices.Equal(wildcard, want) {
		t.Errorf("* lists %v, want %v", wildcard, want)
	}
}

// resource_graph lists the scope's resource groups from Resource Graph
// too, so that groups with no resources, which azlist doesn't add, are
// imported as well; a group azlist already added isn't listed twice,
// whatever the case of its ID.
func TestResourceGraphListsEmptyResourceGroups(t *testing.T) {
	t.Parallel()
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
	}, nil)
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
	t.Parallel()
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

// The catalogue's inventory entries, built in or from --catalogue, map a
// resource before aztft: a type aztft doesn't know, or one it maps
// otherwise.
func TestGraphResourcesCatalogueOverridesAztft(t *testing.T) {
	t.Parallel()
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
	}, nil)
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
	t.Parallel()
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
		listed(t, sub+"/resourceGroups/AzureBackupRG_eastus_1", nil),
	}
	cat := catalogue.MustBuiltin("azurerm")
	resources := graphResources(listedResources, cat, func(id string) ([]aztft.Type, []string, bool, error) {
		return aztft.QueryTypeAndId(id, nil)
	}, nil)
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
	// What Azure Backup puts in its group, and what Defender for Cloud,
	// Azure Policy and AKS add to a user's groups, by the names they give.
	for _, tc := range []struct{ typ, id string }{
		{"azurerm_virtual_machine_restore_point_collection", sub + "/resourceGroups/AzureBackupRG_eastus_1/providers/Microsoft.Compute/restorePointCollections/AzureBackup_vm1_123"},
		{"azurerm_virtual_machine_extension", app + "/providers/Microsoft.Compute/virtualMachines/vm1/extensions/MDE.Linux"},
		{"azurerm_virtual_machine_extension", app + "/providers/Microsoft.Compute/virtualMachines/vm1/extensions/AzurePolicyforWindows"},
		{"azurerm_monitor_data_collection_rule", app + "/providers/Microsoft.Insights/dataCollectionRules/MSCI-eastus-aks"},
		{"azurerm_monitor_data_collection_endpoint", app + "/providers/Microsoft.Insights/dataCollectionEndpoints/MSProm-eastus-aks"},
		{"azurerm_monitor_alert_prometheus_rule_group", app + "/providers/Microsoft.AlertsManagement/prometheusRuleGroups/KubernetesRecordingRulesRuleGroup-aks"},
		{"azurerm_log_analytics_solution", app + "/providers/Microsoft.OperationsManagement/solutions/SecurityCenterFree(logs)"},
		{"azurerm_log_analytics_solution", app + "/providers/Microsoft.OperationsManagement/solutions/ContainerInsights(logs)"},
	} {
		rid, err := armid.ParseResourceId(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		a := graphAttributes(azlist.AzureResource{Id: rid}, listedOwners{})
		if reason := cat.Skipped(tc.typ, tc.id, a); reason == "" {
			t.Errorf("%s %s (%v): not skipped", tc.typ, tc.id, a)
		}
	}
	// A user's flow logs and connection monitors in NetworkWatcherRG are
	// kept, and so are their children listed.
	watcher := watchers + "/providers/Microsoft.Network/networkWatchers/NetworkWatcher_eastus"
	for _, tc := range []struct{ typ, id string }{
		{"azurerm_network_watcher_flow_log", watcher + "/flowLogs/vnet-flows"},
		{"azurerm_network_connection_monitor", watcher + "/connectionMonitors/app-latency"},
	} {
		rid, err := armid.ParseResourceId(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		a := graphAttributes(azlist.AzureResource{Id: rid}, listedOwners{})
		if reason := cat.Skipped(tc.typ, tc.id, a); reason != "" {
			t.Errorf("%s %s (%v): skipped %q", tc.typ, tc.id, a, reason)
		}
	}
	if inSkippedGroup(listed(t, watcher, nil), nil, cat) {
		t.Error("a network watcher's flow logs and connection monitors aren't listed")
	}
}

// A disk's managedBy is the virtual machine it is attached to. The
// machine's OS disk, which its os_disk block manages, is left out, told
// apart by the machine's storageProfile, or by the disk's osType when the
// machine isn't listed; a data disk, which the machine's data disk
// attachment needs, is kept. Before, azlist dropped every attached disk.
func TestGraphResourcesKeepsAttachedDataDisks(t *testing.T) {
	t.Parallel()
	const rg = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app"
	vm := rg + "/providers/Microsoft.Compute/virtualMachines/vm1"
	other := rg + "/providers/Microsoft.Compute/virtualMachines/vm2"
	disks := rg + "/providers/Microsoft.Compute/disks/"
	disk := func(by, osType string) map[string]interface{} {
		properties := map[string]interface{}{}
		if osType != "" {
			properties["osType"] = osType
		}
		return map[string]interface{}{"managedBy": by, "properties": properties}
	}
	listedResources := []azlist.AzureResource{
		listed(t, vm, map[string]interface{}{"properties": map[string]interface{}{"storageProfile": map[string]interface{}{
			"osDisk": map[string]interface{}{"managedDisk": map[string]interface{}{"id": strings.ToUpper(disks + "vm1-os")}},
		}}}),
		listed(t, disks+"vm1-os", disk(vm, "Linux")),
		// A data disk made from an OS disk's snapshot keeps its osType.
		listed(t, disks+"vm1-data", disk(vm, "Linux")),
		// vm2 isn't listed: the osType tells.
		listed(t, disks+"vm2-os", disk(other, "Windows")),
		listed(t, disks+"vm2-data", disk(other, "")),
		listed(t, disks+"spare", disk("", "")),
	}
	cat := catalogue.MustBuiltin("azurerm")
	resources := graphResources(listedResources, cat, func(id string) ([]aztft.Type, []string, bool, error) {
		rid, _ := armid.ParseResourceId(id)
		typ := "azurerm_managed_disk"
		if strings.Contains(id, "/virtualMachines/") {
			typ = "azurerm_linux_virtual_machine"
		}
		return []aztft.Type{{AzureId: rid, TFType: typ}}, []string{id}, true, nil
	}, nil)
	if len(resources) != len(listedResources) {
		t.Fatalf("got %d resources, want %d: %+v", len(resources), len(listedResources), resources)
	}
	skipped := map[string]bool{disks + "vm1-os": true, disks + "vm2-os": true}
	for _, r := range resources {
		reason := cat.Skipped(r.Address.Type, r.Observed.ID, r.Observed.Attributes)
		if skipped[r.Observed.ID] != (reason != "") {
			t.Errorf("%s (%v): skipped %q", r.Observed.ID, r.Observed.Attributes, reason)
		}
		a := r.Observed.Attributes
		switch r.Observed.ID {
		case disks + "vm1-os":
			if a["os_disk_of"] != vm || a["managed_by"] != vm {
				t.Errorf("OS disk: %v", a)
			}
		case disks + "vm1-data":
			if _, ok := a["managed_by"]; ok || a["attached_to"] != vm {
				t.Errorf("data disk: %v", a)
			}
		}
	}
}

// The network interface of a private endpoint or a Private Link service
// is recorded with it and left out: the endpoint or service creates and
// deletes it. A virtual machine's interface is kept.
func TestGraphResourcesSkipsPrivateEndpointInterfaces(t *testing.T) {
	t.Parallel()
	const network = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Network"
	endpoint := network + "/privateEndpoints/pe-blob"
	service := network + "/privateLinkServices/pls-app"
	nic := func(property, id string) map[string]interface{} {
		properties := map[string]interface{}{"ipConfigurations": []interface{}{}}
		if property != "" {
			properties[property] = map[string]interface{}{"id": id}
		}
		return map[string]interface{}{"properties": properties}
	}
	listedResources := []azlist.AzureResource{
		listed(t, network+"/networkInterfaces/pe-blob.nic.1", nic("privateEndpoint", endpoint)),
		listed(t, network+"/networkInterfaces/pls-app.nic.1", nic("privateLinkService", service)),
		listed(t, network+"/networkInterfaces/vm1-nic", nic("", "")),
	}
	cat := catalogue.MustBuiltin("azurerm")
	resources := graphResources(listedResources, cat, func(id string) ([]aztft.Type, []string, bool, error) {
		rid, _ := armid.ParseResourceId(id)
		return []aztft.Type{{AzureId: rid, TFType: "azurerm_network_interface"}}, []string{id}, true, nil
	}, nil)
	want := map[string]map[string]string{
		network + "/networkInterfaces/pe-blob.nic.1": {"private_endpoint": endpoint},
		network + "/networkInterfaces/pls-app.nic.1": {"private_link_service": service},
		network + "/networkInterfaces/vm1-nic":       nil,
	}
	if len(resources) != len(want) {
		t.Fatalf("got %d resources, want %d: %+v", len(resources), len(want), resources)
	}
	for _, r := range resources {
		owners, ok := want[r.Observed.ID]
		if !ok {
			t.Errorf("unexpected %s", r.Observed.ID)
			continue
		}
		for k, v := range owners {
			if r.Observed.Attributes[k] != v {
				t.Errorf("%s: %s = %q, want %q", r.Observed.ID, k, r.Observed.Attributes[k], v)
			}
		}
		if reason := cat.Skipped(r.Address.Type, r.Observed.ID, r.Observed.Attributes); (owners != nil) != (reason != "") {
			t.Errorf("%s (%v): skipped %q", r.Observed.ID, r.Observed.Attributes, reason)
		}
	}
}

// What is in a group the built-in rules leave out whole is listed without
// its child resources; what is in any other group, or is left out for
// itself alone, has them listed.
func TestInSkippedGroup(t *testing.T) {
	t.Parallel()
	const sub = "/subscriptions/00000000-0000-0000-0000-000000000001"
	aks := sub + "/resourceGroups/rg-aks/providers/Microsoft.ContainerService/managedClusters/aks"
	nodes := sub + "/resourceGroups/MC_rg-aks_aks_eastus"
	managed := map[string]string{strings.ToUpper(nodes): aks}
	cat := catalogue.MustBuiltin("azurerm")
	for id, want := range map[string]bool{
		nodes + "/providers/Microsoft.Network/virtualNetworks/aks-vnet":                                                      true,
		sub + "/resourceGroups/DefaultResourceGroup-EUS/providers/Microsoft.OperationalInsights/workspaces/DefaultWorkspace": true,
		sub + "/resourceGroups/rg-app/providers/Microsoft.Network/virtualNetworks/net1":                                      false,
		// Left out by its name, not its group: its children are listed.
		sub + "/resourceGroups/rg-app/providers/Microsoft.Insights/actionGroups/Application Insights Smart Detection": false,
		sub + "/resourceGroups/rg-app": false,
	} {
		if got := inSkippedGroup(listed(t, id, nil), managed, cat); got != want {
			t.Errorf("%s: got %v, want %v", id, got, want)
		}
	}
}

// A child list that was refused, throttled or failed by Resource Manager,
// or got no answer, fails the service in part, as resources were left
// out, and what was listed is kept; one for a child type a resource
// doesn't have, or an item azlist couldn't read, is only logged.
func TestChildListFailures(t *testing.T) {
	t.Parallel()
	response := func(status int, text string) string {
		return "GET https://management.azure.com/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/sites/app/config\n" +
			"--------------------------------------------------------------------------------\n" +
			"RESPONSE " + strconv.Itoa(status) + ": " + text + "\nERROR CODE: X\n"
	}
	for message, failed := range map[string]bool{
		response(403, "403 Forbidden"):                                    true,
		response(401, "401 Unauthorized"):                                 true,
		response(429, "429 Too Many Requests"):                            true,
		response(500, "500 Internal Server Error"):                        true,
		response(503, "503 Service Unavailable"):                          true,
		"Get \"https://management.azure.com/...\": dial tcp: i/o timeout": true,
		response(400, "400 Bad Request"):                                  false,
		response(409, "409 Conflict"):                                     false,
		response(405, "405 Method Not Allowed"):                           false,
		response(501, "501 Not Implemented"):                              false,
		"no resource id found in response: {}":                            false,
	} {
		e := azlist.ListError{Endpoint: "/X/CONFIG", Version: "2024-04-01", Message: message}
		if got := childListFailed(e); got != failed {
			t.Errorf("%q: failed %v, want %v", message, got, failed)
		}
	}

	if err := childListFailures([]azlist.ListError{{Endpoint: "/A", Message: response(400, "400 Bad Request")}}); err != nil {
		t.Errorf("a child type a resource doesn't have: %v", err)
	}
	forbidden := azlist.ListError{Endpoint: "/B", Message: response(403, "403 Forbidden")}
	err := childListFailures([]azlist.ListError{{Endpoint: "/A", Message: response(400, "400 Bad Request")}, forbidden})
	var listErr azlist.ListError
	if !harvest.IsPartial(err) || !errors.As(err, &listErr) || listErr.Endpoint != "/B" {
		t.Errorf("got %v, want the refused list as a partial error", err)
	}
}

// The tables and saved searches every Log Analytics workspace has built
// in are recorded with what tells them apart, and the built-in skip rules
// leave them out; a table whose plan or retention was changed, a custom
// table and a user's saved search are kept. Before the fix, one
// workspace in the Azure sandbox imported 697 built-in tables and 39
// LogManagement saved searches.
func TestGraphResourcesSkipsBuiltInWorkspaceTablesAndSearches(t *testing.T) {
	t.Parallel()
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
	}, nil)
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
	t.Parallel()
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

// pseudoID is the ID aztft gives a resource azurerm models apart from the
// one owner names, as populate does: owner's ID with kind and the base64
// of target's ID after it.
func pseudoID(t *testing.T, owner, kind, target string) armid.ResourceId {
	t.Helper()
	rid, err := armid.ParseResourceId(owner)
	if err != nil {
		t.Fatal(err)
	}
	id := rid.Clone().(*armid.ScopedResourceId)
	id.AttrTypes = append(id.AttrTypes, kind)
	id.AttrNames = append(id.AttrNames, base64.StdEncoding.EncodeToString([]byte(target)))
	return id
}

// An association aztft finds as part of a resource is named after that
// resource, and the associated one where it can have several. Before, it
// was named after the base64 of the associated resource's ID, which made
// labels hundreds of characters long.
func TestGraphNameOfAssociations(t *testing.T) {
	t.Parallel()
	const network = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg1/providers/Microsoft.Network"
	subnet := network + "/virtualNetworks/net1/subnets/app"
	nic := network + "/networkInterfaces/vm1-nic"
	for _, tc := range []struct {
		id   armid.ResourceId
		want string
	}{
		{pseudoID(t, subnet, "routeTables", network+"/routeTables/ih-sbx-rt"), "app"},
		{pseudoID(t, subnet, "networkSecurityGroups", network+"/networkSecurityGroups/web"), "app"},
		{pseudoID(t, nic, "networkSecurityGroups", network+"/networkSecurityGroups/web"), "vm1-nic"},
		{pseudoID(t, nic+"/ipConfigurations/internal", "applicationSecurityGroups", network+"/applicationSecurityGroups/web"), "vm1-nic_web"},
		{pseudoID(t, network+"/natGateways/gw", "publicIPAddresses", network+"/publicIPAddresses/gw-ip"), "gw_gw-ip"},
	} {
		got := mapping{types: []aztft.Type{{AzureId: tc.id, TFType: "azurerm_x"}}, ids: []string{"x"}}.records("x", nil)
		if len(got) != 1 || got[0].RawName != tc.want {
			t.Errorf("%s: got %+v, want the name %q", tc.id, got, tc.want)
		}
	}
	// Other resources keep the last name in their ID.
	rid, err := armid.ParseResourceId(subnet)
	if err != nil {
		t.Fatal(err)
	}
	if got := graphName(rid); got != "app" {
		t.Errorf("subnet: got %q, want app", got)
	}
}

// azlist lists child resources childListParallel at a time, not one per
// CPU, recursively and with managed resources.
func TestListerOption(t *testing.T) {
	t.Parallel()
	opt := listerOption("00000000-0000-0000-0000-000000000001", nil, arm.ClientOptions{})
	if opt.Parallelism != childListParallel || !opt.Recursive || !opt.IncludeManaged || opt.SubscriptionId != "00000000-0000-0000-0000-000000000001" {
		t.Errorf("option %+v", opt)
	}
}

// pagedTransport answers the requests it gets with pages, one each in
// order, and records the body of each request.
type pagedTransport struct {
	pages []string
	sent  []string
}

func (f *pagedTransport) Do(r *http.Request) (*http.Response, error) {
	sent, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	f.sent = append(f.sent, string(sent))
	if len(f.sent) > len(f.pages) {
		return nil, fmt.Errorf("request %d, but only %d pages", len(f.sent), len(f.pages))
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(f.pages[len(f.sent)-1])),
		Request:    r,
	}, nil
}

// Every Resource Graph query asks for graphPageSize rows a page, where
// Resource Graph would send 100, and follows the skip token to the last
// page. A row that isn't an object is left out.
func TestGraphQueryPages(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		query func(options *arm.ClientOptions) (int, error)
	}{
		{"resource groups", func(options *arm.ClientOptions) (int, error) {
			groups, err := graphResourceGroups(t.Context(), fakeCredential{}, options, "sub", "subscriptionId =~ 'sub'")
			return len(groups), err
		}},
		{"role assignments", func(options *arm.ClientOptions) (int, error) {
			rows, err := authorizationRows(t.Context(), fakeCredential{}, options, "sub")
			return len(rows), err
		}},
		{"subscriptions", func(options *arm.ClientOptions) (int, error) {
			subscriptions, err := subscriptionsIn(t.Context(), fakeCredential{}, options, "platform")
			return len(subscriptions), err
		}},
	} {
		row := func(n string) string {
			return `{"id":"/subscriptions/sub-` + n + `/resourceGroups/rg-` + n + `","subscriptionId":"sub-` + n + `","type":"microsoft.authorization/roleassignments"}`
		}
		transport := &pagedTransport{pages: []string{
			`{"$skipToken":"next","data":[` + row("1") + `,"not an object"]}`,
			`{"data":[` + row("2") + `]}`,
		}}
		got, err := tc.query(&arm.ClientOptions{ClientOptions: policy.ClientOptions{Transport: transport}})
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != 2 {
			t.Errorf("%s: %d rows, want 2", tc.name, got)
		}
		if len(transport.sent) != 2 {
			t.Fatalf("%s: %d requests, want 2", tc.name, len(transport.sent))
		}
		for i, sent := range transport.sent {
			if !strings.Contains(sent, `"$top":1000`) {
				t.Errorf("%s: request %d doesn't ask for 1000 rows: %s", tc.name, i, sent)
			}
		}
		if strings.Contains(transport.sent[0], "$skipToken") || !strings.Contains(transport.sent[1], `"$skipToken":"next"`) {
			t.Errorf("%s: skip tokens sent: %v", tc.name, transport.sent)
		}
	}
}
