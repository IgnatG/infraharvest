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
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v8"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type DiskGenerator struct {
	AzureService
}

func (g DiskGenerator) createResources(disks []*armcompute.Disk) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, disk := range disks {
		resources = append(resources, terraformutils.NewSimpleResource(
			*disk.ID,
			*disk.Name,
			"azurerm_managed_disk",
			"azurerm"))
	}
	return resources
}

func (g *DiskGenerator) InitResources() error {
	ctx := g.Context()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	disksClient, err := armcompute.NewDisksClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}

	var disks []*armcompute.Disk
	if resourceGroup != "" {
		disks, err = listAll(ctx, disksClient.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armcompute.DisksClientListByResourceGroupResponse) []*armcompute.Disk { return p.Value })
	} else {
		disks, err = listAll(ctx, disksClient.NewListPager(nil),
			func(p armcompute.DisksClientListResponse) []*armcompute.Disk { return p.Value })
	}
	g.Resources = g.createResources(disks)
	return err
}
