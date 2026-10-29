// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/providers/providertest"
)

// Every lister of the provider's services keeps the listers' contract
// (see providertest.Contract) against a fake Resource Manager: nothing
// listed from an empty subscription; only the types it declares listed
// from one with a resource at every list it asks for; a refusal (403)
// classified by azureCodes as a failure; and a canceled context honoured.
// The query service lists through Terraform, not Resource Manager.
func TestListerContract(t *testing.T) {
	t.Parallel()
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for name := range services {
			if name != harvest.QueryService && !yield(name) {
				return
			}
		}
	}) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			providertest.Contract{
				Declared: declaredTypes(name),
				List: func(ctx context.Context, _ *testing.T, cloud *providertest.Cloud) ([]harvest.Resource, error) {
					g := services[name]().(settledLister)
					args := fakeArgs(cloud, "")
					args.subscriptionID = contractSubscription
					g.SetSettings(args)
					return g.List(ctx, harvest.Settings{Provider: "azurerm"})
				},
				Empty:       answering(`{"value":[],"data":[]}`),
				Listed:      listedAnswer(name),
				Denied:      func(r providertest.Request) providertest.Answer { return armError(http.StatusForbidden, r.Path) },
				DeniedCodes: []string{"403"},
				Codes:       azureCodes,
			}.Run(t)
		})
	}
}

// contractSubscription is the subscription the contract lists, as Azure
// names one, and contractSubscriptionPath its path.
const (
	contractSubscription     = "00000000-0000-0000-0000-000000000001"
	contractSubscriptionPath = "/subscriptions/" + contractSubscription
)

// declaredTypes are the types the service called name declares it lists
// (see listedTypes): those it lists first, and for a service that lists
// what resource_graph lists too (see resourceManagerServices),
// resource_graph's.
func declaredTypes(name string) []string {
	var declared []string
	for typ, service := range listedTypes {
		if service == name || service == resourceGraphService && slices.Contains(resourceManagerServices, name) {
			declared = append(declared, typ)
		}
	}
	return declared
}

// listedAnswer answers every list the service called name asks for with
// a resource under the list's path, in a resource group, or is nil for
// the services whose tests list their resources: resource_graph and
// role_assignment, which query Resource Graph, and diagnostic_setting,
// which lists the settings of what the other services listed.
func listedAnswer(name string) providertest.Handler {
	switch name {
	case resourceGraphService, "role_assignment", diagnosticSettingService:
		return nil
	}
	return func(r providertest.Request) providertest.Answer {
		id := strings.Replace(r.Path, contractSubscriptionPath+"/providers/", contractSubscriptionPath+"/resourceGroups/rg1/providers/", 1) + "/one"
		return providertest.JSON(armList(armItem(id, "one", "")))
	}
}
