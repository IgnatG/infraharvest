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
	secrets := []Secret{{Variable: "v", Address: "aws_ssm_parameter.p", Attribute: "value"}, {Variable: "w", Address: "aws_mq_broker.b", Attribute: "user[0].password"}}
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
		{"delete", []*tfjson.ResourceChange{change("aws_vpc.old", tfjson.Actions{tfjson.ActionDelete}, map[string]any{}, nil)}, false},
		{"replace", []*tfjson.ResourceChange{change("aws_vpc.main", tfjson.Actions{tfjson.ActionDelete, tfjson.ActionCreate}, map[string]any{}, map[string]any{})}, false},
		{"secret in a block", []*tfjson.ResourceChange{change("aws_mq_broker.b", update, map[string]any{"user": []any{map[string]any{"username": "app", "password": "real"}}}, map[string]any{"user": []any{map[string]any{"username": "app", "password": "placeholder"}}})}, true},
		{"secret in a block and a real change beside it", []*tfjson.ResourceChange{change("aws_mq_broker.b", update, map[string]any{"user": []any{map[string]any{"username": "app", "password": "real"}}}, map[string]any{"user": []any{map[string]any{"username": "other", "password": "placeholder"}}})}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := planCheck(&tfjson.Plan{ResourceChanges: tc.changes}, nil, secrets, stateOnly, len(tc.changes))
			if check.Passed != tc.passed {
				t.Errorf("passed=%v, want %v: %v", check.Passed, tc.passed, check.Details)
			}
		})
	}

	failed := planCheck(&tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		change("aws_vpc.main", update, map[string]any{"cidr_block": "a", "tags": nil}, map[string]any{"cidr_block": "b", "tags": map[string]any{}}),
	}}, nil, nil, nil, 1)
	want := []PlannedChange{{Address: "aws_vpc.main", Actions: []string{"update"}, Attributes: []string{"cidr_block", "tags"}}}
	if !reflect.DeepEqual(failed.Changes, want) || failed.Details[0] != "aws_vpc.main: update (cidr_block, tags)" {
		t.Errorf("got %+v, %v", failed.Changes, failed.Details)
	}

	errored := planCheck(nil, []tfjson.Diagnostic{errorAt(1, "Invalid value", "")}, nil, nil, 0)
	if errored.Passed || len(errored.Details) != 1 {
		t.Errorf("want a failure with the error, got %+v", errored)
	}

	noSummary := planCheck(nil, nil, nil, nil, 0)
	if noSummary.Passed || len(noSummary.Details) != 1 {
		t.Errorf("want a failure saying the plan reported nothing, got %+v", noSummary)
	}

	// An edit that lost an import block, with its resource, leaves a plan
	// with fewer imports than the configuration should have.
	dropped := planCheck(&tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{change("aws_vpc.main", tfjson.Actions{tfjson.ActionNoop}, nil, nil)}}, nil, nil, nil, 2)
	if dropped.Passed || !strings.Contains(strings.Join(dropped.Details, "\n"), "imports 1 resources, the configuration has 2 import blocks") {
		t.Errorf("want a failure for the missing import, got %+v", dropped)
	}
	if unchecked := planCheck(&tfjson.Plan{}, nil, nil, nil, -1); !unchecked.Passed {
		t.Errorf("a negative expected count must not be checked, got %+v", unchecked)
	}
}

func TestChangedLeaves(t *testing.T) {
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

	got := changedLeaves(before, after, unknown)

	want := []leaf{
		{path: "arn", unknown: true},
		{path: "tags"},
		{path: "user[0].groups[1]"},
		{path: "user[0].id", unknown: true},
		{path: "user[0].password"},
		{path: "version", unknown: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if top := topLevel(got); !reflect.DeepEqual(top, []string{"arn", "tags", "user", "version"}) {
		t.Errorf("topLevel: got %v", top)
	}
	if got := withoutIndexes("user[0].groups[1]"); got != "user.groups" {
		t.Errorf("withoutIndexes: got %q", got)
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

provider "pagerduty" {
  token          = "u+abcdefghijklmnop"
  service_region = "eu"
  api_key        = var.pagerduty_key
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
	for _, want := range []string{"generated.tf: contains a value the provider marks sensitive", "providers.tf: looks like it contains a AWS access key ID", "providers.tf: provider block sets pagerduty.token, which belongs in the provider's environment", "providers.tf: provider block sets aws.access_key"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, ImportsFileName) || strings.Contains(joined, "pagerduty.api_key") || strings.Contains(joined, "service_region") {
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
	writeConfig(t, dir, VersionsFileName, string(VersionsFile(">= 1.16, < 2.0", Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.14"})))
	writeConfig(t, dir, GeneratedFileName, `resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}
`)
	tf := &fakeTerraform{dir: dir, shown: &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{change("aws_vpc.main", tfjson.Actions{tfjson.ActionNoop}, nil, nil)}}}

	gate, err := runGate(context.Background(), tf, dir, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, c := range gate {
		names = append(names, c.Name)
	}
	if want := []string{CheckFormat, CheckValidate, CheckPlan, CheckStandards, CheckScanners, CheckSecrets, CheckDeterminism}; !reflect.DeepEqual(names, want) {
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

func TestScanNondeterminismIgnoresStringsAndComments(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `# set at timestamp() time
resource "aws_ssm_parameter" "a" {
  value       = "uuid("
  description = "made by timestamp()"
  name        = lower(uuid())
}
`)

	check, err := scanNondeterminism(dir)
	if err != nil {
		t.Fatal(err)
	}

	if check.Passed || len(check.Details) != 1 || check.Details[0] != "generated.tf: calls uuid(" {
		t.Errorf("got %+v", check)
	}
}
