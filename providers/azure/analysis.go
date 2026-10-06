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
	"log"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/analysisservices/armanalysisservices"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type AnalysisGenerator struct {
	AzureService
}

func (g *AnalysisGenerator) listServiceServers() ([]terraformutils.Resource, error) {
	log.Println("\tImporting Service Servers")
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	client, err := armanalysisservices.NewServersClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	var servers []*armanalysisservices.Server
	if resourceGroup != "" {
		servers, err = listAll(ctx, client.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armanalysisservices.ServersClientListByResourceGroupResponse) []*armanalysisservices.Server {
				return p.Value
			})
	} else {
		servers, err = listAll(ctx, client.NewListPager(nil),
			func(p armanalysisservices.ServersClientListResponse) []*armanalysisservices.Server { return p.Value })
	}
	if err != nil {
		return nil, err
	}
	for _, svr := range servers {
		resources = append(resources, terraformutils.NewSimpleResource(
			*svr.ID,
			*svr.Name,
			"azurerm_analysis_services_server",
			g.ProviderName))
	}

	return resources, nil
}

func (g *AnalysisGenerator) InitResources() error {
	functions := []func() ([]terraformutils.Resource, error){
		g.listServiceServers,
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
