// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tftypes

import (
	"slices"
	"testing"
)

// The types of a group are other names of each other, and a type of no
// group has none.
func TestAliases(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		typ  string
		want []string
	}{
		{"aws_alb", []string{"aws_lb"}},
		{"aws_lb", []string{"aws_alb"}},
		{"azurerm_virtual_machine", []string{"azurerm_linux_virtual_machine", "azurerm_windows_virtual_machine"}},
		{"azurerm_linux_virtual_machine", []string{"azurerm_virtual_machine", "azurerm_windows_virtual_machine"}},
		{"aws_sqs_queue", nil},
	} {
		if got := Aliases(tt.typ); !slices.Equal(got, tt.want) {
			t.Errorf("%s: got %q, want %q", tt.typ, got, tt.want)
		}
	}
	if !Same("aws_alb_listener", "aws_lb_listener") || !Same("aws_sqs_queue", "aws_sqs_queue") || Same("aws_alb", "aws_lb_listener") {
		t.Error("Same doesn't match the groups")
	}
	if got := Canonical("google_compute_instance_from_template"); got != "google_compute_instance" {
		t.Errorf("Canonical: got %s", got)
	}
}

// An identity's ID lists its attributes sorted by key, whatever order
// they come in, and its values read from JSON keep their meaning.
func TestIdentityID(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		identity map[string]any
		want     string
		ok       bool
	}{
		{map[string]any{"region": "eu-west-2", "name": "/app/logs", "account_id": "123456789012"}, "account_id=123456789012,name=/app/logs,region=eu-west-2", true},
		{map[string]any{"name": "web", "port": float64(443), "global": true, "zone": nil}, "global=true,name=web,port=443", true},
		{map[string]any{}, "", true},
		{map[string]any{"names": []any{"a"}}, "", false},
	} {
		values, ok := IdentityValues(tt.identity)
		if ok != tt.ok {
			t.Errorf("%v: ok %v, want %v", tt.identity, ok, tt.ok)
			continue
		}
		if got := IdentityID(values); got != tt.want {
			t.Errorf("%v: got %q, want %q", tt.identity, got, tt.want)
		}
	}
}
