// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"slices"
	"strings"
	"testing"
)

// Google Cloud's traits: a Compute Engine self link has its alias on the
// service's own host, service accounts' emails identify them, the
// provider's attribution label says Terraform manages what has it, and
// the labels default_labels applies, and their effective ones, are among
// the tag attributes.
func TestTraits(t *testing.T) {
	t.Parallel()
	profile := (&Provider{}).Profile()
	traits := profile.Traits
	const (
		link  = "https://www.googleapis.com/compute/v1/projects/p/global/networks/n"
		alias = "https://compute.googleapis.com/compute/v1/projects/p/global/networks/n"
	)
	var aliases []string
	for start, to := range traits.SelfLinkAliases {
		if rest, ok := strings.CutPrefix(link, start); ok {
			aliases = append(aliases, to+rest)
		}
	}
	if !slices.Equal(aliases, []string{alias}) {
		t.Errorf("aliases of %s: got %v, want %s", link, aliases, alias)
	}
	email := "app@p.iam.gserviceaccount.com"
	if !slices.ContainsFunc(traits.IDSuffixes, func(s string) bool { return strings.HasSuffix(email, s) }) {
		t.Errorf("IDSuffixes %v: want service accounts' emails", traits.IDSuffixes)
	}
	if len(traits.OwnerTags) != 1 || traits.OwnerTags[0].Key != "goog-terraform-provisioned" || traits.OwnerTags[0].Tool != "Terraform" {
		t.Errorf("OwnerTags %+v: want the provider's attribution label", traits.OwnerTags)
	}
	for _, attribute := range []string{profile.DefaultTags.Attribute, profile.EffectiveTags.Effective} {
		if !slices.Contains(traits.TagAttributes, attribute) {
			t.Errorf("TagAttributes %v: want %s", traits.TagAttributes, attribute)
		}
	}
}

// Google Cloud's regions and zones are shared by what is in them, so none
// identifies a resource; a name that merely ends with a number does.
func TestTraitsRegionIDs(t *testing.T) {
	t.Parallel()
	traits := (&Provider{}).Profile().Traits
	for _, v := range []string{"europe-west1", "us-central1", "us-central1-a", "northamerica-northeast1", "asia-southeast1-b"} {
		if !traits.RegionIDs.MatchString(v) {
			t.Errorf("%s isn't taken for a region", v)
		}
	}
	for _, v := range []string{"web-server-1", "prod-api-2", "my-service1", "dev-test-3", "eu-west-2", "us-east-1"} {
		if traits.RegionIDs.MatchString(v) {
			t.Errorf("%s is taken for a region", v)
		}
	}
	if want := []string{"region", "location", "project"}; !slices.Equal(traits.ScopeAttributes, want) {
		t.Errorf("ScopeAttributes %v, want %v", traits.ScopeAttributes, want)
	}
}
