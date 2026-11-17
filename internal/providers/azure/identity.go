// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"regexp"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The keys of an environment's azure section, beside subscriptions, that
// name the identity its subscriptions are read with, which also name them
// in the inventory key (as key=value; see Settings.InventoryKey).
const (
	tenantIDKey = "tenant_id"
	clientIDKey = "client_id"
)

var (
	guid = `[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}`
	// tenantReference matches a tenant's ID, or its domain name.
	tenantReference = regexp.MustCompile(`^(` + guid + `|[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+)$`)
	clientReference = regexp.MustCompile(`^` + guid + `$`)
)

// IdentityKeys are the keys of an environment's azure section, beside
// subscriptions, that name the identity its subscriptions are read with:
// tenant_id, the Entra tenant to sign in to, which the Azure CLI signs in
// to with its own login, and client_id, the application or user-assigned
// managed identity to sign in as, with the federated (OIDC), managed
// identity or other settings the ARM_* variables hold; they replace
// ARM_TENANT_ID and ARM_CLIENT_ID for those subscriptions. Neither is a
// secret: a client secret or certificate stays in the environment.
func IdentityKeys() []harvest.IdentityKey {
	return []harvest.IdentityKey{
		{Key: tenantIDKey, Match: tenantReference, Format: "a tenant ID (GUID) or domain name"},
		{Key: clientIDKey, Match: clientReference, Format: "a client ID (GUID)"},
	}
}

// TerraformEnv gives Terraform the tenant and client an environment says
// its subscription is read with, so that the plans of its roots sign in
// as the listing did; none, for a subscription read with the ARM_*
// variables, which Terraform has already.
func (p *Provider) TerraformEnv(context.Context) (map[string]string, error) {
	env := map[string]string{}
	if p.tenantID != "" {
		env["ARM_TENANT_ID"] = p.tenantID
	}
	if p.clientID != "" {
		env["ARM_CLIENT_ID"] = p.clientID
	}
	return env, nil
}
