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

// List lists the security groups and their rules. The rules come from
// the group list, which carries them; only a group that came back without
// them, as some emulators answer, has its rules listed on their own. Any
// listing that fails fails the service.
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
		rules, err := az.rulesOf(rulesClient, group)
		if err != nil {
			return err
		}
		for _, rule := range rules {
			if rule == nil || rule.ID == nil || rule.Name == nil {
				continue
			}
			// The same rule name often appears in many groups, so the
			// group's name leads the label.
			az.recordBare(*rule.ID, *group.Name+"_"+*rule.Name, "azurerm_network_security_rule")
		}
	}
	return nil
}

// rulesOf returns the rules group carries in its properties, which a
// Resource Manager list returns in full. Only when group came back without
// them are they listed with rulesClient, under the group's own resource
// group, which is not --resource-group when the whole subscription is
// listed. In a custom cloud an empty list is asked again too, as some
// emulators answer one; Azure's own clouds list every rule with its group,
// so there a group without rules costs no request.
func (az *NetworkSecurityGroupLister) rulesOf(rulesClient *armnetwork.SecurityRulesClient, group *armnetwork.SecurityGroup) ([]*armnetwork.SecurityRule, error) {
	if group.Properties != nil && listedInFull(group.Properties.SecurityRules, az.customCloud()) {
		return group.Properties.SecurityRules, nil
	}
	groupResourceGroup, err := resourceGroupOf(*group.ID)
	if err != nil {
		return nil, err
	}
	return listAll(az.Context(), rulesClient.NewListPager(groupResourceGroup, *group.Name, nil),
		func(p armnetwork.SecurityRulesClientListResponse) []*armnetwork.SecurityRule { return p.Value })
}
