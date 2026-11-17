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
// plan with: an attribute known before that becomes known only after the
// apply, which the plan shows as null, is a change. It needs no emulator.
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
			name: "known before and unknown after the apply is a change",
			change: &tfjson.Change{
				Before:       map[string]any{"arn": "arn:aws:sqs:us-east-1:1:q", "tags_all": map[string]any{"team": "web"}, "name": "a"},
				After:        map[string]any{"arn": nil, "tags_all": nil, "name": "b"},
				AfterUnknown: map[string]any{"arn": true, "tags_all": true},
			},
			want: map[string]string{
				"name":     "a -> b",
				"arn":      "arn:aws:sqs:us-east-1:1:q -> (known after apply)",
				"tags_all": "map[team:web] -> (known after apply)",
			},
		},
		{
			name: "null before and unknown after the apply is no change",
			change: &tfjson.Change{
				Before:       map[string]any{"name": "a", "kms_key_id": nil},
				After:        map[string]any{"name": "a"},
				AfterUnknown: map[string]any{"kms_key_id": true},
			},
			want: map[string]string{},
		},
		{
			name: "absent before and unknown after the apply is no change",
			change: &tfjson.Change{
				Before:       map[string]any{"name": "a"},
				After:        map[string]any{"name": "a"},
				AfterUnknown: map[string]any{"arn": true},
			},
			want: map[string]string{},
		},
		{
			name: "false in after_unknown is compared",
			change: &tfjson.Change{
				Before:       map[string]any{"name": "a"},
				After:        map[string]any{"name": "b"},
				AfterUnknown: map[string]any{"name": false},
			},
			want: map[string]string{"name": "a -> b"},
		},
		{
			name: "a removed attribute is a change",
			change: &tfjson.Change{
				Before: map[string]any{"name": "a", "description": "managed by hand"},
				After:  map[string]any{"name": "a"},
			},
			want: map[string]string{"description": "managed by hand -> <nil>"},
		},
		{
			name: "a value set to null is a change",
			change: &tfjson.Change{
				Before: map[string]any{"description": "managed by hand"},
				After:  map[string]any{"description": nil},
			},
			want: map[string]string{"description": "managed by hand -> <nil>"},
		},
		{
			name: "null against absent is no change",
			change: &tfjson.Change{
				Before: map[string]any{"name": "a", "description": nil},
				After:  map[string]any{"name": "a"},
			},
			want: map[string]string{},
		},
		{
			name: "removed and unknown after the apply is a change",
			change: &tfjson.Change{
				Before:       map[string]any{"arn": "arn:aws:sqs:us-east-1:1:q"},
				After:        map[string]any{},
				AfterUnknown: map[string]any{"arn": true},
			},
			want: map[string]string{"arn": "arn:aws:sqs:us-east-1:1:q -> (known after apply)"},
		},
		{
			name: "a no-op plan has nothing unknown",
			change: &tfjson.Change{
				Before:       map[string]any{"arn": "arn:aws:sqs:us-east-1:1:q"},
				After:        map[string]any{"arn": "arn:aws:sqs:us-east-1:1:q"},
				AfterUnknown: map[string]any{},
			},
			want: map[string]string{},
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
