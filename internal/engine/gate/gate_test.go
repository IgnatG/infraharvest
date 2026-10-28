// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
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
	secrets := []secret.Variable{{Variable: "v", Address: "aws_ssm_parameter.p", Attribute: "value"}, {Variable: "w", Address: "aws_mq_broker.b", Attribute: "user[0].password"}}
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
			check := PlanCheck(&tfjson.Plan{ResourceChanges: tc.changes}, nil, secrets, stateOnly, len(tc.changes))
			if check.Passed != tc.passed {
				t.Errorf("passed=%v, want %v: %v", check.Passed, tc.passed, check.Details)
			}
		})
	}

	failed := PlanCheck(&tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		change("aws_vpc.main", update, map[string]any{"cidr_block": "a", "tags": nil}, map[string]any{"cidr_block": "b", "tags": map[string]any{}}),
	}}, nil, nil, nil, 1)
	want := []PlannedChange{{Address: "aws_vpc.main", Actions: []string{"update"}, Attributes: []string{"cidr_block", "tags"}}}
	if !reflect.DeepEqual(failed.Changes, want) || failed.Details[0] != "aws_vpc.main: update (cidr_block, tags)" {
		t.Errorf("got %+v, %v", failed.Changes, failed.Details)
	}

	errored := PlanCheck(nil, []tfjson.Diagnostic{errorAt(1, "Invalid value", "")}, nil, nil, 0)
	if errored.Passed || len(errored.Details) != 1 {
		t.Errorf("want a failure with the error, got %+v", errored)
	}

	noSummary := PlanCheck(nil, nil, nil, nil, 0)
	if noSummary.Passed || len(noSummary.Details) != 1 {
		t.Errorf("want a failure saying the plan reported nothing, got %+v", noSummary)
	}

	// An edit that lost an import block, with its resource, leaves a plan
	// with fewer imports than the configuration should have.
	dropped := PlanCheck(&tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{change("aws_vpc.main", tfjson.Actions{tfjson.ActionNoop}, nil, nil)}}, nil, nil, nil, 2)
	if dropped.Passed || !strings.Contains(strings.Join(dropped.Details, "\n"), "imports 1 resources, the configuration has 2 import blocks") {
		t.Errorf("want a failure for the missing import, got %+v", dropped)
	}
	if unchecked := PlanCheck(&tfjson.Plan{}, nil, nil, nil, -1); !unchecked.Passed {
		t.Errorf("a negative expected count must not be checked, got %+v", unchecked)
	}
}

func TestScanSecrets(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, "generated.tf", `resource "aws_db_instance" "a" {
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
	writeConfig(t, dir, "imports.tf", `import {
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
	if strings.Contains(joined, "imports.tf") || strings.Contains(joined, "pagerduty.api_key") || strings.Contains(joined, "service_region") {
		t.Errorf("clean file reported:\n%s", joined)
	}
}

// The local state of a root applied without a backend holds the values the
// provider marks sensitive, but isn't configuration: the scan passes over
// it, its workspaces' states too.
func TestScanSecretsSkipsLocalState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, "generated.tf", "resource \"aws_db_instance\" \"a\" {\n  password = var.password\n}\n")
	state := `{"resources": [{"instances": [{"attributes": {"password": "s3cr3t-value"}}]}]}`
	for _, name := range []string{"terraform.tfstate", "terraform.tfstate.backup", filepath.Join(tfrun.WorkspaceStateDir, "dev", "terraform.tfstate")} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(state), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	check, err := scanSecrets(dir, []string{"s3cr3t-value"})
	if err != nil {
		t.Fatal(err)
	}
	if !check.Passed {
		t.Errorf("want a pass, got %v", check.Details)
	}
}

// Made-up key halves, built from parts so that secret scanners don't take
// the source for a leak. betterleaks still takes the secret for a generic
// API key; the betterleaks:allow comment tells it this one is made up.
var (
	testKeyID  = "AKIA" + "EXAMPLE" + "EXAMPLE12"
	testSecret = "Ab3+cD4/eF5gH6iJ7kL8" + "mN9oP0qR1sT2uV3wX4yZ" // betterleaks:allow
)

func TestScanNondeterminism(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, "generated.tf", `resource "aws_ssm_parameter" "a" {
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
	writeConfig(t, root, "generated.tf", `module "vpc" {
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

func TestScanNondeterminismIgnoresStringsAndComments(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, "generated.tf", `# set at timestamp() time
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

// withUnknown marks attributes of rc as known only after apply.
func withUnknown(rc *tfjson.ResourceChange, attributes ...string) *tfjson.ResourceChange {
	unknown := map[string]any{}
	for _, a := range attributes {
		unknown[a] = true
	}
	rc.Change.AfterUnknown = unknown
	return rc
}

// errorAt is an error diagnostic at line of the generated configuration.
func errorAt(line int, summary, detail string) tfjson.Diagnostic {
	return tfjson.Diagnostic{
		Severity: tfjson.DiagnosticSeverityError,
		Summary:  summary,
		Detail:   detail,
		Range:    &tfjson.Range{Filename: "generated.tf", Start: tfjson.Pos{Line: line}, End: tfjson.Pos{Line: line}},
	}
}

// writeConfig writes content into the file name of dir.
func writeConfig(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
