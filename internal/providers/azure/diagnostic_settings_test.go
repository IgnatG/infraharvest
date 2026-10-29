// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// diagnosticTransport answers a request for a path in bodies with its
// body, one for a path in statuses with that status, and any other with
// 404, and records the paths asked for. Requests may come at once.
type diagnosticTransport struct {
	mu       sync.Mutex
	bodies   map[string]string
	statuses map[string]int
	paths    []string
}

func (f *diagnosticTransport) Do(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.Path)
	status, body := http.StatusOK, f.bodies[r.URL.Path]
	if s, ok := f.statuses[r.URL.Path]; ok {
		status, body = s, `{"error":{"code":"Failed","message":"answered `+http.StatusText(s)+`"}}`
	} else if _, ok := f.bodies[r.URL.Path]; !ok {
		status, body = http.StatusNotFound, `{"error":{"code":"NotFound","message":"nothing at `+r.URL.Path+`"}}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

// diagnosticArgs are the settings of diagnostic_setting for subscription
// "sub" and resourceGroup, after the other services listed listed, with
// every request answered by transport, once.
func diagnosticArgs(transport *diagnosticTransport, resourceGroup string, listed []harvest.Resource) serviceSettings {
	return serviceSettings{
		subscriptionID: "sub",
		resourceGroup:  resourceGroup,
		credential:     fakeCredential{},
		clientOptions: &arm.ClientOptions{ClientOptions: policy.ClientOptions{
			Transport: transport,
			Retry:     policy.RetryOptions{MaxRetries: -1},
		}},
		listed: listed,
	}
}

// The settings of a resource are listed at its ID.
func settingsPath(id string) string {
	return id + "/providers/Microsoft.Insights/diagnosticSettings"
}

const (
	vaultID   = "/subscriptions/sub/resourceGroups/app/providers/Microsoft.KeyVault/vaults/secrets"
	accountID = "/subscriptions/sub/resourceGroups/app/providers/Microsoft.Storage/storageAccounts/data"
	subnetID  = "/subscriptions/sub/resourceGroups/app/providers/Microsoft.Network/virtualNetworks/net/subnets/web"
)

// listedByOthers is what resource_graph lists in the tests: a key vault,
// twice, the second time in other case, a storage account, a subnet,
// which can't have diagnostic settings, and the association of the subnet
// with a route table, whose import ID isn't a Resource Manager ID.
func listedByOthers() []harvest.Resource {
	return []harvest.Resource{
		harvest.Record(vaultID, "secrets", "azurerm_key_vault", "azurerm", map[string]string{"resource_group_name": "app"}),
		harvest.Record(strings.ToLower(vaultID), "secrets", "azurerm_key_vault", "azurerm", nil),
		harvest.Record(accountID, "data", "azurerm_storage_account", "azurerm", nil),
		harvest.Record(subnetID, "web", "azurerm_subnet", "azurerm", nil),
		harvest.Record(subnetID+"|/subscriptions/sub/resourceGroups/app/providers/Microsoft.Network/routeTables/rt", "web", "azurerm_subnet_route_table_association", "azurerm", nil),
	}
}

// diagnostic_setting asks for the settings of the subscription and of each
// resource the other services listed that can have them, once, and of a
// storage account's services, and records each setting under the ID its
// target is imported by, whatever the case of the IDs Resource Manager
// returns, named after its target. A subnet, which can't have any, costs
// no request, nor does what isn't a Resource Manager ID; a setting without
// a name is left out.
func TestDiagnosticSettingListsWhatOthersListed(t *testing.T) {
	t.Parallel()
	const subscriptionPath = "/subscriptions/sub/providers/Microsoft.Insights/diagnosticSettings"
	transport := &diagnosticTransport{bodies: map[string]string{
		subscriptionPath:      armList(armItem(strings.ToLower(subscriptionPath)+"/activity", "activity", `{"logs":[{"category":"Administrative","enabled":true}]}`)),
		settingsPath(vaultID): armList(armItem(strings.ToLower(settingsPath(vaultID))+"/audit", "audit", `{"workspaceId":"/subscriptions/sub/resourcegroups/app/providers/microsoft.operationalinsights/workspaces/logs"}`), `{"id":"nameless"}`),
		settingsPath(accountID + "/blobServices/default"): armList(armItem(settingsPath(accountID+"/blobServices/default")+"/blob-logs", "blob-logs", "")),
	}}
	g := &DiagnosticSettingLister{}
	g.SetSettings(diagnosticArgs(transport, "", listedByOthers()))

	resources, err := g.List(t.Context(), harvest.Settings{Provider: "azurerm"})
	if err != nil {
		t.Fatal(err)
	}

	asked := slices.Sorted(slices.Values(transport.paths))
	want := []string{
		settingsPath(accountID),
		settingsPath(accountID + "/blobServices/default"),
		settingsPath(accountID + "/fileServices/default"),
		settingsPath(accountID + "/queueServices/default"),
		settingsPath(accountID + "/tableServices/default"),
		settingsPath(vaultID),
		subscriptionPath,
	}
	slices.Sort(want)
	if !slices.Equal(asked, want) {
		t.Errorf("asked for\n%v\nwant\n%v", asked, want)
	}
	wantListed := []listedResource{
		{Type: diagnosticSettingType, ID: subscriptionPath + "/activity", Name: "subscription_activity", Attributes: map[string]string{"name": "activity", "target_resource_id": "/subscriptions/sub"}},
		{Type: diagnosticSettingType, ID: settingsPath(vaultID) + "/audit", Name: "secrets_audit", Attributes: map[string]string{"name": "audit", "target_resource_id": vaultID, "resource_group_name": "app"}},
		{Type: diagnosticSettingType, ID: settingsPath(accountID+"/blobServices/default") + "/blob-logs", Name: "data_blobServices_blob-logs", Attributes: map[string]string{"name": "blob-logs", "target_resource_id": accountID + "/blobServices/default", "resource_group_name": "app"}},
	}
	if got := listedResources(resources); !reflect.DeepEqual(got, wantListed) {
		t.Errorf("got\n%+v\nwant\n%+v", got, wantListed)
	}
}

// With --resource-group, the subscription's settings aren't the group's:
// only those of what the other services listed are asked for.
func TestDiagnosticSettingInAResourceGroup(t *testing.T) {
	t.Parallel()
	transport := &diagnosticTransport{}
	g := &DiagnosticSettingLister{}
	g.SetSettings(diagnosticArgs(transport, "app", listedByOthers()[:1]))

	resources, err := g.List(t.Context(), harvest.Settings{Provider: "azurerm"})
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{settingsPath(vaultID)}; !slices.Equal(transport.paths, want) {
		t.Errorf("asked for %v, want %v", transport.paths, want)
	}
	if len(resources) != 0 {
		t.Errorf("listed %+v, want none", resources)
	}
}

// A resource whose settings are refused fails the service in part, and
// the settings of the others are kept; one whose type or SKU has none
// (400) or that is gone (404) has none.
func TestDiagnosticSettingFailures(t *testing.T) {
	t.Parallel()
	transport := &diagnosticTransport{
		bodies: map[string]string{
			"/subscriptions/sub/providers/Microsoft.Insights/diagnosticSettings": armList(armItem("/subscriptions/sub/providers/microsoft.insights/diagnosticSettings/activity", "activity", "")),
		},
		statuses: map[string]int{
			settingsPath(vaultID):   http.StatusForbidden,
			settingsPath(accountID): http.StatusBadRequest,
		},
	}
	g := &DiagnosticSettingLister{}
	g.SetSettings(diagnosticArgs(transport, "", listedByOthers()))

	resources, err := g.List(t.Context(), harvest.Settings{Provider: "azurerm"})

	if !harvest.IsPartial(err) || !strings.Contains(err.Error(), vaultID) || strings.Contains(err.Error(), accountID) {
		t.Errorf("got %v, want the vault's refusal alone, as a partial failure", err)
	}
	if len(resources) != 1 || resources[0].Observed.ID != "/subscriptions/sub/providers/Microsoft.Insights/diagnosticSettings/activity" {
		t.Errorf("listed %+v, want the subscription's setting", listedResources(resources))
	}
}

// Only what the other services listed of azurerm, as Resource Manager IDs
// of types that can have diagnostic settings, are asked about, and no
// diagnostic setting is.
func TestDiagnosticTargets(t *testing.T) {
	t.Parallel()
	listed := append(listedByOthers(),
		harvest.Record(settingsPath(vaultID)+"/audit", "secrets_audit", diagnosticSettingType, "azurerm", nil),
		harvest.Record("/subscriptions/sub/resourceGroups/app/providers/Microsoft.Web/sites/api", "api", "aws_thing", "aws", nil),
		harvest.Record("/subscriptions/sub/resourceGroups/MC_app_aks_westeurope/providers/Microsoft.Network/loadBalancers/kubernetes", "kubernetes", "azurerm_lb", "azurerm",
			map[string]string{"resource_group_name": "MC_app_aks_westeurope", "resource_group_managed_by": "/subscriptions/sub/resourceGroups/app/providers/Microsoft.ContainerService/managedClusters/aks"}),
	)
	var got []string
	for _, target := range diagnosticTargets(listed) {
		got = append(got, target.label)
	}
	want := []string{"secrets", "data", "data_blobServices", "data_fileServices", "data_queueServices", "data_tableServices", "kubernetes"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The catalogue leaves out the settings Azure Policy deploys under the
// names its built-in policies give them, whatever their case, and those
// of what is in a group another resource manages, and imports a setting
// by its target and name.
func TestDiagnosticSettingCatalogue(t *testing.T) {
	t.Parallel()
	cat := catalogue.MustBuiltin("azurerm")
	const aks = "/subscriptions/sub/resourceGroups/app/providers/Microsoft.ContainerService/managedClusters/aks"
	for _, tc := range []struct {
		name       string
		attributes map[string]string
		skipped    bool
	}{
		{"audit", map[string]string{"resource_group_name": "app"}, false},
		{"setByPolicy-LogAnalytics", nil, true},
		{"setbypolicy_eventHub", nil, true},
		{"setbypolicy", nil, true},
		{"lb", map[string]string{"resource_group_name": "MC_app_aks_westeurope", "resource_group_managed_by": aks}, true},
	} {
		attributes := map[string]string{"name": tc.name, "target_resource_id": vaultID}
		for key, value := range tc.attributes {
			attributes[key] = value
		}
		id := settingsPath(vaultID) + "/" + tc.name
		if skipped := cat.Skipped(diagnosticSettingType, id, attributes) != ""; skipped != tc.skipped {
			t.Errorf("%s: left out by default %v, want %v", tc.name, skipped, tc.skipped)
		}
		if got, ok, _ := cat.ImportID(diagnosticSettingType, id, attributes); !ok || got != vaultID+"|"+tc.name {
			t.Errorf("%s: imported by %q (%v), want %q", tc.name, got, ok, vaultID+"|"+tc.name)
		}
	}
}

// Every type that can have diagnostic settings is in the list once,
// whatever its case, as <namespace>/<type>.
func TestDiagnosticSettingTypes(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, typ := range diagnosticSettingTypes {
		key := strings.ToUpper(typ)
		if seen[key] {
			t.Errorf("%s is in the list twice", typ)
		}
		seen[key] = true
		if strings.Count(typ, "/") < 1 || strings.HasSuffix(typ, "/") {
			t.Errorf("%s isn't <namespace>/<type>", typ)
		}
	}
	for typ, want := range map[string]bool{
		"Microsoft.KeyVault/vaults":                      true,
		"microsoft.keyvault/VAULTS":                      true,
		"Microsoft.Storage/storageAccounts/blobServices": true,
		"Microsoft.Network/virtualNetworks/subnets":      false,
		"Microsoft.Resources/resourceGroups":             false,
	} {
		if got := hasDiagnosticSettings(typ); got != want {
			t.Errorf("%s: got %v, want %v", typ, got, want)
		}
	}
}
