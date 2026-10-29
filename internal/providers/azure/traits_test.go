// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"slices"
	"strings"
	"testing"
)

// Azure's traits: resource IDs identify resources, a role definition's
// resource ID and a system-assigned identity's principal identify theirs,
// and resources take tags; nothing is an ARN.
func TestTraits(t *testing.T) {
	t.Parallel()
	profile := (&Provider{}).Profile()
	traits := profile.Traits
	id := "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg"
	if !slices.ContainsFunc(traits.IDPrefixes, func(p string) bool { return strings.HasPrefix(id, p) }) || traits.ARNPrefix != "" {
		t.Errorf("IDPrefixes %v, ARNPrefix %q: want resource IDs alone", traits.IDPrefixes, traits.ARNPrefix)
	}
	if !slices.Contains(traits.IdentityAttributes, "role_definition_resource_id") || traits.NestedIdentities["identity"] != "principal_id" {
		t.Errorf("IdentityAttributes %v, NestedIdentities %v", traits.IdentityAttributes, traits.NestedIdentities)
	}
	if !slices.Contains(traits.TagAttributes, profile.DefaultTags.Attribute) {
		t.Errorf("TagAttributes %v: want %s", traits.TagAttributes, profile.DefaultTags.Attribute)
	}
}
