// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resourcegraph/armresourcegraph"

	"github.com/IgnatG/infraharvest/terraformutils"
)

// tagsQuery reads the tags of every resource and resource group that
// predicate (see graphPredicate) selects.
func tagsQuery(predicate string) string {
	return fmt.Sprintf("resources | where %[1]s | project id, tags"+
		" | union (resourcecontainers | where type =~ 'microsoft.resources/subscriptions/resourcegroups' and %[1]s | project id, tags)", predicate)
}

// Tags reads the tags of the listed resources from Azure Resource Graph,
// in one query for the subscription or --resource-group. Resource IDs
// match whatever their case, as in Azure. Child resources, which Resource
// Graph doesn't index, such as subnets, have no tags of their own. It
// returns them by "type id".
func (p *AzureProvider) Tags(ctx context.Context, resources []terraformutils.Resource) (map[string]map[string]string, error) {
	predicate, err := graphPredicate(p.subscriptionID, p.resourceGroup)
	if err != nil {
		return nil, err
	}
	client, err := armresourcegraph.NewClient(p.credential, p.clientOptions)
	if err != nil {
		return nil, err
	}
	request := armresourcegraph.QueryRequest{
		Query:         to.Ptr(tagsQuery(predicate)),
		Subscriptions: []*string{to.Ptr(p.subscriptionID)},
		Options:       &armresourcegraph.QueryRequestOptions{ResultFormat: to.Ptr(armresourcegraph.ResultFormatObjectArray)},
	}
	byID := map[string]map[string]string{}
	for {
		resp, err := client.Resources(ctx, request, nil)
		if err != nil {
			return nil, fmt.Errorf("tags from Resource Graph: %w", err)
		}
		rows, _ := resp.Data.([]interface{})
		for _, row := range rows {
			m, _ := row.(map[string]interface{})
			id, _ := m["id"].(string)
			if id == "" {
				continue
			}
			if tags := tagMap(m["tags"]); len(tags) > 0 {
				byID[strings.ToLower(id)] = tags
			}
		}
		if resp.SkipToken == nil || *resp.SkipToken == "" {
			break
		}
		request.Options.SkipToken = resp.SkipToken
	}
	found := map[string]map[string]string{}
	for _, r := range resources {
		if tags, ok := byID[strings.ToLower(r.InstanceState.ID)]; ok {
			found[r.InstanceInfo.Type+" "+r.InstanceState.ID] = tags
		}
	}
	return found, nil
}

// tagMap is a resource's tags as Resource Graph returns them, leaving out
// values that aren't strings.
func tagMap(tags interface{}) map[string]string {
	m, _ := tags.(map[string]interface{})
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}
