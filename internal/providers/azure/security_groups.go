// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v11"
)

// NetworkSecurityGroupLister lists network security groups as
// azurerm_network_security_group, in --resource-group when it is set, else
// in the whole subscription, and the rules of each group as
// azurerm_network_security_rule. The default rules Azure adds to every
// group are not listed: they can't be managed.
type NetworkSecurityGroupLister struct {
	Service
}

// List lists the security groups and their rules. Any listing that
// fails fails the service.
func (az *NetworkSecurityGroupLister) List() error {
	subscriptionID, resourceGroup, credential, options := az.clientSettings()
	groupsClient, err := armnetwork.NewSecurityGroupsClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	rulesClient, err := armnetwork.NewSecurityRulesClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := az.Context()
	var groups []*armnetwork.SecurityGroup
	if resourceGroup != "" {
		groups, err = listAll(ctx, groupsClient.NewListPager(resourceGroup, nil),
			func(p armnetwork.SecurityGroupsClientListResponse) []*armnetwork.SecurityGroup { return p.Value })
	} else {
		groups, err = listAll(ctx, groupsClient.NewListAllPager(nil),
			func(p armnetwork.SecurityGroupsClientListAllResponse) []*armnetwork.SecurityGroup { return p.Value })
	}
	if err != nil {
		return err
	}
	for _, group := range groups {
		if group.ID == nil || group.Name == nil {
			continue
		}
		az.recordTagged(*group.ID, *group.Name, "azurerm_network_security_group", group.Tags)
		// Rules are listed under the group's own resource group, which is
		// not --resource-group when the whole subscription is listed.
		groupResourceGroup, err := resourceGroupOf(*group.ID)
		if err != nil {
			return err
		}
		rules, err := listAll(ctx, rulesClient.NewListPager(groupResourceGroup, *group.Name, nil),
			func(p armnetwork.SecurityRulesClientListResponse) []*armnetwork.SecurityRule { return p.Value })
		if err != nil {
			return err
		}
		for _, rule := range rules {
			if rule.ID == nil || rule.Name == nil {
				continue
			}
			// The same rule name often appears in many groups, so the
			// group's name leads the label.
			az.recordBare(*rule.ID, *group.Name+"_"+*rule.Name, "azurerm_network_security_rule")
		}
	}
	return nil
}
