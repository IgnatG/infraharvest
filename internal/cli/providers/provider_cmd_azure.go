// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal || azure

package providers

import (
	"context"
	"errors"
	"log"

	"github.com/spf13/pflag"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
	"github.com/infraharvest/infraharvest-cli/internal/importer"
	"github.com/infraharvest/infraharvest-cli/internal/providers/azure"
)

func init() {
	define(definition{
		name:        "azure",
		newProvider: func() harvest.Provider { return &azure.Provider{} },
		short:       "Generate Terraform configuration for the resources of Azure subscriptions",
		long: "List the resources of the services --services names in an Azure subscription,\n" +
			"or in one of its resource groups with --resource-group, and generate their\n" +
			"Terraform configuration with import blocks. The subscription is\n" +
			"ARM_SUBSCRIPTION_ID's, unless --subscriptions or --management-group names\n" +
			"several, each imported into roots of its own. Nothing is applied and no state\n" +
			"is written.",
		sampleServices: "resource_graph,management_lock",
		sampleIDs:      "management_lock=LOCK_ID",
		parallelScopes: "subscriptions of --subscriptions or --management-group",
		flags:          azureFlags,
	})
}

// azureScopes are the Azure command's flags that aren't options: what
// widens the import to several subscriptions.
type azureScopes struct {
	subscriptions   []string
	managementGroup string
}

// azureFlags adds the Azure command's own flags.
func azureFlags(flags *pflag.FlagSet, options *importer.Options) planner {
	scopes := &azureScopes{}
	flags.StringVarP(&options.ResourceGroup, "resource-group", "g", "", "resource group to import (default: the whole subscription)")
	flags.StringSliceVar(&scopes.subscriptions, "subscriptions", nil, "subscription IDs to import, each into roots of its own (default ARM_SUBSCRIPTION_ID)")
	flags.StringVar(&scopes.managementGroup, "management-group", "", "import every enabled subscription under this management group ID, in its child groups too (needs Reader on the group)")
	return scopes.plan
}

// plan imports ARM_SUBSCRIPTION_ID's subscription or, with --subscriptions
// or --management-group, each of theirs.
func (s *azureScopes) plan(ctx context.Context, options importer.Options) (plan, error) {
	subscriptions := s.subscriptions
	if s.managementGroup != "" {
		found, err := azure.Subscriptions(ctx, s.managementGroup)
		if err != nil {
			return plan{}, err
		}
		if len(found) == 0 {
			return plan{}, errors.New("no enabled subscriptions under --management-group " + s.managementGroup)
		}
		log.Printf("azurerm: %d subscriptions under management group %s", len(found), s.managementGroup)
		subscriptions = appendMissing(subscriptions, found)
	}

	if len(subscriptions) == 0 {
		// The provider takes the subscription from ARM_SUBSCRIPTION_ID.
		return plan{calls: func(scopeOptions importer.Options, _ string) []call {
			return []call{{options: scopeOptions, args: []string{scopeOptions.ResourceGroup}}}
		}}, nil
	}
	if len(subscriptions) > 1 {
		if err := checkScopesApart(options.Layout, "subscription"); err != nil {
			return plan{}, err
		}
	}
	return plan{kind: "subscription", scopes: subscriptions, calls: func(scopeOptions importer.Options, subscription string) []call {
		return []call{{options: scopeOptions, args: []string{scopeOptions.ResourceGroup, subscription}}}
	}}, nil
}
