// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

func change(address string, actions tfjson.Actions, before, after map[string]any) *tfjson.ResourceChange {
	typ, _, _ := strings.Cut(address, ".")
	return &tfjson.ResourceChange{
		Address: address, Type: typ, Mode: tfjson.ManagedResourceMode,
		Change: &tfjson.Change{Actions: actions, Before: before, After: after, Importing: &tfjson.Importing{ID: "x"}},
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

	checks, err := runGate(context.Background(), tf, dir, nil, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, c := range checks {
		names = append(names, c.Name)
	}
	if want := []string{gate.CheckFormat, gate.CheckValidate, gate.CheckPlan, gate.CheckStandards, gate.CheckMoves, gate.CheckScanners, gate.CheckSecrets, gate.CheckDeterminism}; !reflect.DeepEqual(names, want) {
		t.Errorf("checks: got %v, want %v", names, want)
	}
	if !checks.Passed() {
		t.Errorf("want every check passed: %+v", checks)
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
	spy := gate.Scanner{Name: "spy", Run: func(context.Context, string) ([]gate.Finding, error) {
		ran = true
		return nil, nil
	}}

	checks, err := runGate(context.Background(), tf, dir, nil, Options{Scanners: []gate.Scanner{spy}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Error("a scanner ran on a directory holding a secret")
	}
	if c := checks.Named(gate.CheckSecrets); c.Passed {
		t.Errorf("G6 passed: %+v", c)
	}
	if c := checks.Named(gate.CheckScanners); !c.Skipped {
		t.Errorf("scanners not skipped: %+v", c)
	}
}

// A gate interrupted part way fails as cancelled, not with the checks
// that were cut short, nor with the failure it stopped at.
func TestGateCancelled(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		policy func(cancel func()) gate.Policy
	}{
		{"every check", func(cancel func()) gate.Policy {
			return withChecks(gate.Policy{Plan: func() (gate.Check, []string, error) {
				cancel() // as an interrupt while the plan runs
				return gate.Check{Name: gate.CheckPlan, Passed: true}, nil, nil
			}})
		}},
		{"stopped at a failure", func(cancel func()) gate.Policy {
			cancel() // as an interrupt while fmt runs
			return withChecks(gate.Policy{Checks: []string{gate.CheckFormat, gate.CheckValidate}, Diff: true, Edited: []string{GeneratedFileName}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeConfig(t, dir, GeneratedFileName, `resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}
`)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tf := &fakeTerraform{dir: dir, unformatted: []string{GeneratedFileName}}

			_, _, err := tc.policy(cancel).Run(ctx, tf, dir)

			if !errors.Is(err, context.Canceled) {
				t.Errorf("want the cancellation, got %v", err)
			}
		})
	}
}

// The gate reports its checks in the same order whichever policy it
// runs: the scanners before the secrets check that holds them back.
func TestGateCheckOrder(t *testing.T) {
	t.Parallel()
	all := []string{gate.CheckFormat, gate.CheckValidate, gate.CheckPlan, gate.CheckStandards, gate.CheckMoves, gate.CheckScanners, gate.CheckSecrets, gate.CheckDeterminism}
	for _, tc := range []struct {
		name   string
		checks []string
		want   []string
	}{
		{"every check", nil, all},
		{"drift's", []string{gate.CheckSecrets, gate.CheckMoves, gate.CheckStandards, gate.CheckValidate, gate.CheckFormat}, []string{gate.CheckFormat, gate.CheckValidate, gate.CheckStandards, gate.CheckMoves, gate.CheckSecrets}},
		{"a baseline's", []string{gate.CheckStandards, gate.CheckMoves}, []string{gate.CheckStandards, gate.CheckMoves}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeConfig(t, dir, GeneratedFileName, `resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}
`)
			policy := withChecks(gate.Policy{Checks: tc.checks, Plan: func() (gate.Check, []string, error) {
				return gate.Check{Name: gate.CheckPlan, Passed: true}, nil, nil
			}})
			checks, _, err := policy.Run(context.Background(), &fakeTerraform{dir: dir}, dir)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, c := range checks {
				names = append(names, c.Name)
			}
			if !reflect.DeepEqual(names, tc.want) {
				t.Errorf("checks: got %v, want %v", names, tc.want)
			}
		})
	}
}

// The scanners never run while a file holds a secret value: one the
// gate's plan marks sensitive, one another plan of the directory does,
// or one a secrets check the gate doesn't run found.
func TestGateWithholdsScanners(t *testing.T) {
	t.Parallel()
	const secret = "s3cr3t-value"
	plan := func(values ...string) func() (gate.Check, []string, error) {
		return func() (gate.Check, []string, error) {
			return gate.Check{Name: gate.CheckPlan, Passed: true}, values, nil
		}
	}
	staged := gate.Check{Name: gate.CheckSecrets, Details: []string{"generated.tf: contains a value the provider marks sensitive"}}
	for _, tc := range []struct {
		name    string
		policy  gate.Policy
		ran     bool
		details []string // the scanners' check's, when held back
		secrets bool     // whether the gate's secrets check passes
	}{
		{"clean", withChecks(gate.Policy{Plan: plan(), Others: []gate.OtherPlan{{Name: "environment b", Run: plan("not-in-a-file")}}}), true, nil, true},
		{"the gate's plan", withChecks(gate.Policy{Plan: plan(secret)}), false, []string{"not run, as " + gate.CheckSecrets + " found secret values"}, false},
		{"another plan", withChecks(gate.Policy{Plan: plan(), Others: []gate.OtherPlan{{Name: "environment b", Run: plan(secret)}}}), false, []string{"not run, as " + gate.CheckSecrets + " found secret values", "environment b"}, true},
		{"withheld", withChecks(gate.Policy{Plan: plan(), Withheld: &staged}), false, append([]string{"not run, as " + gate.CheckSecrets + " found secret values"}, staged.Details...), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeConfig(t, dir, GeneratedFileName, `resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
  tags       = { Name = "`+secret+`" }
}
`)
			ran := false
			policy := tc.policy
			policy.Scanners = []gate.Scanner{{Name: "spy", Run: func(context.Context, string) ([]gate.Finding, error) {
				ran = true
				return nil, nil
			}}}
			checks, others, err := policy.Run(context.Background(), &fakeTerraform{dir: dir}, dir)
			if err != nil {
				t.Fatal(err)
			}
			if ran != tc.ran {
				t.Errorf("scanners ran: %t, want %t", ran, tc.ran)
			}
			if c := checks.Named(gate.CheckScanners); !tc.ran && (!c.Skipped || !reflect.DeepEqual(c.Details, tc.details)) {
				t.Errorf("scanners: %+v, want skipped with %v", c, tc.details)
			}
			if c := checks.Named(gate.CheckSecrets); c.Passed != tc.secrets {
				t.Errorf("secrets: %+v, want passed %t", c, tc.secrets)
			}
			if len(others) != len(policy.Others) {
				t.Fatalf("got %d other gates, want %d", len(others), len(policy.Others))
			}
			for i, other := range others {
				names := []string{other[0].Name, other[1].Name}
				if !reflect.DeepEqual(names, []string{gate.CheckPlan, gate.CheckSecrets}) || other[1].Passed != tc.ran {
					t.Errorf("gate of %s: %+v", policy.Others[i].Name, other)
				}
			}
		})
	}
}

// A gate that blames edited files fails only on what the edits add to
// the findings before them, names the files it blames, and stops at the
// first check that fails: what drift's rejected_by_check reports.
func TestGateBlamesEditedFiles(t *testing.T) {
	t.Parallel()
	const secret = "s3cr3t-value"
	held := `resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
  tags       = { Name = "` + secret + `" }
}
`
	const badMove = `moved {
  from = aws_subnet.a
  to   = aws_subnet.missing
}
`
	for _, tc := range []struct {
		name        string
		unformatted []string
		edits       map[string][2]string // by file, what it held before and holds now
		want        string               // the check that fails, with what it found; "" if none
		files       []string
	}{
		{"an edited file unformatted", []string{"main.tf", "other.tf"}, map[string][2]string{"main.tf": {"", ""}}, gate.CheckFormat + ": main.tf isn't formatted", []string{"main.tf"}},
		{"another file unformatted", []string{"other.tf"}, map[string][2]string{"main.tf": {"", ""}}, "", nil},
		{"a moved block added", nil, map[string][2]string{"main.tf": {"", badMove}}, gate.CheckMoves + ": main.tf:1: moved block to aws_subnet.missing, which the root doesn't declare, and no removed block takes", []string{"main.tf"}},
		{"a secret kept", nil, map[string][2]string{"secret.tf": {held, held}}, "", nil},
		{"a secret added", nil, map[string][2]string{"secret.tf": {held, held}, "z.tf": {"", held}}, gate.CheckSecrets + ": z.tf would hold a secret", []string{"z.tf"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeConfig(t, dir, GeneratedFileName, movesCheckConfig)
			if err := os.MkdirAll(filepath.Join(dir, "modules", "network"), 0o755); err != nil {
				t.Fatal(err)
			}
			// A finding the root has already doesn't fail the edits.
			writeConfig(t, dir, MovesFileName, strings.NewReplacer("aws_subnet.a", "aws_subnet.z", "missing", "elsewhere").Replace(badMove))
			tf := &fakeTerraform{dir: dir, unformatted: tc.unformatted}
			baseline, _, err := withChecks(gate.Policy{Checks: []string{gate.CheckStandards, gate.CheckMoves}}).Run(context.Background(), tf, dir)
			if err != nil {
				t.Fatal(err)
			}
			policy := withChecks(gate.Policy{
				Checks:    []string{gate.CheckFormat, gate.CheckValidate, gate.CheckStandards, gate.CheckMoves, gate.CheckSecrets},
				Sensitive: []string{secret},
				Baseline:  baseline,
				Diff:      true,
				Before:    map[string][]byte{},
				After:     map[string][]byte{},
			})
			for name, edit := range tc.edits {
				policy.Edited = append(policy.Edited, name)
				policy.Before[name], policy.After[name] = []byte(edit[0]), []byte(edit[1])
				writeConfig(t, dir, name, edit[1])
			}
			slices.Sort(policy.Edited)
			checks, _, err := policy.Run(context.Background(), tf, dir)
			if err != nil {
				t.Fatal(err)
			}
			var got string
			var files []string
			for _, c := range checks {
				if !c.Passed {
					got, files = c.Name+": "+strings.Join(c.Details, "; "), c.Files
				}
			}
			if got != tc.want || !slices.Equal(files, tc.files) {
				t.Errorf("got %q blaming %v, want %q blaming %v", got, files, tc.want, tc.files)
			}
			if tc.want != "" && !strings.HasPrefix(tc.want, checks[len(checks)-1].Name) {
				t.Errorf("the gate went on past the failure: %+v", checks)
			}
		})
	}
}

// A diff gate with no edited files still judges edits: no file fails
// the format check, the secrets check scans none of the directory's, and
// the gate stops at the first check that fails.
func TestDiffGateWithoutEditedFiles(t *testing.T) {
	t.Parallel()
	const secret = "s3cr3t-value"
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
  tags       = { Name = "`+secret+`" }
}
`)
	tf := &fakeTerraform{dir: dir, unformatted: []string{GeneratedFileName}}
	checks, _, err := withChecks(gate.Policy{
		Checks:    []string{gate.CheckFormat, gate.CheckSecrets},
		Sensitive: []string{secret},
		Diff:      true,
	}).Run(context.Background(), tf, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 2 || !checks[0].Passed || !checks[1].Passed {
		t.Errorf("gate %+v, want format and secrets passed", checks)
	}

	// It stops at the first check that fails, here a moved block the
	// root has without a baseline to judge it by.
	writeConfig(t, dir, MovesFileName, `moved {
  from = aws_vpc.old
  to   = aws_vpc.missing
}
`)
	checks, _, err = withChecks(gate.Policy{Checks: []string{gate.CheckMoves, gate.CheckSecrets}, Sensitive: []string{secret}, Diff: true}).Run(context.Background(), tf, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Name != gate.CheckMoves || checks[0].Passed {
		t.Errorf("gate %+v, want it stopped at the failed moves check", checks)
	}
}

// G4 accepts a merged root's var files and fails others under vars, and
// var files that set a sensitive variable; G6 fails a secret value a var
// file sets.
func TestGateVarFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, hclfile.VariablesFileName, `variable "db_password" {
  description = "The password."
  type        = string
  sensitive   = true
}

variable "api_token" {
  description = "A token."
  type        = string
  default     = null
}
`)
	for name, content := range map[string]string{
		"vars/common.tfvars":         "# nothing\n",
		"vars/prod/terraform.tfvars": "db_password = \"x\"\napi_token = \"ghp_" + strings.Repeat("a1", 18) + "\"\n",
		"vars/prod/backend.hcl":      "bucket = \"shop-tfstate\"\n",
		"vars/prod/notes.txt":        "notes\n",
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	standards, err := checkStandards(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	details := strings.Join(standards.Details, "\n")
	for _, want := range []string{
		"secret-in-var-file: vars/prod/terraform.tfvars:1: sets db_password",
		"unexpected-var-file: vars/prod/notes.txt",
	} {
		if !strings.Contains(details, want) {
			t.Errorf("G4 doesn't report %s:\n%s", want, details)
		}
	}
	if strings.Contains(details, "backend.hcl") || strings.Contains(details, "common.tfvars") {
		t.Errorf("G4 reports a var file of a merged root:\n%s", details)
	}
	checks, _, err := withChecks(gate.Policy{Checks: []string{gate.CheckSecrets}}).Run(t.Context(), &fakeTerraform{dir: dir}, dir)
	if err != nil {
		t.Fatal(err)
	}
	secrets := checks.Named(gate.CheckSecrets)
	if !strings.Contains(strings.Join(secrets.Details, "\n"), "vars/prod/terraform.tfvars: api_token holds a secret value") {
		t.Errorf("G6 doesn't find the token the var file sets: %+v", secrets)
	}
	for _, rel := range []string{"terraform.tfvars", "vars.tfvars", "vars/prod/main.tf"} {
		if hclfile.IsMergedVarFile(rel) {
			t.Errorf("%s is taken for a merged root's var file", rel)
		}
	}
}

// withChecks gives p the engine's standards and moves checks, as fullGate
// does, with no omissions and none of the organisation's own modules.
func withChecks(p gate.Policy) gate.Policy {
	full := fullGate(Options{})
	p.Standards, p.Moves = full.Standards, full.Moves
	return p
}
