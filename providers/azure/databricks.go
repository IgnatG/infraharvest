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
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/databricks/armdatabricks/v2"
)

type DatabricksGenerator struct {
	AzureService
}

func (az *DatabricksGenerator) listWorkspaces() ([]*armdatabricks.Workspace, error) {
	subscriptionID, resourceGroup, credential, options := az.getClientArgs()
	client, err := armdatabricks.NewWorkspacesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := az.Context()
	if resourceGroup != "" {
		return listAll(ctx, client.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armdatabricks.WorkspacesClientListByResourceGroupResponse) []*armdatabricks.Workspace {
				return p.Value
			})
	}
	return listAll(ctx, client.NewListBySubscriptionPager(nil),
		func(p armdatabricks.WorkspacesClientListBySubscriptionResponse) []*armdatabricks.Workspace {
			return p.Value
		})
}

func (az *DatabricksGenerator) AppendWorkspace(workspace *armdatabricks.Workspace) {
	az.AppendSimpleResource(*workspace.ID, *workspace.Name, "azurerm_databricks_workspace")
}

func (az *DatabricksGenerator) InitResources() error {

	workspaces, err := az.listWorkspaces()
	if err != nil {
		return err
	}
	for _, workspace := range workspaces {
		az.AppendWorkspace(workspace)
	}
	return nil
}
