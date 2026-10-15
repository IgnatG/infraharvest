// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// A tag filter with values becomes tag:<key>; one on the key alone, as
// --tag owner writes it, becomes tag-key, which matches whatever the value.
// Filters on IDs, other attributes or other services are not passed on.
func TestEC2TagFilters(t *testing.T) {
	got := ec2TagFilters([]harvest.Filter{
		{Path: "tags.env", Values: []string{"prod", "dev"}},
		{Path: "tags.owner"},
		{Service: "instance", Path: "id", Values: []string{"i-1"}},
		{Path: "cidr_block", Values: []string{"10.0.0.0/16"}},
		{Service: "ebs_volume", Path: "tags.team", Values: []string{"data"}},
	}, ec2FilterServiceNames...)
	if len(got) != 2 {
		t.Fatalf("got %d filters, want 2: %+v", len(got), got)
	}
	if aws.ToString(got[0].Name) != "tag:env" || !slices.Equal(got[0].Values, []string{"prod", "dev"}) {
		t.Errorf("first filter: %s %v", aws.ToString(got[0].Name), got[0].Values)
	}
	if aws.ToString(got[1].Name) != "tag-key" || !slices.Equal(got[1].Values, []string{"owner"}) {
		t.Errorf("second filter: %s %v", aws.ToString(got[1].Name), got[1].Values)
	}
}
