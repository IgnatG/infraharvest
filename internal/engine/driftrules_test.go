// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/IgnatG/infraharvest/internal/adapters"
)

// The standards check drift holds its edits to leaves the organisation's
// own modules to their owners, as the verification gate does: it doesn't
// read them, even where they hold what it couldn't parse.
func TestDriftRulesLeaveOwnModulesToTheirOwners(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	dir, own := filepath.Join(out, "aws", "123", "eu-west-2"), filepath.Join(out, "own")
	writeConfig(t, mkdir(t, dir), "main.tf", "module \"own\" {\n  source = \"../../../own\"\n}\n")
	writeConfig(t, dir, VersionsFileName, string(VersionsFile(">= 1.16, < 2.0", Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.14"})))
	if err := os.WriteFile(filepath.Join(mkdir(t, own), "main.tf"), []byte("resource \"aws_sqs_queue\" \"this\" {\n  name = \"${\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	check, err := DriftRules(Options{Adapters: []adapters.Adapter{{Source: own}}}).Standards(dir)

	if err != nil || !check.Passed {
		t.Errorf("standards: %+v, %v", check, err)
	}
	if check, err := DriftRules(Options{}).Standards(dir); err == nil && check.Passed {
		t.Error("standards: the module it can't parse passes as no one's")
	}
}

// A value is a default where the defaults of the options name it for the
// resource type and the argument.
func TestDriftRulesDefaults(t *testing.T) {
	t.Parallel()
	rules := DriftRules(Options{Defaults: map[string][]string{"aws_sqs_queue": {"delay_seconds=0"}}})
	for _, tc := range []struct {
		typ, path, value string
		want             bool
	}{
		{"aws_sqs_queue", "delay_seconds", "0", true},
		{"aws_sqs_queue", "delay_seconds", "30", false},
		{"aws_sqs_queue", "name", "0", false},
		{"aws_sns_topic", "delay_seconds", "0", false},
	} {
		expr, diags := hclsyntax.ParseExpression([]byte(tc.value), "", hcl.InitialPos)
		if diags.HasErrors() {
			t.Fatal(diags)
		}
		if got := rules.IsDefault(tc.typ, tc.path, expr); got != tc.want {
			t.Errorf("%s.%s = %s: %v, want %v", tc.typ, tc.path, tc.value, got, tc.want)
		}
	}
}

// A value refers to the one resource that has it, as generation's
// references do.
func TestDriftRulesReferToTheResourceWithTheValue(t *testing.T) {
	t.Parallel()
	refer := DriftRules(Options{}).Referrer(map[string]map[string]any{"aws_vpc.main": {"id": "vpc-0aaa1111"}, "aws_vpc.other": {"id": "vpc-0bbb2222"}})
	address, ref, ok := refer("vpc_id", "vpc-0bbb2222")
	if text := string(hclwrite.TokensForTraversal(ref).Bytes()); !ok || address != "aws_vpc.other" || text != "aws_vpc.other.id" {
		t.Errorf("got %s, %s, %v, want aws_vpc.other.id", address, text, ok)
	}
	if _, _, ok := refer("vpc_id", "vpc-0ccc3333"); ok {
		t.Error("a value no resource has refers to one")
	}
}

// mkdir makes dir, with its parents, and returns it.
func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}
