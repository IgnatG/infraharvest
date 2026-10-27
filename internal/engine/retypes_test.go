// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"slices"
	"testing"
)

// The types of a group are other names of each other, and a type of no
// group has none.
func TestAliasTypes(t *testing.T) {
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
		if got := AliasTypes(tt.typ); !slices.Equal(got, tt.want) {
			t.Errorf("%s: got %q, want %q", tt.typ, got, tt.want)
		}
	}
	if !sameType("aws_alb_listener", "aws_lb_listener") || !sameType("aws_sqs_queue", "aws_sqs_queue") || sameType("aws_alb", "aws_lb_listener") {
		t.Error("sameType doesn't match the groups")
	}
	if got := CanonicalType("google_compute_instance_from_template"); got != "google_compute_instance" {
		t.Errorf("CanonicalType: got %s", got)
	}
}
