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

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v8"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type ScaleSetGenerator struct {
	AzureService
}

func (g ScaleSetGenerator) createResources(scaleSets []*armcompute.VirtualMachineScaleSet) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, scaleSet := range scaleSets {
		resources = append(resources, terraformutils.NewSimpleResource(
			*scaleSet.ID,
			*scaleSet.Name,
			"azurerm_virtual_machine_scale_set",
			"azurerm"))
	}
	return resources
}

func (g *ScaleSetGenerator) InitResources() error {
	ctx := context.Background()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	scaleSetClient, err := armcompute.NewVirtualMachineScaleSetsClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}

	var scaleSets []*armcompute.VirtualMachineScaleSet
	if resourceGroup != "" {
		scaleSets, err = listAll(ctx, scaleSetClient.NewListPager(resourceGroup, nil),
			func(p armcompute.VirtualMachineScaleSetsClientListResponse) []*armcompute.VirtualMachineScaleSet {
				return p.Value
			})
	} else {
		scaleSets, err = listAll(ctx, scaleSetClient.NewListAllPager(nil),
			func(p armcompute.VirtualMachineScaleSetsClientListAllResponse) []*armcompute.VirtualMachineScaleSet {
				return p.Value
			})
	}
	g.Resources = g.createResources(scaleSets)
	return err
}
