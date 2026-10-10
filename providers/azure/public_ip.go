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
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v11"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type PublicIPGenerator struct {
	AzureService
}

func (g *PublicIPGenerator) listAndAddForPublicIPAddress() ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := g.Context()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	publicIPAddressesClient, err := armnetwork.NewPublicIPAddressesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	var publicIPs []*armnetwork.PublicIPAddress
	if resourceGroup != "" {
		publicIPs, err = listAll(ctx, publicIPAddressesClient.NewListPager(resourceGroup, nil),
			func(p armnetwork.PublicIPAddressesClientListResponse) []*armnetwork.PublicIPAddress { return p.Value })
	} else {
		publicIPs, err = listAll(ctx, publicIPAddressesClient.NewListAllPager(nil),
			func(p armnetwork.PublicIPAddressesClientListAllResponse) []*armnetwork.PublicIPAddress {
				return p.Value
			})
	}
	for _, publicIP := range publicIPs {
		resources = append(resources, terraformutils.NewSimpleResource(
			*publicIP.ID,
			*publicIP.Name,
			"azurerm_public_ip",
			g.ProviderName))
	}

	return resources, err
}

func (g *PublicIPGenerator) listAndAddForPublicIPPrefix() ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := g.Context()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	publicIPPrefixesClient, err := armnetwork.NewPublicIPPrefixesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	var publicIPPrefixes []*armnetwork.PublicIPPrefix
	if resourceGroup != "" {
		publicIPPrefixes, err = listAll(ctx, publicIPPrefixesClient.NewListPager(resourceGroup, nil),
			func(p armnetwork.PublicIPPrefixesClientListResponse) []*armnetwork.PublicIPPrefix { return p.Value })
	} else {
		publicIPPrefixes, err = listAll(ctx, publicIPPrefixesClient.NewListAllPager(nil),
			func(p armnetwork.PublicIPPrefixesClientListAllResponse) []*armnetwork.PublicIPPrefix { return p.Value })
	}
	for _, publicIPPrefix := range publicIPPrefixes {
		resources = append(resources, terraformutils.NewSimpleResource(
			*publicIPPrefix.ID,
			*publicIPPrefix.Name,
			"azurerm_public_ip_prefix",
			g.ProviderName))
	}

	return resources, err
}

func (g *PublicIPGenerator) InitResources() error {
	functions := []func() ([]terraformutils.Resource, error){
		g.listAndAddForPublicIPAddress,
		g.listAndAddForPublicIPPrefix,
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
