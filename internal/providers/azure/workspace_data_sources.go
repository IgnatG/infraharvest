// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/operationalinsights/armoperationalinsights/v3"
	"github.com/magodo/armid"
	"github.com/magodo/azlist/azlist"
	"github.com/magodo/aztft/aztft"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// workspaceType is the Resource Manager type of a Log Analytics workspace.
const workspaceType = "Microsoft.OperationalInsights/workspaces"

// workspaceDataSourceKinds are the kinds of Log Analytics workspace data
// source azurerm has a resource type for, with that type. Resource Manager
// lists a workspace's data sources one kind at a time: without a
// $filter=kind eq '...' it answers 400 KindNotSpecified. So only these
// kinds are asked for, one list each per workspace, and none of the
// others, which azurerm can't manage.
var workspaceDataSourceKinds = []struct {
	kind   armoperationalinsights.DataSourceKind
	tfType string
}{
	{armoperationalinsights.DataSourceKindWindowsEvent, "azurerm_log_analytics_datasource_windows_event"},
	{armoperationalinsights.DataSourceKindWindowsPerformanceCounter, "azurerm_log_analytics_datasource_windows_performance_counter"},
}

// withoutWorkspaceDataSources stops azlist listing the data sources of
// workspaces as their child resources, which it would do without a kind
// and fail (see workspaceDataSourceKinds). tree is the lister's own copy
// of the schema tree, built for it.
func withoutWorkspaceDataSources(tree azlist.ARMSchemaTree) {
	if workspace := tree[strings.ToUpper(workspaceType)]; workspace != nil {
		delete(workspace.Children, "DATASOURCES")
	}
}

// workspaceDataSources lists the data sources of the workspaces in listed
// that azurerm has a resource type for, and the import wants (see
// Service.wants), with one filtered list per kind and workspace. It returns them under the IDs azurerm imports them by,
// with their azurerm types by upper-case ID, so they need no further call
// to tell their kind. A list that fails leaves the others to go on, and
// the failures come back joined as a partial error (see harvest.Partial)
// together with what was listed: a workspace's data sources that a
// refused or throttled list left out are missing from the import, which
// must not read as complete. A workspace gone since it was listed (404)
// is no failure, as nothing that exists is left out. Only a cancelled
// context, or a client that can't be built, is a full failure.
func workspaceDataSources(ctx context.Context, credential azcore.TokenCredential, options *arm.ClientOptions, listed []azlist.AzureResource, wants func(typ string) bool) ([]azlist.AzureResource, map[string]string, error) {
	clients := map[string]*armoperationalinsights.DataSourcesClient{}
	var found []azlist.AzureResource
	var failures []error
	types := map[string]string{}
	for _, res := range listed {
		if !strings.EqualFold(res.Id.TypeString(), workspaceType) {
			continue
		}
		group, ok := res.Id.RootScope().(*armid.ResourceGroup)
		if !ok {
			continue
		}
		workspace := lastSegment(res.Id.String())
		client := clients[group.SubscriptionId]
		if client == nil {
			var err error
			if client, err = armoperationalinsights.NewDataSourcesClient(group.SubscriptionId, credential, options); err != nil {
				return nil, nil, err
			}
			clients[group.SubscriptionId] = client
		}
		for _, k := range workspaceDataSourceKinds {
			if !wants(k.tfType) {
				continue
			}
			pager := client.NewListByWorkspacePager(group.Name, workspace, "kind eq '"+string(k.kind)+"'", nil)
			sources, err := listAll(ctx, pager, func(p armoperationalinsights.DataSourcesClientListByWorkspaceResponse) []*armoperationalinsights.DataSource {
				return p.Value
			})
			if err != nil {
				if ctx.Err() != nil {
					return nil, nil, ctx.Err()
				}
				var respErr *azcore.ResponseError
				if errors.As(err, &respErr) && respErr.StatusCode == http.StatusNotFound {
					log.Printf("azurerm: resource_graph: workspace %s is gone: %v", res.Id, err)
					continue
				}
				failures = append(failures, fmt.Errorf("azurerm: resource_graph: listing the %s data sources of %s: %w", k.kind, res.Id, err))
				continue
			}
			for _, source := range sources {
				name := ""
				switch {
				case source.ID != nil:
					name = lastSegment(*source.ID)
				case source.Name != nil:
					name = lastSegment(*source.Name)
				}
				if name == "" {
					continue
				}
				// The form azurerm parses, whatever the case of the ID
				// Resource Manager returned.
				id := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/%s/%s/dataSources/%s", group.SubscriptionId, group.Name, workspaceType, workspace, name)
				parsed, err := armid.ParseResourceId(id)
				if err != nil {
					log.Printf("azurerm: resource_graph: data source %s: %v", id, err)
					continue
				}
				properties := map[string]interface{}{"kind": string(k.kind)}
				if len(source.Tags) > 0 {
					tags := map[string]interface{}{}
					for key, value := range source.Tags {
						if value != nil {
							tags[key] = *value
						}
					}
					properties["tags"] = tags
				}
				found = append(found, azlist.AzureResource{Id: parsed, Properties: properties})
				types[strings.ToUpper(id)] = k.tfType
			}
		}
	}
	return found, types, harvest.Partial(errors.Join(failures...))
}

// knownTypes is query, except for the resources whose azurerm type is
// already known, by upper-case ID, such as workspace data sources (see
// workspaceDataSources), which it answers without asking aztft, and so
// without a call to Resource Manager.
func knownTypes(known map[string]string, query queryType) queryType {
	return func(id string) ([]aztft.Type, []string, bool, error) {
		tfType, ok := known[strings.ToUpper(id)]
		if !ok {
			return query(id)
		}
		parsed, err := armid.ParseResourceId(id)
		if err != nil {
			return nil, nil, false, err
		}
		return []aztft.Type{{AzureId: parsed, TFType: tfType}}, []string{id}, true, nil
	}
}
