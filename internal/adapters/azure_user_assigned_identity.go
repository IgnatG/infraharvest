// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"strconv"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// Azure Verified Modules: every call turns telemetry off, which also
// keeps the modules from reading azapi's client configuration. AVM's
// resource group, network security group, virtual network and storage
// modules manage their resources as azapi_resource: imported into them,
// azapi plans an update of its own arguments, such as retry and timeouts,
// and of body properties the modules set to null, so no adapter maps onto
// them.

// AzureUserAssignedIdentity maps a user-assigned identity and its
// federated identity credentials onto Azure Verified Modules' managed
// identity module, which keys the credentials by name. Release 0.5.3,
// the newest, accepts azurerm below 5 only: an import doesn't call a
// module that would hold the root a major release behind, so while
// azurerm 5 is the newest, identities stay in the root as plain
// resources. The adapter applies again once it is bumped to a release
// that accepts azurerm 5, or with --modules latest-untested once one is
// out. A release that gets there by managing the identity as
// azapi_resource, as AVM's network and storage modules do, keeps the
// variables and outputs but not the resources the adapter maps onto
// (Targets): the adaptercheck report says it doesn't fit.
var AzureUserAssignedIdentity = Adapter{
	Source:   "Azure/avm-res-managedidentity-userassignedidentity/azurerm",
	Version:  "0.5.3",
	Anchor:   "azurerm_user_assigned_identity",
	Members:  []string{"azurerm_federated_identity_credential"},
	Repeated: []string{"azurerm_federated_identity_credential"},
	Inputs: []string{
		"enable_telemetry", "federated_identity_credentials", "isolation_scope", "location", "name",
		"resource_group_name", "tags",
	},
	Outputs:   []string{"client_id", "principal_id", "resource", "resource_id", "resource_name", "tenant_id"},
	Providers: []string{"azapi"},
	Targets:   []string{"azurerm_federated_identity_credential.this", "azurerm_user_assigned_identity.this"},
	Map:       mapAzureUserAssignedIdentity,
}

func mapAzureUserAssignedIdentity(c Cluster) (*Call, error) {
	call := &Call{
		Addresses: map[string]string{c.Anchor.Address(): "azurerm_user_assigned_identity.this"},
		Outputs: map[string]map[string]string{c.Anchor.Address(): {
			"client_id":    "client_id",
			"id":           "resource_id",
			"location":     "resource.location",
			"name":         "resource_name",
			"principal_id": "principal_id",
			"tenant_id":    "tenant_id",
		}},
	}
	identity := Read(c.Anchor.Address(), c.Anchor.Body)
	if err := identity.Require(call, Pair{"name", "name"}, Pair{"location", "location"}, Pair{"resource_group_name", "resource_group_name"}); err != nil {
		return nil, err
	}
	identity.CopyAs(call, Pair{"isolation_scope", "isolation_scope"}, Pair{"tags", "tags"})
	if err := identity.Done(); err != nil {
		return nil, err
	}

	var credentials []hclwrite.ObjectAttrTokens
	keys := map[string]bool{}
	for _, m := range c.Members {
		r := Read(m.Address(), m.Body)
		// azurerm 4 names the identity by user_assigned_identity_id, and
		// before by parent_id and resource_group_name, as the module does.
		if !r.RefersTo("user_assigned_identity_id", c.Anchor) && !r.RefersTo("parent_id", c.Anchor) {
			return nil, Declinef("%s doesn't refer to %s", m.Address(), c.Anchor.Address())
		}
		r.Ignore("parent_id", "resource_group_name")
		nameTokens, _ := r.Attr("name")
		name, isLiteral := literalString(nameTokens)
		if !isLiteral {
			return nil, Declinef("%s has no literal name to key it by", m.Address())
		}
		if keys[name] {
			return nil, Declinef("%s has two federated credentials named %s", c.Anchor.Address(), name)
		}
		keys[name] = true
		var credential Object
		credential.Set("name", nameTokens)
		for _, arg := range []string{"audience", "issuer", "subject"} {
			value, ok := r.Attr(arg)
			if !ok {
				return nil, Declinef("%s has no %s", m.Address(), arg)
			}
			credential.Set(arg, value)
		}
		if err := r.Done(); err != nil {
			return nil, err
		}
		credentials = append(credentials, hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForValue(cty.StringVal(name)), Value: credential.Tokens()})
		call.Addresses[m.Address()] = "azurerm_federated_identity_credential.this[" + strconv.Quote(name) + "]"
	}
	if len(credentials) > 0 {
		call.Set("federated_identity_credentials", hclwrite.TokensForObject(credentials))
	}
	call.Set("enable_telemetry", hclwrite.TokensForValue(cty.False))
	return call, nil
}
