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

type NetworkWatcherGenerator struct {
	AzureService
}

func (az *NetworkWatcherGenerator) listResources() ([]*armnetwork.Watcher, error) {
	subscriptionID, resourceGroup, credential, options := az.getClientArgs()
	client, err := armnetwork.NewWatchersClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if resourceGroup != "" {
		return listAll(ctx, client.NewListPager(resourceGroup, nil),
			func(p armnetwork.WatchersClientListResponse) []*armnetwork.Watcher { return p.Value })
	}
	return listAll(ctx, client.NewListAllPager(nil),
		func(p armnetwork.WatchersClientListAllResponse) []*armnetwork.Watcher { return p.Value })
}

func (az *NetworkWatcherGenerator) appendResource(resource *armnetwork.Watcher) {
	az.AppendSimpleResource(*resource.ID, *resource.Name, "azurerm_network_watcher")
}

func (az *NetworkWatcherGenerator) appendFlowLogs(parent *armnetwork.Watcher, resourceGroupID *ResourceID) error {
	subscriptionID, _, credential, options := az.getClientArgs()
	client, err := armnetwork.NewFlowLogsClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := context.Background()
	flowLogs, err := listAll(ctx, client.NewListPager(resourceGroupID.ResourceGroup, *parent.Name, nil),
		func(p armnetwork.FlowLogsClientListResponse) []*armnetwork.FlowLog { return p.Value })
	for _, item := range flowLogs {
		az.AppendSimpleResource(*item.ID, *item.Name, "azurerm_network_watcher_flow_log")
	}
	return err
}

func (az *NetworkWatcherGenerator) appendPacketCaptures(parent *armnetwork.Watcher, resourceGroupID *ResourceID) error {
	subscriptionID, _, credential, options := az.getClientArgs()
	client, err := armnetwork.NewPacketCapturesClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := context.Background()
	captures, err := listAll(ctx, client.NewListPager(resourceGroupID.ResourceGroup, *parent.Name, nil),
		func(p armnetwork.PacketCapturesClientListResponse) []*armnetwork.PacketCaptureResult { return p.Value })
	if err != nil {
		return err
	}
	for _, item := range captures {
		az.AppendSimpleResource(*item.ID, *item.Name, "azurerm_network_packet_capture")
	}
	return nil
}

func (az *NetworkWatcherGenerator) InitResources() error {

	resources, err := az.listResources()
	if err != nil {
		return err
	}
	for _, resource := range resources {
		az.appendResource(resource)
		resourceGroupID, err := ParseAzureResourceID(*resource.ID)
		if err != nil {
			return err
		}
		err = az.appendFlowLogs(resource, resourceGroupID)
		if err != nil {
			return err
		}
		err = az.appendPacketCaptures(resource, resourceGroupID)
		if err != nil {
			return err
		}
	}
	return nil
}
