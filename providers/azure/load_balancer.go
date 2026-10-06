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
	"regexp"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v11"
	"github.com/IgnatG/infraharvest/terraformutils"
)

// Child resource suffixes trimmed off a load balancer child ID to get the
// load balancer ID back.
//
// NOTE:
// This works out the loadBalancer resource id from current probe
// /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/group1/providers/Microsoft.Network/loadBalancers/lb1
// /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/group1/providers/Microsoft.Network/loadBalancers/lb1/probes/probe1
//
// As the related data_source in azurerm provider works by starting to look up with loadbalancer_id
// https://github.com/terraform-providers/terraform-provider-azurerm/blob/v2.18.0/azurerm/internal/services/network/lb_probe_resource.go#L186
var (
	loadBalancerProbeSuffix              = regexp.MustCompile(`/probes/.*$`)
	loadBalancerInboundNatRuleSuffix     = regexp.MustCompile(`/inboundNatRules/.*$`)
	loadBalancerBackendAddressPoolSuffix = regexp.MustCompile(`/backendAddressPools/.*$`)
)

type LoadBalancerGenerator struct {
	AzureService
}

// newLoadBalancerChild records a load balancer child resource with the
// loadbalancer_id the azurerm provider looks it up by.
func (g *LoadBalancerGenerator) newLoadBalancerChild(id, name *string, resourceType string, suffix *regexp.Regexp) terraformutils.Resource {
	return terraformutils.NewResource(
		*id,
		*name,
		resourceType,
		g.ProviderName,
		map[string]string{
			"loadbalancer_id": suffix.ReplaceAllLiteralString(*id, ""),
		})
}

func (g *LoadBalancerGenerator) listLoadBalancerProbes(resourceGroupName string, loadBalancerName string) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	loadBalancerProbesClient, err := armnetwork.NewLoadBalancerProbesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	probes, err := listAllLenient(ctx, loadBalancerProbesClient.NewListPager(resourceGroupName, loadBalancerName, nil),
		func(p armnetwork.LoadBalancerProbesClientListResponse) []*armnetwork.Probe { return p.Value })
	if err != nil {
		return nil, err
	}
	for _, probe := range probes {
		resources = append(resources, g.newLoadBalancerChild(probe.ID, probe.Name, "azurerm_lb_probe", loadBalancerProbeSuffix))
	}

	return resources, nil
}

func (g *LoadBalancerGenerator) listInboundNatRules(resourceGroupName string, loadBalancerName string) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	inboundNatRulesClient, err := armnetwork.NewInboundNatRulesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	rules, err := listAllLenient(ctx, inboundNatRulesClient.NewListPager(resourceGroupName, loadBalancerName, nil),
		func(p armnetwork.InboundNatRulesClientListResponse) []*armnetwork.InboundNatRule { return p.Value })
	if err != nil {
		return nil, err
	}
	for _, rule := range rules {
		resources = append(resources, g.newLoadBalancerChild(rule.ID, rule.Name, "azurerm_lb_nat_rule", loadBalancerInboundNatRuleSuffix))
	}

	return resources, nil
}

func (g *LoadBalancerGenerator) listLoadBalancerBackendAddressPools(resourceGroupName string, loadBalancerName string) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	backendAddressPoolsClient, err := armnetwork.NewLoadBalancerBackendAddressPoolsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	pools, err := listAllLenient(ctx, backendAddressPoolsClient.NewListPager(resourceGroupName, loadBalancerName, nil),
		func(p armnetwork.LoadBalancerBackendAddressPoolsClientListResponse) []*armnetwork.BackendAddressPool {
			return p.Value
		})
	if err != nil {
		return nil, err
	}
	for _, pool := range pools {
		resources = append(resources, g.newLoadBalancerChild(pool.ID, pool.Name, "azurerm_lb_backend_address_pool", loadBalancerBackendAddressPoolSuffix))
	}

	return resources, nil
}

func (g *LoadBalancerGenerator) listAndAddForLoadBalancers() ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	loadBalancersClient, err := armnetwork.NewLoadBalancersClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	var loadBalancers []*armnetwork.LoadBalancer
	if resourceGroup != "" {
		loadBalancers, err = listAll(ctx, loadBalancersClient.NewListPager(resourceGroup, nil),
			func(p armnetwork.LoadBalancersClientListResponse) []*armnetwork.LoadBalancer { return p.Value })
	} else {
		loadBalancers, err = listAll(ctx, loadBalancersClient.NewListAllPager(nil),
			func(p armnetwork.LoadBalancersClientListAllResponse) []*armnetwork.LoadBalancer { return p.Value })
	}
	if err != nil {
		return nil, err
	}
	for _, loadBalancer := range loadBalancers {
		resources = append(resources, terraformutils.NewSimpleResource(
			*loadBalancer.ID,
			*loadBalancer.Name,
			"azurerm_lb",
			g.ProviderName))

		id, err := ParseAzureResourceID(*loadBalancer.ID)
		if err != nil {
			return nil, err
		}

		probes, err := g.listLoadBalancerProbes(id.ResourceGroup, *loadBalancer.Name)
		if err != nil {
			return nil, err
		}
		resources = append(resources, probes...)

		inboundNatRules, err := g.listInboundNatRules(id.ResourceGroup, *loadBalancer.Name)
		if err != nil {
			return nil, err
		}
		resources = append(resources, inboundNatRules...)

		backendAddressPools, err := g.listLoadBalancerBackendAddressPools(id.ResourceGroup, *loadBalancer.Name)
		if err != nil {
			return nil, err
		}
		resources = append(resources, backendAddressPools...)
	}

	return resources, nil
}

func (g *LoadBalancerGenerator) InitResources() error {
	functions := []func() ([]terraformutils.Resource, error){
		g.listAndAddForLoadBalancers,
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
