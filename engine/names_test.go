// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"reflect"
	"testing"
)

func TestLabel(t *testing.T) {
	for name, want := range map[string]string{
		"vpc-7fe7f292":               "vpc_7fe7f292",
		"/infraharvest-e2e/endpoint": "infraharvest_e2e_endpoint",
		"Orders Queue":               "orders_queue",
		"2024-backups":               "r_2024_backups",
		"--":                         "resource",
	} {
		if got := Label(name); got != want {
			t.Errorf("Label(%q): got %q, want %q", name, got, want)
		}
	}
}

func TestLabelledIsUniqueAndStable(t *testing.T) {
	imports := []Import{
		{Type: "aws_vpc", Name: "main", ID: "vpc-2"},
		{Type: "aws_vpc", Name: "main_2", ID: "vpc-9"},
		{Type: "aws_vpc", Name: "Main", ID: "vpc-1"},
		{Type: "aws_subnet", Name: "main", ID: "subnet-1"},
	}
	want := []Import{
		{Type: "aws_subnet", Name: "main", ID: "subnet-1"},
		{Type: "aws_vpc", Name: "main", ID: "vpc-1"},
		{Type: "aws_vpc", Name: "main_3", ID: "vpc-2"}, // main_2 is another resource's own label
		{Type: "aws_vpc", Name: "main_2", ID: "vpc-9"},
	}

	got := labelled(imports, nil)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// Discovery order must not change the labels.
	reversed := []Import{imports[3], imports[2], imports[1], imports[0]}
	if again := labelled(reversed, nil); !reflect.DeepEqual(again, want) {
		t.Errorf("reordered input: got %+v, want %+v", again, want)
	}
}
