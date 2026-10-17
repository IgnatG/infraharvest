// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	t.Parallel()
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
	t.Parallel()
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

// A sensitive value that another attribute shows in the open isn't a
// secret: the provider marks every SSM parameter's value sensitive, even
// one holding the region the resource is in.
func TestSensitiveValuesLeavesOutPublicOnes(t *testing.T) {
	t.Parallel()
	p := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		{Change: &tfjson.Change{
			Before:          map[string]any{"name": "/app/region", "region": "eu-west-1", "value": "eu-west-1"},
			BeforeSensitive: map[string]any{"value": true},
		}},
		{Change: &tfjson.Change{
			Before:          map[string]any{"name": "/app/key", "value": "k3y-value-1", "tags": map[string]any{"Note": "k3y-value"}},
			BeforeSensitive: map[string]any{"value": true},
		}},
		// A secret's value copied into a function's environment, where the
		// provider doesn't mark it: the copy is the leak, so the value
		// stays in.
		{Change: &tfjson.Change{
			Before:          map[string]any{"secret_string": "Pa55-W0rd!db"},
			BeforeSensitive: map[string]any{"secret_string": true},
		}},
		{Change: &tfjson.Change{
			Before: map[string]any{"environment": []any{map[string]any{"variables": map[string]any{"DB_PW": "Pa55-W0rd!db"}}}},
		}},
	}}

	// k3y-value is only part of the secret: still left in.
	if got, want := sensitiveValues(p), []string{"Pa55-W0rd!db", "k3y-value-1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestScanSecrets(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_db_instance" "a" {
  password = "s3cr3t-value"
}
`)
	writeConfig(t, dir, "providers.tf", `provider "aws" {
  access_key = "`+testKeyID+`"
  secret_key = "`+testSecret+`"
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
	for _, want := range []string{"generated.tf: contains a value the provider marks sensitive", "providers.tf: looks like it contains an AWS access key ID with its secret access key", "providers.tf: provider block sets pagerduty.token, which belongs in the provider's environment", "providers.tf: provider block sets aws.access_key"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, ImportsFileName) || strings.Contains(joined, "pagerduty.api_key") || strings.Contains(joined, "service_region") {
		t.Errorf("clean file reported:\n%s", joined)
	}
}

// Made-up key halves, built from parts so that secret scanners don't take
// the source for a leak. betterleaks still takes the secret for a generic
// API key; the betterleaks:allow comment tells it this one is made up.
var (
	testKeyID  = "AKIA" + "EXAMPLE" + "EXAMPLE12"
	testSecret = "Ab3+cD4/eF5gH6iJ7kL8" + "mN9oP0qR1sT2uV3wX4yZ" // betterleaks:allow
)

// An access key ID is an identifier: on its own, or beside a hex digest,
// it is no credential; beside what looks like its secret, it is.
func TestAWSKeyPair(t *testing.T) {
	t.Parallel()
	for content, want := range map[string]bool{
		`tags = { ` + testKeyID + ` = "tool" }`:                                                false,
		`id = "` + testKeyID + `"` + "\n" + `sha = "3f786850e387550fdab836ed7e6dc881de23001b"`: false,
		`key = "` + testKeyID + `"` + "\n" + `secret = "` + testSecret + `"`:                   true,
		`secret = "` + testSecret + `"`:                                                        false,
		`env = { ID = "` + "ASIA" + testKeyID[4:] + `", SECRET = "` + testSecret + `" }`:       true,
	} {
		if got := awsKeyPair(content); got != want {
			t.Errorf("awsKeyPair(%s) = %v, want %v", content, got, want)
		}
	}
}

func TestScanNondeterminism(t *testing.T) {
	t.Parallel()
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

// The secrets and determinism checks cover the shared local modules a root
// calls, and those they call, outside the root's directory; modules it
// doesn't call aren't its to check.
func TestScansCoverCalledModules(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	root, vpc, subnet, unused := filepath.Join(out, "aws", "eu-west-2"), filepath.Join(out, "modules", "vpc"), filepath.Join(out, "modules", "subnet"), filepath.Join(out, "modules", "unused")
	for _, d := range []string{root, vpc, subnet, unused} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, root, GeneratedFileName, `module "vpc" {
  source = "../../modules/vpc"
}

module "missing" {
  source = "../../modules/missing"
}
`)
	writeConfig(t, vpc, "main.tf", `module "subnet" {
  source = "../subnet"
}

resource "aws_ssm_parameter" "a" {
  value = timestamp()
}
`)
	writeConfig(t, subnet, "main.tf", `resource "aws_ssm_parameter" "b" {
  value = "s3cr3t-value"
}
`)
	writeConfig(t, unused, "main.tf", `resource "aws_ssm_parameter" "c" {
  value = uuid()
}
`)

	secrets, err := scanSecrets(root, []string{"s3cr3t-value"})
	if err != nil {
		t.Fatal(err)
	}
	if secrets.Passed || !strings.Contains(strings.Join(secrets.Details, "\n"), "../../modules/subnet/main.tf: contains a value the provider marks sensitive") {
		t.Errorf("secrets: %+v", secrets)
	}
	determinism, err := scanNondeterminism(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"../../modules/vpc/main.tf: calls timestamp("}; determinism.Passed || !reflect.DeepEqual(determinism.Details, want) {
		t.Errorf("determinism: %+v, want %v", determinism, want)
	}
}

func TestRunGate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, ImportsFileName, `import {
  to = aws_vpc.main
  id = "vpc-1"
}
`)
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
	t.Parallel()
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

// External scanners are other people's code, and some upload what they
// scan: they never see a directory that holds a secret value.
func TestScannersDontSeeSecrets(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, ImportsFileName, `import {
  to = aws_vpc.main
  id = "vpc-1"
}
`)
	writeConfig(t, dir, VersionsFileName, string(VersionsFile(">= 1.16, < 2.0", Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.14"})))
	writeConfig(t, dir, GeneratedFileName, `resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
  tags = { token = "ghp_`+strings.Repeat("a1", 18)+`" }
}
`)
	tf := &fakeTerraform{dir: dir, shown: &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{change("aws_vpc.main", tfjson.Actions{tfjson.ActionNoop}, nil, nil)}}}
	ran := false
	spy := Scanner{Name: "spy", Run: func(context.Context, string) ([]Finding, error) {
		ran = true
		return nil, nil
	}}

	gate, err := runGate(context.Background(), tf, dir, nil, Options{Scanners: []Scanner{spy}})
	if err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Error("a scanner ran on a directory holding a secret")
	}
	if c := gate.check(CheckSecrets); c.Passed {
		t.Errorf("G6 passed: %+v", c)
	}
	if c := gate.check(CheckScanners); !c.Skipped {
		t.Errorf("scanners not skipped: %+v", c)
	}
}

// A gate interrupted part way fails as cancelled, not with the checks
// that were cut short.
func TestGateCancelled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}
`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tf := &fakeTerraform{dir: dir}

	_, err := gateWith(ctx, tf, dir, Options{}, func() (Check, []string, error) {
		cancel() // as an interrupt while the plan runs
		return Check{Name: CheckPlan, Passed: true}, nil, nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("want the cancellation, got %v", err)
	}
}

// TestValueMatcher checks that valueMatcher finds what a strings.Contains
// of each value would: at the start and the end of a text, among values
// that share their first bytes, and values shorter than minSecretLength.
func TestValueMatcher(t *testing.T) {
	t.Parallel()
	values := []string{"s3cr3t-value", "s3cr3t-other", "abc", "token-1234567"}
	contains := func(content string) bool {
		for _, v := range values {
			if strings.Contains(content, v) {
				return true
			}
		}
		return false
	}
	m := newValueMatcher(values)
	for _, content := range []string{
		"", "s3cr3t", "s3cr3t-valu", "s3cr3t-value", "prefix s3cr3t-other", "token-123456 token-1234567",
		"no secret here", "ab", "zzabc", "s3cr3t-s3cr3t-value", "s3cr3t-otherwise",
	} {
		if got, want := m.in(content), contains(content); got != want {
			t.Errorf("in(%q) = %v, want %v", content, got, want)
		}
	}
	if newValueMatcher(nil).in("anything") {
		t.Error("a matcher of no values matched")
	}
}
