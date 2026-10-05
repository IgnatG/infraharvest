// Copyright 2020 The Terraformer Authors.
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
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dns/armdns"
	"github.com/IgnatG/infraharvest/terraformutils"
)

// dnsRecordResourceTypes maps the record type at the end of a DNS record set
// type (for example "Microsoft.Network/dnszones/AAAA") to its Terraform type.
var dnsRecordResourceTypes = map[string]string{
	"A":     "azurerm_dns_a_record",
	"AAAA":  "azurerm_dns_aaaa_record",
	"CAA":   "azurerm_dns_caa_record",
	"CNAME": "azurerm_dns_cname_record",
	"MX":    "azurerm_dns_mx_record",
	"NS":    "azurerm_dns_ns_record",
	"PTR":   "azurerm_dns_ptr_record",
	"SRV":   "azurerm_dns_srv_record",
	"TXT":   "azurerm_dns_txt_record",
}

// recordResourceType returns the Terraform type for a record set type, using
// its last path segment, and false for record types it does not import.
func recordResourceType(recordSetType string, types map[string]string) (string, bool) {
	segments := strings.Split(recordSetType, "/")
	resourceType, ok := types[segments[len(segments)-1]]
	return resourceType, ok
}

type DNSGenerator struct {
	AzureService
}

func (g *DNSGenerator) listRecordSets(resourceGroupName string, zoneName string, top *int32) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	recordSetsClient, err := armdns.NewRecordSetsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	recordSets, err := listAll(ctx,
		recordSetsClient.NewListAllByDNSZonePager(resourceGroupName, zoneName, &armdns.RecordSetsClientListAllByDNSZoneOptions{Top: top}),
		func(p armdns.RecordSetsClientListAllByDNSZoneResponse) []*armdns.RecordSet { return p.Value })
	for _, recordSet := range recordSets {
		if resName, exist := recordResourceType(*recordSet.Type, dnsRecordResourceTypes); exist {
			resources = append(resources, terraformutils.NewSimpleResource(
				*recordSet.ID,
				*recordSet.Name,
				resName,
				g.ProviderName))
		}
	}
	return resources, err
}

func (g *DNSGenerator) listAndAddForDNSZone() ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	dnsZonesClient, err := armdns.NewZonesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	pageSize := to.Ptr[int32](50)

	var zones []*armdns.Zone
	if resourceGroup != "" {
		zones, err = listAll(ctx,
			dnsZonesClient.NewListByResourceGroupPager(resourceGroup, &armdns.ZonesClientListByResourceGroupOptions{Top: pageSize}),
			func(p armdns.ZonesClientListByResourceGroupResponse) []*armdns.Zone { return p.Value })
	} else {
		zones, err = listAll(ctx,
			dnsZonesClient.NewListPager(&armdns.ZonesClientListOptions{Top: pageSize}),
			func(p armdns.ZonesClientListResponse) []*armdns.Zone { return p.Value })
	}
	if err != nil {
		return nil, err
	}
	for _, zone := range zones {
		resources = append(resources, terraformutils.NewSimpleResource(
			*zone.ID,
			*zone.Name,
			"azurerm_dns_zone",
			g.ProviderName))

		id, err := ParseAzureResourceID(*zone.ID)
		if err != nil {
			return nil, err
		}

		records, err := g.listRecordSets(id.ResourceGroup, *zone.Name, pageSize)
		if err != nil {
			return nil, err
		}
		resources = append(resources, records...)
	}

	return resources, nil
}

func (g *DNSGenerator) InitResources() error {
	functions := []func() ([]terraformutils.Resource, error){
		g.listAndAddForDNSZone,
	}

	for _, f := range functions {
		resources, err := f()
		if err != nil {
			return err
		}
		g.Resources = append(g.Resources, resources...)
	}

	return nil
}
