// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"regexp"
	"testing"
)

// testTraits are the traits the engine's tests run with: those of a
// provider whose resources are like AWS's, Azure's and Google's at once,
// so that each test can use the fixtures of any of them.
var testTraits = Traits{
	IDPrefixes:      []string{"arn:", "/subscriptions/", "projects/"},
	IDSuffixes:      []string{".gserviceaccount.com"},
	ARNPrefix:       "arn:",
	WellKnownIDs:    regexp.MustCompile(`^arn:aws[a-z-]*:iam::aws:policy/`),
	SelfLinkAliases: map[string]string{"https://www.googleapis.com/compute/": "https://compute.googleapis.com/compute/"},
	IdentityAttributes: []string{
		"execution_arn", "root_resource_id", "key_id", "zone_id", "hosted_zone_id", "dns_name", "domain_name",
		"bucket_regional_domain_name", "bucket_domain_name", "fqdn", "arn_suffix", "principal_id", "client_id",
		"unique_id", "role_definition_resource_id",
	},
	NestedIdentities: map[string]string{"identity": "principal_id"},
	OwnerTags: OwnerTags{
		{Key: "aws:cloudformation:stack-name", Tool: "CloudFormation", Stack: true},
		{Key: "goog-terraform-provisioned", Value: "true", Tool: "Terraform"},
	},
	TagAttributes:       []string{"tags", "labels", "terraform_labels", "effective_labels"},
	MergedTagAttributes: []string{"tags_all", "default_tags"},
}

func TestTraitsValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		value                         string
		referenceable, liftable, well bool
		arn                           bool
	}{
		{value: "arn:aws:iam::123456789012:role/app", referenceable: true, liftable: true, arn: true},
		{value: "arn:aws:iam::aws:policy/ReadOnlyAccess", referenceable: true, liftable: true, well: true, arn: true},
		{value: "/subscriptions/0000/resourceGroups/rg", referenceable: true, liftable: true},
		{value: "projects/p/global/networks/n", referenceable: true, liftable: true},
		{value: "vpc-0abc12de", referenceable: true, liftable: true},
		{value: "app@p.iam.gserviceaccount.com", referenceable: true},
		{value: "https://sqs.eu-west-2.amazonaws.com/123456789012/q", referenceable: true},
		{value: "Enabled"},
	} {
		if got := testTraits.referenceable(tc.value); got != tc.referenceable {
			t.Errorf("referenceable(%q): got %v, want %v", tc.value, got, tc.referenceable)
		}
		if got := testTraits.liftable(tc.value); got != tc.liftable {
			t.Errorf("liftable(%q): got %v, want %v", tc.value, got, tc.liftable)
		}
		if got := testTraits.wellKnown(tc.value); got != tc.well {
			t.Errorf("wellKnown(%q): got %v, want %v", tc.value, got, tc.well)
		}
		if got := testTraits.isARN(tc.value); got != tc.arn {
			t.Errorf("isARN(%q): got %v, want %v", tc.value, got, tc.arn)
		}
	}
}

// Without traits, the engine knows only what every provider's values are
// like: a cloud ID and a URL identify a resource, and nothing is an ARN.
func TestZeroTraits(t *testing.T) {
	t.Parallel()
	var none Traits
	for value, want := range map[string]bool{"vpc-0abc12de": true, "https://example.com/x": true, "arn:aws:iam::123456789012:role/app": false, "app@p.iam.gserviceaccount.com": false} {
		if got := none.referenceable(value); got != want {
			t.Errorf("referenceable(%q): got %v, want %v", value, got, want)
		}
	}
	if none.isARN("arn:aws:iam::123456789012:role/app") || none.wellKnown("arn:aws:iam::aws:policy/ReadOnlyAccess") || none.unreferencedMap("tags") {
		t.Error("the zero traits know a provider's values")
	}
}
