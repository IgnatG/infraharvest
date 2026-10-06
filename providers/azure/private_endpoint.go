// Copyright 2021 The Terraformer Authors.
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

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v11"
)

type PrivateEndpointGenerator struct {
	AzureService
}

func (az *PrivateEndpointGenerator) listServices() ([]*armnetwork.PrivateLinkService, error) {
	subscriptionID, resourceGroup, credential, options := az.getClientArgs()
	client, err := armnetwork.NewPrivateLinkServicesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if resourceGroup != "" {
		return listAll(ctx, client.NewListPager(resourceGroup, nil),
			func(p armnetwork.PrivateLinkServicesClientListResponse) []*armnetwork.PrivateLinkService {
				return p.Value
			})
	}
	return listAll(ctx, client.NewListBySubscriptionPager(nil),
		func(p armnetwork.PrivateLinkServicesClientListBySubscriptionResponse) []*armnetwork.PrivateLinkService {
			return p.Value
		})
}

func (az *PrivateEndpointGenerator) AppendServices(link *armnetwork.PrivateLinkService) {
	az.AppendSimpleResource(*link.ID, *link.Name, "azurerm_private_link_service")
}

func (az *PrivateEndpointGenerator) listEndpoints() ([]*armnetwork.PrivateEndpoint, error) {
	subscriptionID, resourceGroup, credential, options := az.getClientArgs()
	client, err := armnetwork.NewPrivateEndpointsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if resourceGroup != "" {
		return listAll(ctx, client.NewListPager(resourceGroup, nil),
			func(p armnetwork.PrivateEndpointsClientListResponse) []*armnetwork.PrivateEndpoint { return p.Value })
	}
	return listAll(ctx, client.NewListBySubscriptionPager(nil),
		func(p armnetwork.PrivateEndpointsClientListBySubscriptionResponse) []*armnetwork.PrivateEndpoint {
			return p.Value
		})
}

func (az *PrivateEndpointGenerator) AppendEndpoint(link *armnetwork.PrivateEndpoint) {
	az.AppendSimpleResource(*link.ID, *link.Name, "azurerm_private_endpoint")
}

func (az *PrivateEndpointGenerator) InitResources() error {

	services, err := az.listServices()
	if err != nil {
		return err
	}
	for _, link := range services {
		az.AppendServices(link)
	}
	endpoints, err := az.listEndpoints()
	if err != nil {
		return err
	}
	for _, endpoint := range endpoints {
		az.AppendEndpoint(endpoint)
	}
	return nil
}
