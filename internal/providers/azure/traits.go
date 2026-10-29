// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import "github.com/IgnatG/infraharvest/internal/harvest"

// traits returns what Azure's resources and the values that identify them
// are like: resource IDs, which start with /subscriptions/, identify a
// resource on their own; the attributes the provider computes that
// identify their resources are a role definition's resource ID, which
// role assignments name it by (its id has the scope too), a managed
// identity's principal and client IDs, and the DNS names and IDs of
// zones, servers and the like, and a resource's system-assigned
// identity, identity[0].principal_id, which a role assignment grants a
// role to; and resources take tags, and some labels.
func traits() harvest.Traits {
	return harvest.Traits{
		IDPrefixes: []string{"/subscriptions/"},
		IdentityAttributes: []string{
			"key_id", "zone_id", "dns_name", "domain_name", "fqdn", "principal_id", "client_id", "unique_id", "role_definition_resource_id",
		},
		NestedIdentities: map[string]string{"identity": "principal_id"},
		TagAttributes:    []string{"tags", "labels"},
	}
}
