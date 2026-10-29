// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"reflect"
	"slices"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// A lister asks for the parts of each resource it lists only when the
// import may keep their type: one whose child types the import leaves
// out whatever is listed asks only for the parents, and lists none of
// the children.
func TestListersSkipTheCallsOfUnwantedTypes(t *testing.T) {
	t.Parallel()
	const (
		rg       = "/subscriptions/sub/resourceGroups/rg1/providers/"
		ids      = rg + "Microsoft.ManagedIdentity/userAssignedIdentities"
		groups   = rg + "Microsoft.Network/networkSecurityGroups"
		networks = rg + "Microsoft.Network/virtualNetworks"
		policies = rg + "Microsoft.Network/serviceEndpointPolicies"
	)
	for _, tc := range []struct {
		name     string
		lister   func() settledLister
		bodies   map[string]string
		unwanted []string
		// wanted and unwanted are the paths asked for with every type
		// wanted, and with unwanted left out.
		wantedPaths, unwantedPaths []string
		parent                     string
	}{
		{
			name:   "federated identity credentials",
			lister: func() settledLister { return &ManagedIdentityLister{} },
			bodies: map[string]string{
				ids:                                      armList(armItem(ids+"/ci", "ci", "")),
				ids + "/ci/federatedIdentityCredentials": armList(armItem(ids+"/ci/federatedIdentityCredentials/gh", "gh", "")),
			},
			unwanted:      []string{"azurerm_federated_identity_credential"},
			wantedPaths:   []string{ids, ids + "/ci/federatedIdentityCredentials"},
			unwantedPaths: []string{ids},
			parent:        "azurerm_user_assigned_identity",
		},
		{
			name:   "security rules",
			lister: func() settledLister { return &NetworkSecurityGroupLister{} },
			bodies: map[string]string{
				groups:                        armList(armItem(groups+"/web", "web", "")),
				groups + "/web/securityRules": armList(armItem(groups+"/web/securityRules/ssh", "ssh", "")),
			},
			unwanted:      []string{"azurerm_network_security_rule"},
			wantedPaths:   []string{groups, groups + "/web/securityRules"},
			unwantedPaths: []string{groups},
			parent:        "azurerm_network_security_group",
		},
		{
			name:   "subnets and their associations",
			lister: func() settledLister { return &SubnetLister{} },
			bodies: map[string]string{
				networks:                   armList(armItem(networks+"/net1", "net1", "")),
				networks + "/net1/subnets": armList(armItem(networks+"/net1/subnets/app", "app", "")),
				policies:                   armList(armItem(policies+"/storage", "storage", "")),
			},
			unwanted:      subnetTypes,
			wantedPaths:   []string{networks, networks + "/net1/subnets", policies},
			unwantedPaths: []string{policies},
			parent:        "azurerm_subnet_service_endpoint_storage_policy",
		},
	} {
		for _, unwanted := range []bool{false, true} {
			transport := routed(tc.bodies)
			settings := harvest.Settings{}
			if unwanted {
				settings.Unwanted = func(typ string) bool { return slices.Contains(tc.unwanted, typ) }
			}
			lister := tc.lister()
			lister.SetSettings(fakeArgs(transport, "rg1"))
			resources, err := lister.List(t.Context(), settings)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			want := tc.wantedPaths
			if unwanted {
				want = tc.unwantedPaths
			}
			if !reflect.DeepEqual(transport.Paths(), want) {
				t.Errorf("%s, unwanted %v: requested %v, want %v", tc.name, unwanted, transport.Paths(), want)
			}
			parents, children := 0, 0
			for _, r := range resources {
				switch {
				case r.Address.Type == tc.parent:
					parents++
				case slices.Contains(tc.unwanted, r.Address.Type):
					children++
				}
			}
			if parents == 0 || (children == 0) != unwanted {
				t.Errorf("%s, unwanted %v: listed %d %s and %d of %v", tc.name, unwanted, parents, tc.parent, children, tc.unwanted)
			}
		}
	}
}

// diagnostic_setting asks no resource for its settings when the import
// leaves them out whatever is listed.
func TestDiagnosticSettingSkipsUnwantedSettings(t *testing.T) {
	t.Parallel()
	transport := routed(nil)
	g := &DiagnosticSettingLister{}
	g.SetSettings(diagnosticArgs(transport, "", listedByOthers()))
	unwanted := func(typ string) bool { return typ == diagnosticSettingType }
	resources, err := g.List(t.Context(), harvest.Settings{Provider: "azurerm", Unwanted: unwanted})
	if err != nil {
		t.Fatal(err)
	}
	if len(transport.Paths()) != 0 || len(resources) != 0 {
		t.Errorf("asked for %v and listed %d, want neither", transport.Paths(), len(resources))
	}
}
