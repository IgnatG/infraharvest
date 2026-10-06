// Copyright 2019 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package azure

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/datafactory/armdatafactory/v11"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type fakePage struct {
	items []*string
	next  int
}

// fakePager serves pages of items and fails at page failAt (0 for never).
func fakePager(pages [][]*string, failAt int) *runtime.Pager[fakePage] {
	return runtime.NewPager(runtime.PagingHandler[fakePage]{
		More: func(p fakePage) bool { return p.next < len(pages) },
		Fetcher: func(_ context.Context, current *fakePage) (fakePage, error) {
			index := 0
			if current != nil {
				index = current.next
			}
			if index+1 == failAt {
				return fakePage{}, errors.New("page failed")
			}
			return fakePage{items: pages[index], next: index + 1}, nil
		},
	})
}

func pageItems(p fakePage) []*string { return p.items }

func values(items []*string) []string {
	var out []string
	for _, item := range items {
		out = append(out, *item)
	}
	return out
}

func TestListAllWalksEveryPageAndSkipsNilItems(t *testing.T) {
	pages := [][]*string{{to.Ptr("a"), nil}, {}, {to.Ptr("b"), to.Ptr("c")}}
	items, err := listAll(context.Background(), fakePager(pages, 0), pageItems)
	if err != nil {
		t.Fatal(err)
	}
	if got := values(items); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("items = %v", got)
	}
}

func TestListAllStopsAtAFailingPage(t *testing.T) {
	pages := [][]*string{{to.Ptr("a")}, {to.Ptr("b")}, {to.Ptr("c")}}
	items, err := listAll(context.Background(), fakePager(pages, 2), pageItems)
	if err == nil {
		t.Fatal("want the page error")
	}
	if got := values(items); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("items = %v", got)
	}
}

func TestListAllLenientKeepsItemsWhenALaterPageFails(t *testing.T) {
	pages := [][]*string{{to.Ptr("a")}, {to.Ptr("b")}, {to.Ptr("c")}}
	items, err := listAllLenient(context.Background(), fakePager(pages, 3), pageItems)
	if err != nil {
		t.Fatalf("err = %v, want nil after the first page", err)
	}
	if got := values(items); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("items = %v", got)
	}

	if _, err := listAllLenient(context.Background(), fakePager(pages, 1), pageItems); err == nil {
		t.Fatal("want the error when the first page fails")
	}
}

func TestVirtualMachineResourceType(t *testing.T) {
	windows := to.Ptr(armcompute.OperatingSystemTypesWindows)
	linux := to.Ptr(armcompute.OperatingSystemTypesLinux)
	vmWith := func(osProfile *armcompute.OSProfile, osType *armcompute.OperatingSystemTypes) *armcompute.VirtualMachine {
		return &armcompute.VirtualMachine{Properties: &armcompute.VirtualMachineProperties{
			OSProfile:      osProfile,
			StorageProfile: &armcompute.StorageProfile{OSDisk: &armcompute.OSDisk{OSType: osType}},
		}}
	}
	cases := []struct {
		name string
		vm   *armcompute.VirtualMachine
		want string
	}{
		{"windows profile", vmWith(&armcompute.OSProfile{WindowsConfiguration: &armcompute.WindowsConfiguration{}}, linux), "azurerm_windows_virtual_machine"},
		{"linux profile", vmWith(&armcompute.OSProfile{LinuxConfiguration: &armcompute.LinuxConfiguration{}}, windows), "azurerm_linux_virtual_machine"},
		{"no profile, windows disk", vmWith(nil, windows), "azurerm_windows_virtual_machine"},
		{"no profile, linux disk", vmWith(nil, linux), "azurerm_linux_virtual_machine"},
		{"no properties", &armcompute.VirtualMachine{}, "azurerm_linux_virtual_machine"},
	}
	for _, c := range cases {
		if got := virtualMachineResourceType(c.vm); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestRecordResourceType(t *testing.T) {
	if got, ok := recordResourceType("Microsoft.Network/dnszones/AAAA", dnsRecordResourceTypes); !ok || got != "azurerm_dns_aaaa_record" {
		t.Errorf("AAAA: %s %v", got, ok)
	}
	if _, ok := recordResourceType("Microsoft.Network/dnszones/SOA", dnsRecordResourceTypes); ok {
		t.Error("SOA records are not imported")
	}
	if got, ok := recordResourceType("Microsoft.Network/privateDnsZones/CNAME", privateDNSRecordResourceTypes); !ok || got != "azurerm_private_dns_cname_record" {
		t.Errorf("private CNAME: %s %v", got, ok)
	}
	if _, ok := recordResourceType("Microsoft.Network/privateDnsZones/NS", privateDNSRecordResourceTypes); ok {
		t.Error("private NS records are not imported")
	}
}

func TestGetIntegrationRuntimeType(t *testing.T) {
	cases := []struct {
		name       string
		properties armdatafactory.IntegrationRuntimeClassification
		want       string
	}{
		{"self-hosted", &armdatafactory.SelfHostedIntegrationRuntime{Type: to.Ptr(armdatafactory.IntegrationRuntimeTypeSelfHosted)},
			"azurerm_data_factory_integration_runtime_self_hosted"},
		{"managed without SSIS", &armdatafactory.ManagedIntegrationRuntime{
			Type:           to.Ptr(armdatafactory.IntegrationRuntimeTypeManaged),
			TypeProperties: &armdatafactory.ManagedIntegrationRuntimeTypeProperties{},
		}, "azurerm_data_factory_integration_runtime_azure"},
		{"managed with SSIS", &armdatafactory.ManagedIntegrationRuntime{
			Type: to.Ptr(armdatafactory.IntegrationRuntimeTypeManaged),
			TypeProperties: &armdatafactory.ManagedIntegrationRuntimeTypeProperties{
				SsisProperties: &armdatafactory.IntegrationRuntimeSsisProperties{},
			},
		}, "azurerm_data_factory_integration_runtime_azure_ssis"},
		{"unknown kind", &armdatafactory.IntegrationRuntime{Type: to.Ptr(armdatafactory.IntegrationRuntimeType("Other"))},
			"azurerm_data_factory_integration_runtime_azure_ssis"},
	}
	for _, c := range cases {
		if got := getIntegrationRuntimeType(c.properties); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestDataFactoryPolymorphicTypes(t *testing.T) {
	if got := linkedServiceType(&armdatafactory.AzureBlobStorageLinkedService{Type: to.Ptr("AzureBlobStorage")}); got != "AzureBlobStorage" {
		t.Errorf("linked service type = %q", got)
	}
	if got := linkedServiceType(nil); got != "" {
		t.Errorf("nil linked service type = %q", got)
	}
	if got := triggerType(&armdatafactory.ScheduleTrigger{Type: to.Ptr("ScheduleTrigger")}); got != "ScheduleTrigger" {
		t.Errorf("trigger type = %q", got)
	}
	if got := datasetType(&armdatafactory.DelimitedTextDataset{Type: to.Ptr("DelimitedText")}); got != "DelimitedText" {
		t.Errorf("dataset type = %q", got)
	}
	if got := getResourceTypeFrom(datasetType(&armdatafactory.DelimitedTextDataset{Type: to.Ptr("DelimitedText")})); got != "azurerm_data_factory_dataset_delimited_text" {
		t.Errorf("dataset resource type = %q", got)
	}
}

func TestDataFactoryResourceNames(t *testing.T) {
	g := &DataFactoryGenerator{}
	g.ProviderName = "azurerm"
	resources := g.appendResourceFrom(nil, "/subscriptions/s/resourceGroups/rg/providers/Microsoft.DataFactory/factories/f/linkedservices/my-blob",
		"my-blob", "AzureBlobStorage")
	resources = g.appendResourceFrom(resources, "id", "unknown", "NotSupported")
	if len(resources) != 1 {
		t.Fatalf("resources = %d, want 1", len(resources))
	}
	r := resources[0]
	if r.ResourceName != terraformutils.TfSanitize("adf_my_blob") || r.InstanceInfo.Type != "azurerm_data_factory_linked_service_azure_blob_storage" {
		t.Fatalf("resource = %s %s", r.ResourceName, r.InstanceInfo.Type)
	}
}

func TestCosmosDBSQLIDInOldFormat(t *testing.T) {
	id := "/subscriptions/s/resourceGroups/rg/providers/Microsoft.DocumentDB/databaseAccounts/a/sqlDatabases/db/containers/c"
	want := "/subscriptions/s/resourceGroups/rg/providers/Microsoft.DocumentDB/databaseAccounts/a/databases/db/containers/c"
	if got := cosmosDBSQLIDInOldFormat(id); got != want {
		t.Fatalf("got %s", got)
	}
}

func TestLoadBalancerChildRecordsTheLoadBalancerID(t *testing.T) {
	g := &LoadBalancerGenerator{}
	g.ProviderName = "azurerm"
	lb := "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Network/loadBalancers/lb1"
	cases := map[string]*regexp.Regexp{
		lb + "/probes/p1":              loadBalancerProbeSuffix,
		lb + "/inboundNatRules/r1":     loadBalancerInboundNatRuleSuffix,
		lb + "/backendAddressPools/b1": loadBalancerBackendAddressPoolSuffix,
	}
	for id, suffix := range cases {
		r := g.newLoadBalancerChild(to.Ptr(id), to.Ptr("child"), "azurerm_lb_probe", suffix)
		if got := r.InstanceState.Attributes["loadbalancer_id"]; got != lb {
			t.Errorf("%s: loadbalancer_id = %s", id, got)
		}
		if r.InstanceState.ID != id {
			t.Errorf("%s: ID = %s", id, r.InstanceState.ID)
		}
	}
}

func TestContainerResource(t *testing.T) {
	r := containerResource(blobContainer{accountName: "acct", resourceGroup: "rg", name: "data"})
	if r.InstanceState.ID != "https://acct.blob.core.windows.net/data" {
		t.Errorf("ID = %s", r.InstanceState.ID)
	}
	want := map[string]string{"storage_account_name": "acct", "name": "data"}
	for k, v := range want {
		if r.InstanceState.Attributes[k] != v {
			t.Errorf("%s = %q, want %q", k, r.InstanceState.Attributes[k], v)
		}
	}
}

func TestSynapseDevScope(t *testing.T) {
	cases := map[string]string{
		"https://ws.dev.azuresynapse.net":                 "https://dev.azuresynapse.net/.default",
		"https://ws.dev.azuresynapse.usgovcloudapi.net/":  "https://dev.azuresynapse.usgovcloudapi.net/.default",
		"https://ws.dev.azuresynapse.azure.cn:443/subdir": "https://dev.azuresynapse.azure.cn/.default",
	}
	for endpoint, want := range cases {
		got, err := synapseDevScope(endpoint)
		if err != nil {
			t.Fatalf("%s: %v", endpoint, err)
		}
		if got != want {
			t.Errorf("%s: got %s, want %s", endpoint, got, want)
		}
	}
	if _, err := synapseDevScope("https://localhost"); err == nil {
		t.Error("want an error for an endpoint without a workspace label")
	}
}

func TestScope(t *testing.T) {
	for resourceGroup, want := range map[string]string{"app": "app", "": "all"} {
		p := &AzureProvider{subscriptionID: "00000000-0000-0000-0000-000000000001", resourceGroup: resourceGroup}
		account, region, err := p.Scope(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if account != "00000000-0000-0000-0000-000000000001" || region != want {
			t.Errorf("resource group %q: scope %s/%s, want the subscription and %s", resourceGroup, account, region, want)
		}
	}
}
