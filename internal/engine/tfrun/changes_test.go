// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"reflect"
	"testing"
)

func TestChangedLeaves(t *testing.T) {
	t.Parallel()
	before := map[string]any{
		"cidr_block": "10.0.0.0/16",
		"tags":       nil,
		"user":       []any{map[string]any{"username": "app", "password": "real", "groups": []any{"a"}}},
		"version":    1.0,
	}
	after := map[string]any{
		"cidr_block": "10.0.0.0/16",
		"tags":       map[string]any{},
		"user":       []any{map[string]any{"username": "app", "password": "placeholder", "groups": []any{"a", "b"}}},
	}
	unknown := map[string]any{"version": true, "arn": true, "user": []any{map[string]any{"id": true}}}

	got := ChangedLeaves(before, after, unknown)

	want := []Leaf{
		{Path: "arn", Unknown: true},
		{Path: "tags"},
		{Path: "user[0].groups[1]"},
		{Path: "user[0].id", Unknown: true},
		{Path: "user[0].password"},
		{Path: "version", Unknown: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if top := TopLevel(got); !reflect.DeepEqual(top, []string{"arn", "tags", "user", "version"}) {
		t.Errorf("TopLevel: got %v", top)
	}
	if got := WithoutIndexes("user[0].groups[1]"); got != "user.groups" {
		t.Errorf("WithoutIndexes: got %q", got)
	}
}
