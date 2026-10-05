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

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v8"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type VirtualMachineGenerator struct {
	AzureService
}

// virtualMachineResourceType tells Windows virtual machines from Linux ones:
// by the OS profile when the VM has one, otherwise (VMs created from a
// specialized disk have none) by the OS disk type.
func virtualMachineResourceType(vm *armcompute.VirtualMachine) string {
	var osProfile *armcompute.OSProfile
	var osType *armcompute.OperatingSystemTypes
	if props := vm.Properties; props != nil {
		osProfile = props.OSProfile
		if props.StorageProfile != nil && props.StorageProfile.OSDisk != nil {
			osType = props.StorageProfile.OSDisk.OSType
		}
	}
	if osProfile == nil {
		if osType != nil && *osType == armcompute.OperatingSystemTypesWindows {
			return "azurerm_windows_virtual_machine"
		}
		return "azurerm_linux_virtual_machine"
	}
	if osProfile.WindowsConfiguration != nil {
		return "azurerm_windows_virtual_machine"
	}
	return "azurerm_linux_virtual_machine"
}

func (g VirtualMachineGenerator) createResources(virtualMachines []*armcompute.VirtualMachine) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, vm := range virtualMachines {
		resources = append(resources, terraformutils.NewSimpleResource(
			*vm.ID,
			*vm.Name,
			virtualMachineResourceType(vm),
			"azurerm"))
	}
	return resources
}

func (g *VirtualMachineGenerator) InitResources() error {
	ctx := context.Background()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	vmClient, err := armcompute.NewVirtualMachinesClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}

	var virtualMachines []*armcompute.VirtualMachine
	if resourceGroup != "" {
		virtualMachines, err = listAll(ctx, vmClient.NewListPager(resourceGroup, nil),
			func(p armcompute.VirtualMachinesClientListResponse) []*armcompute.VirtualMachine { return p.Value })
	} else {
		virtualMachines, err = listAll(ctx, vmClient.NewListAllPager(nil),
			func(p armcompute.VirtualMachinesClientListAllResponse) []*armcompute.VirtualMachine { return p.Value })
	}
	g.Resources = g.createResources(virtualMachines)
	return err
}
