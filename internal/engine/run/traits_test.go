// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package run

import "testing"

// A value holds IDs of its own where it is a URL, starts as the provider's
// IDs do, or has one of its segments: what one provider says doesn't hold
// for another's.
func TestTraitsHoldsIDs(t *testing.T) {
	t.Parallel()
	aws := Traits{IDPrefixes: []string{"arn:"}}
	google := Traits{IDPrefixes: []string{"projects/"}, IDSegments: []string{"projects/"}}
	for _, tt := range []struct {
		traits Traits
		value  string
		want   bool
	}{
		{aws, "arn:aws:iam::123456789012:role/ci", true},
		{aws, "projects/app-prod/topics/orders", false},
		{aws, "https://s3.eu-west-2.amazonaws.com", true},
		{google, "projects/app-prod/topics/orders", true},
		{google, "//pubsub.googleapis.com/projects/app-prod", true},
		{google, "app-prod-orders", false},
		{Traits{}, "orders", false},
	} {
		if got := tt.traits.HoldsIDs(tt.value); got != tt.want {
			t.Errorf("HoldsIDs(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}
