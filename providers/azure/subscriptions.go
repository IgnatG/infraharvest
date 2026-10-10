// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resourcegraph/armresourcegraph"
)

// subscriptionsQuery lists subscriptions with their state.
const subscriptionsQuery = "resourcecontainers | where type =~ 'microsoft.resources/subscriptions'" +
	" | project subscriptionId, state = tostring(properties.state)"

// Subscriptions returns the IDs of the subscriptions under the management
// group (its ID), in its child management groups too, that aren't disabled
// or deleted, sorted. It signs in as an import does, without a
// subscription, and asks Resource Graph, which needs Reader (or
// Microsoft.Management/managementGroups/read) on the group.
func Subscriptions(ctx context.Context, managementGroup string) ([]string, error) {
	cfg, err := loadAuthConfig(os.Getenv)
	if err != nil {
		return nil, err
	}
	_, credential, options, err := signIn(cfg)
	if err != nil {
		return nil, err
	}
	return subscriptionsIn(ctx, credential, options, managementGroup)
}

func subscriptionsIn(ctx context.Context, credential azcore.TokenCredential, options *arm.ClientOptions, managementGroup string) ([]string, error) {
	if !resourceGroupName.MatchString(managementGroup) {
		return nil, fmt.Errorf("management group ID %q has characters Azure doesn't allow", managementGroup)
	}
	client, err := armresourcegraph.NewClient(credential, options)
	if err != nil {
		return nil, err
	}
	request := armresourcegraph.QueryRequest{
		Query:            to.Ptr(subscriptionsQuery),
		ManagementGroups: []*string{to.Ptr(managementGroup)},
		Options:          &armresourcegraph.QueryRequestOptions{ResultFormat: to.Ptr(armresourcegraph.ResultFormatObjectArray)},
	}
	var subscriptions []string
	for {
		resp, err := client.Resources(ctx, request, nil)
		if err != nil {
			return nil, fmt.Errorf("subscriptions of management group %s: %w", managementGroup, err)
		}
		rows, _ := resp.Data.([]interface{})
		for _, row := range rows {
			m, _ := row.(map[string]interface{})
			id, _ := m["subscriptionId"].(string)
			state, _ := m["state"].(string)
			if id == "" || strings.EqualFold(state, "Disabled") || strings.EqualFold(state, "Deleted") {
				continue
			}
			if !slices.Contains(subscriptions, id) {
				subscriptions = append(subscriptions, id)
			}
		}
		if resp.SkipToken == nil || *resp.SkipToken == "" {
			break
		}
		request.Options.SkipToken = resp.SkipToken
	}
	slices.Sort(subscriptions)
	return subscriptions, nil
}
