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
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/synapse/armsynapse"
)

// synapseManagedPrivateEndpointsAPIVersion is the Synapse data plane API
// version for managed private endpoints.
const synapseManagedPrivateEndpointsAPIVersion = "2019-06-01-preview"

type SynapseGenerator struct {
	AzureService
}

func (az *SynapseGenerator) listWorkspaces() ([]*armsynapse.Workspace, error) {
	subscriptionID, resourceGroup, credential, options := az.getClientArgs()
	client, err := armsynapse.NewWorkspacesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if resourceGroup != "" {
		return listAll(ctx, client.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armsynapse.WorkspacesClientListByResourceGroupResponse) []*armsynapse.Workspace { return p.Value })
	}
	return listAll(ctx, client.NewListPager(nil),
		func(p armsynapse.WorkspacesClientListResponse) []*armsynapse.Workspace { return p.Value })
}

func (az *SynapseGenerator) appendWorkspace(workspace *armsynapse.Workspace) {
	az.AppendSimpleResource(*workspace.ID, *workspace.Name, "azurerm_synapse_workspace")
}

func (az *SynapseGenerator) appendSQLPools(workspace *armsynapse.Workspace, workspaceRg *ResourceID) error {
	subscriptionID, _, credential, options := az.getClientArgs()
	client, err := armsynapse.NewSQLPoolsClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := context.Background()
	pools, err := listAll(ctx, client.NewListByWorkspacePager(workspaceRg.ResourceGroup, *workspace.Name, nil),
		func(p armsynapse.SQLPoolsClientListByWorkspaceResponse) []*armsynapse.SQLPool { return p.Value })
	for _, item := range pools {
		az.AppendSimpleResource(*item.ID, *item.Name, "azurerm_synapse_sql_pool")
	}
	return err
}

func (az *SynapseGenerator) appendSparkPools(workspace *armsynapse.Workspace, workspaceRg *ResourceID) error {
	subscriptionID, _, credential, options := az.getClientArgs()
	client, err := armsynapse.NewBigDataPoolsClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := context.Background()
	pools, err := listAll(ctx, client.NewListByWorkspacePager(workspaceRg.ResourceGroup, *workspace.Name, nil),
		func(p armsynapse.BigDataPoolsClientListByWorkspaceResponse) []*armsynapse.BigDataPoolResourceInfo {
			return p.Value
		})
	for _, item := range pools {
		az.AppendSimpleResource(*item.ID, *item.Name, "azurerm_synapse_spark_pool")
	}
	return err
}

func (az *SynapseGenerator) appendFirewallRule(workspace *armsynapse.Workspace, workspaceRg *ResourceID) error {
	subscriptionID, _, credential, options := az.getClientArgs()
	client, err := armsynapse.NewIPFirewallRulesClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := context.Background()
	rules, err := listAll(ctx, client.NewListByWorkspacePager(workspaceRg.ResourceGroup, *workspace.Name, nil),
		func(p armsynapse.IPFirewallRulesClientListByWorkspaceResponse) []*armsynapse.IPFirewallRuleInfo {
			return p.Value
		})
	for _, item := range rules {
		az.AppendSimpleResource(*item.ID, *item.Name, "azurerm_synapse_firewall_rule")
	}
	return err
}

// synapseManagedPrivateEndpoint is the part of a Synapse managed private
// endpoint the lister records.
type synapseManagedPrivateEndpoint struct {
	ID   *string `json:"id"`
	Name *string `json:"name"`
}

type synapseManagedPrivateEndpointList struct {
	Value    []*synapseManagedPrivateEndpoint `json:"value"`
	NextLink *string                          `json:"nextLink"`
}

// synapseDevScope returns the token scope for a Synapse workspace
// development endpoint: https://<workspace>.dev.azuresynapse.net takes tokens
// for https://dev.azuresynapse.net (and likewise in the other clouds).
func synapseDevScope(devEndpoint string) (string, error) {
	u, err := url.Parse(devEndpoint)
	if err != nil {
		return "", fmt.Errorf("parsing the Synapse development endpoint %q: %w", devEndpoint, err)
	}
	_, audienceHost, found := strings.Cut(u.Hostname(), ".")
	if !found || audienceHost == "" {
		return "", fmt.Errorf("unexpected Synapse development endpoint %q", devEndpoint)
	}
	return "https://" + audienceHost + "/.default", nil
}

// listSynapseManagedPrivateEndpoints lists the managed private endpoints of a
// workspace's managed virtual network from the Synapse data plane, which
// Azure Resource Manager does not expose.
func listSynapseManagedPrivateEndpoints(ctx context.Context, credential azcore.TokenCredential, options *arm.ClientOptions, devEndpoint, virtualNetworkName string) ([]*synapseManagedPrivateEndpoint, error) {
	scope, err := synapseDevScope(devEndpoint)
	if err != nil {
		return nil, err
	}
	var clientOptions policy.ClientOptions
	if options != nil {
		clientOptions = options.ClientOptions
	}
	pipeline := runtime.NewPipeline("infraharvest", "", runtime.PipelineOptions{
		PerRetry: []policy.Policy{runtime.NewBearerTokenPolicy(credential, []string{scope}, nil)},
	}, &clientOptions)

	next := strings.TrimSuffix(devEndpoint, "/") + "/managedVirtualNetworks/" + url.PathEscape(virtualNetworkName) +
		"/managedPrivateEndpoints?api-version=" + synapseManagedPrivateEndpointsAPIVersion
	var endpoints []*synapseManagedPrivateEndpoint
	for next != "" {
		req, err := runtime.NewRequest(ctx, http.MethodGet, next)
		if err != nil {
			return endpoints, err
		}
		resp, err := pipeline.Do(req)
		if err != nil {
			return endpoints, err
		}
		if !runtime.HasStatusCode(resp, http.StatusOK) {
			return endpoints, runtime.NewResponseError(resp)
		}
		var page synapseManagedPrivateEndpointList
		if err := runtime.UnmarshalAsJSON(resp, &page); err != nil {
			return endpoints, err
		}
		for _, endpoint := range page.Value {
			if endpoint != nil && endpoint.ID != nil && endpoint.Name != nil {
				endpoints = append(endpoints, endpoint)
			}
		}
		next = ""
		if page.NextLink != nil {
			next = *page.NextLink
		}
	}
	return endpoints, nil
}

func (az *SynapseGenerator) appendManagedPrivateEndpoint(workspace *armsynapse.Workspace) error {

	if workspace.Properties == nil || workspace.Properties.ManagedVirtualNetwork == nil {
		return nil
	}
	// A workspace with a managed virtual network names it "default"; without
	// one the property is empty, and there are no managed private endpoints.
	virtualNetworkName := *workspace.Properties.ManagedVirtualNetwork
	if virtualNetworkName == "" {
		return nil
	}
	devEndpoint := ""
	if endpoint := workspace.Properties.ConnectivityEndpoints["dev"]; endpoint != nil {
		devEndpoint = *endpoint
	}
	if devEndpoint == "" {
		return fmt.Errorf("synapse workspace %s has no development endpoint", *workspace.Name)
	}
	_, _, credential, options := az.getClientArgs()
	endpoints, err := listSynapseManagedPrivateEndpoints(context.Background(), credential, options, devEndpoint, virtualNetworkName)
	for _, item := range endpoints {
		az.AppendSimpleResource(*item.ID, *item.Name, "azurerm_synapse_managed_private_endpoint")
	}
	return err
}

func (az *SynapseGenerator) listPrivateLinkHubs() ([]*armsynapse.PrivateLinkHub, error) {
	subscriptionID, resourceGroup, credential, options := az.getClientArgs()
	client, err := armsynapse.NewPrivateLinkHubsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if resourceGroup != "" {
		return listAll(ctx, client.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armsynapse.PrivateLinkHubsClientListByResourceGroupResponse) []*armsynapse.PrivateLinkHub {
				return p.Value
			})
	}
	return listAll(ctx, client.NewListPager(nil),
		func(p armsynapse.PrivateLinkHubsClientListResponse) []*armsynapse.PrivateLinkHub { return p.Value })
}

func (az *SynapseGenerator) appendtPrivateLinkHubs(workspace *armsynapse.PrivateLinkHub) {
	az.AppendSimpleResource(*workspace.ID, *workspace.Name, "azurerm_synapse_private_link_hub")
}

func (az *SynapseGenerator) InitResources() error {

	workspaces, err := az.listWorkspaces()
	if err != nil {
		return err
	}
	for _, workspace := range workspaces {
		az.appendWorkspace(workspace)
		workspaceRg, err := ParseAzureResourceID(*workspace.ID)
		if err != nil {
			return err
		}
		err = az.appendSQLPools(workspace, workspaceRg)
		if err != nil {
			return err
		}
		err = az.appendSparkPools(workspace, workspaceRg)
		if err != nil {
			return err
		}
		err = az.appendFirewallRule(workspace, workspaceRg)
		if err != nil {
			return err
		}
		err = az.appendManagedPrivateEndpoint(workspace)
		if err != nil {
			return err
		}
	}

	hubs, err := az.listPrivateLinkHubs()
	if err == nil {
		for _, hub := range hubs {
			az.appendtPrivateLinkHubs(hub)
		}
	}
	return nil
}
