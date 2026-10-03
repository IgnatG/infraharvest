// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

func change(address string, actions tfjson.Actions, before, after map[string]any) *tfjson.ResourceChange {
	typ, _, _ := strings.Cut(address, ".")
	return &tfjson.ResourceChange{
		Address: address, Type: typ, Mode: tfjson.ManagedResourceMode,
		Change: &tfjson.Change{Actions: actions, Before: before, After: after, Importing: &tfjson.Importing{ID: "x"}},
	}
}

var update = tfjson.Actions{tfjson.ActionUpdate}

func TestPlanCheck(t *testing.T) {
	stateOnly := map[string][]string{"aws_secretsmanager_secret": {"recovery_window_in_days"}}
	secrets := []Secret{{Variable: "v", Address: "aws_ssm_parameter.p", Attribute: "value"}}
	for _, tc := range []struct {
		name    string
		changes []*tfjson.ResourceChange
		passed  bool
	}{
		{"imports only", []*tfjson.ResourceChange{change("aws_vpc.main", tfjson.Actions{tfjson.ActionNoop}, nil, nil)}, true},
		{"state-only argument", []*tfjson.ResourceChange{change("aws_secretsmanager_secret.db", update, map[string]any{"name": "db"}, map[string]any{"name": "db", "recovery_window_in_days": 30.0})}, true},
		{"placeholder secret", []*tfjson.ResourceChange{change("aws_ssm_parameter.p", update, map[string]any{"value": "real"}, map[string]any{"value": "placeholder"})}, true},
		{"placeholder secret and what it makes unknown", []*tfjson.ResourceChange{withUnknown(change("aws_ssm_parameter.p", update, map[string]any{"value": "real", "version": 1.0}, map[string]any{"value": "placeholder"}), "version")}, true},
		{"unknown without a secret", []*tfjson.ResourceChange{withUnknown(change("aws_nat_gateway.n", update, map[string]any{}, map[string]any{}), "secondary_allocation_ids")}, false},
		{"real change", []*tfjson.ResourceChange{change("aws_vpc.main", update, map[string]any{"cidr_block": "10.0.0.0/16"}, map[string]any{"cidr_block": "10.1.0.0/16"})}, false},
		{"create", []*tfjson.ResourceChange{change("aws_vpc.extra", tfjson.Actions{tfjson.ActionCreate}, nil, map[string]any{})}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := planCheck(&tfjson.Plan{ResourceChanges: tc.changes}, nil, secrets, stateOnly)
			if check.Passed != tc.passed {
				t.Errorf("passed=%v, want %v: %v", check.Passed, tc.passed, check.Details)
			}
		})
	}

	failed := planCheck(&tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		change("aws_vpc.main", update, map[string]any{"cidr_block": "a", "tags": nil}, map[string]any{"cidr_block": "b", "tags": map[string]any{}}),
	}}, nil, nil, nil)
	want := []PlannedChange{{Address: "aws_vpc.main", Actions: []string{"update"}, Attributes: []string{"cidr_block", "tags"}}}
	if !reflect.DeepEqual(failed.Changes, want) || failed.Details[0] != "aws_vpc.main: update (cidr_block, tags)" {
		t.Errorf("got %+v, %v", failed.Changes, failed.Details)
	}

	errored := planCheck(nil, []tfjson.Diagnostic{errorAt(1, "Invalid value", "")}, nil, nil)
	if errored.Passed || len(errored.Details) != 1 {
		t.Errorf("want a failure with the error, got %+v", errored)
	}
}

func TestSensitiveValues(t *testing.T) {
	p := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{{
		Change: &tfjson.Change{
			Before:          map[string]any{"name": "app-db", "password": "s3cr3t-value", "nested": []any{map[string]any{"token": "tok-123456"}}, "short": "abc"},
			BeforeSensitive: map[string]any{"password": true, "nested": []any{map[string]any{"token": true}}, "short": true},
		},
	}}}

	got := sensitiveValues(p)

	if want := []string{"s3cr3t-value", "tok-123456"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestScanSecrets(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_db_instance" "a" {
  password = "s3cr3t-value"
}
`)
	writeConfig(t, dir, "providers.tf", `provider "aws" {
  access_key = "AKIAABCDEFGHIJKLMNOP"
}
`)
	writeConfig(t, dir, ImportsFileName, `import {
  to = aws_db_instance.a
  id = "db"
}
`)

	check, err := scanSecrets(dir, []string{"s3cr3t-value"})
	if err != nil {
		t.Fatal(err)
	}

	if check.Passed {
		t.Fatal("want a failure")
	}
	joined := strings.Join(check.Details, "\n")
	for _, want := range []string{"generated.tf: contains a value the provider marks sensitive", "providers.tf: looks like it contains a AWS access key ID"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, ImportsFileName) {
		t.Errorf("clean file reported:\n%s", joined)
	}
}

func TestScanNondeterminism(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_ssm_parameter" "a" {
  value = timestamp()
}
`)

	check, err := scanNondeterminism(dir)
	if err != nil {
		t.Fatal(err)
	}

	if check.Passed || len(check.Details) != 1 || !strings.Contains(check.Details[0], "timestamp(") {
		t.Errorf("got %+v", check)
	}
}

func TestRunGate(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}
`)
	tf := &fakeTerraform{dir: dir, shown: &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{change("aws_vpc.main", tfjson.Actions{tfjson.ActionNoop}, nil, nil)}}}

	gate, err := runGate(context.Background(), tf, dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, c := range gate {
		names = append(names, c.Name)
	}
	if want := []string{CheckFormat, CheckValidate, CheckPlan, CheckSecrets, CheckDeterminism}; !reflect.DeepEqual(names, want) {
		t.Errorf("checks: got %v, want %v", names, want)
	}
	if !gate.Passed() {
		t.Errorf("want every check passed: %+v", gate)
	}
}

// withUnknown marks attributes of rc as known only after apply.
func withUnknown(rc *tfjson.ResourceChange, attributes ...string) *tfjson.ResourceChange {
	unknown := map[string]any{}
	for _, a := range attributes {
		unknown[a] = true
	}
	rc.Change.AfterUnknown = unknown
	return rc
}
