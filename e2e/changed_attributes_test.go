// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"maps"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

// TestChangedAttributes checks the helper the e2e tests judge an import's
// plan with: an attribute known only after the apply, which the plan
// shows as null, isn't a change. It needs no emulator.
func TestChangedAttributes(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change *tfjson.Change
		want   map[string]string
	}{
		{name: "no change", change: nil, want: map[string]string{}},
		{
			name: "a changed and an unchanged attribute",
			change: &tfjson.Change{
				Before: map[string]any{"name": "a", "tags": map[string]any{"team": "web"}},
				After:  map[string]any{"name": "a", "tags": map[string]any{"team": "ops"}},
			},
			want: map[string]string{"tags": "map[team:web] -> map[team:ops]"},
		},
		{
			name: "known only after the apply",
			change: &tfjson.Change{
				Before:       map[string]any{"arn": "arn:aws:sqs:us-east-1:1:q", "tags_all": map[string]any{"team": "web"}, "name": "a"},
				After:        map[string]any{"arn": nil, "tags_all": nil, "name": "b"},
				AfterUnknown: map[string]any{"arn": true, "tags_all": true},
			},
			want: map[string]string{"name": "a -> b"},
		},
		{
			name: "partly unknown is still compared",
			change: &tfjson.Change{
				Before:       map[string]any{"tags_all": map[string]any{"team": "web"}},
				After:        map[string]any{"tags_all": map[string]any{"team": "ops"}},
				AfterUnknown: map[string]any{"tags_all": map[string]any{"extra": true}},
			},
			want: map[string]string{"tags_all": "map[team:web] -> map[team:ops]"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := changedAttributes(tt.change); !maps.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
