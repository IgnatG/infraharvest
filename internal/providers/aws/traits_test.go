// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"slices"
	"strings"
	"testing"
)

// AWS's traits: ARNs identify resources, the policies AWS manages in any
// partition stay where they are used, CloudFormation's tag names the
// stack, and the tags default_tags applies are among the tag attributes.
func TestTraits(t *testing.T) {
	t.Parallel()
	profile := (&Provider{}).Profile()
	traits := profile.Traits
	if role := "arn:aws:iam::123456789012:role/app"; traits.ARNPrefix == "" || !strings.HasPrefix(role, traits.ARNPrefix) || !slices.Contains(traits.IDPrefixes, traits.ARNPrefix) {
		t.Errorf("ARNPrefix %q, IDPrefixes %v: want ARNs", traits.ARNPrefix, traits.IDPrefixes)
	}
	for arn, want := range map[string]bool{
		"arn:aws:iam::aws:policy/ReadOnlyAccess":                            true,
		"arn:aws-us-gov:iam::aws:policy/ReadOnlyAccess":                     true,
		"arn:aws-cn:iam::aws:policy/service-role/AWSLambdaRole":             true,
		"arn:aws:iam::123456789012:policy/ReadOnlyAccess":                   false,
		"arn:aws:iam::123456789012:role/aws-service-role/ecs.amazonaws.com": false,
	} {
		if got := traits.WellKnownIDs.MatchString(arn); got != want {
			t.Errorf("WellKnownIDs.MatchString(%q): got %v, want %v", arn, got, want)
		}
	}
	if len(traits.OwnerTags) != 1 || traits.OwnerTags[0].Key != "aws:cloudformation:stack-name" || !traits.OwnerTags[0].Stack {
		t.Errorf("OwnerTags %+v: want CloudFormation's stack-name tag, naming the stack", traits.OwnerTags)
	}
	if !slices.Contains(traits.TagAttributes, profile.DefaultTags.Attribute) || !slices.Contains(traits.MergedTagAttributes, profile.DefaultTags.Block) {
		t.Errorf("TagAttributes %v, MergedTagAttributes %v: want %s and %s", traits.TagAttributes, traits.MergedTagAttributes, profile.DefaultTags.Attribute, profile.DefaultTags.Block)
	}
}
