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

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns/v2"
	"github.com/IgnatG/infraharvest/terraformutils"
)

// privateDNSRecordResourceTypes maps the record type at the end of a private
// DNS record set type (for example "Microsoft.Network/privateDnsZones/CNAME")
// to its Terraform type.
var privateDNSRecordResourceTypes = map[string]string{
	"A":     "azurerm_private_dns_a_record",
	"AAAA":  "azurerm_private_dns_aaaa_record",
	"CNAME": "azurerm_private_dns_cname_record",
	"MX":    "azurerm_private_dns_mx_record",
	"PTR":   "azurerm_private_dns_ptr_record",
	"SRV":   "azurerm_private_dns_srv_record",
	"TXT":   "azurerm_private_dns_txt_record",
}

type PrivateDNSGenerator struct {
	AzureService
}

func (g *PrivateDNSGenerator) listRecordSets(resourceGroupName string, privateZoneName string, top *int32) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	recordSetsClient, err := armprivatedns.NewRecordSetsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	recordSets, err := listAllLenient(ctx,
		recordSetsClient.NewListPager(resourceGroupName, privateZoneName, &armprivatedns.RecordSetsClientListOptions{Top: top}),
		func(p armprivatedns.RecordSetsClientListResponse) []*armprivatedns.RecordSet { return p.Value })
	if err != nil {
		return nil, err
	}
	for _, recordSet := range recordSets {
		if resName, exist := recordResourceType(*recordSet.Type, privateDNSRecordResourceTypes); exist {
			resources = append(resources, terraformutils.NewSimpleResource(
				*recordSet.ID,
				*recordSet.Name,
				resName,
				g.ProviderName))
		}
	}
	return resources, nil
}

func (g *PrivateDNSGenerator) listVirtualNetworkLinks(resourceGroupName string, privateZoneName string, pageSize *int32) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	virtualNetworkLinksClient, err := armprivatedns.NewVirtualNetworkLinksClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	virtualNetworkLinks, err := listAllLenient(ctx,
		virtualNetworkLinksClient.NewListPager(resourceGroupName, privateZoneName, &armprivatedns.VirtualNetworkLinksClientListOptions{Top: pageSize}),
		func(p armprivatedns.VirtualNetworkLinksClientListResponse) []*armprivatedns.VirtualNetworkLink {
			return p.Value
		})
	if err != nil {
		return nil, err
	}
	for _, virtualNetworkLink := range virtualNetworkLinks {
		resources = append(resources, terraformutils.NewSimpleResource(
			*virtualNetworkLink.ID,
			*virtualNetworkLink.Name,
			"azurerm_private_dns_zone_virtual_network_link",
			g.ProviderName))
	}

	return resources, nil
}

func (g *PrivateDNSGenerator) listAndAddForPrivateDNSZone() ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	privateDNSZonesClient, err := armprivatedns.NewPrivateZonesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	pageSize := to.Ptr[int32](50)

	var zones []*armprivatedns.PrivateZone
	if resourceGroup != "" {
		zones, err = listAll(ctx,
			privateDNSZonesClient.NewListByResourceGroupPager(resourceGroup, &armprivatedns.PrivateZonesClientListByResourceGroupOptions{Top: pageSize}),
			func(p armprivatedns.PrivateZonesClientListByResourceGroupResponse) []*armprivatedns.PrivateZone {
				return p.Value
			})
	} else {
		zones, err = listAll(ctx,
			privateDNSZonesClient.NewListPager(&armprivatedns.PrivateZonesClientListOptions{Top: pageSize}),
			func(p armprivatedns.PrivateZonesClientListResponse) []*armprivatedns.PrivateZone { return p.Value })
	}
	if err != nil {
		return nil, err
	}
	for _, zone := range zones {
		resources = append(resources, terraformutils.NewSimpleResource(
			*zone.ID,
			*zone.Name,
			"azurerm_private_dns_zone",
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

		networkLinks, err := g.listVirtualNetworkLinks(id.ResourceGroup, *zone.Name, pageSize)
		if err != nil {
			return nil, err
		}
		resources = append(resources, networkLinks...)
	}

	return resources, nil
}

func (g *PrivateDNSGenerator) InitResources() error {
	functions := []func() ([]terraformutils.Resource, error){
		g.listAndAddForPrivateDNSZone,
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
