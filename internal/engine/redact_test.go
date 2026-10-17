// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

// Providers quote the value they reject, and Terraform doesn't scrub what
// they write: the plans' diagnostics must come back without the values
// Generate hid, raw or quoted.
func TestRedactingTerraformHidesValues(t *testing.T) {
	t.Parallel()
	plain := fakeSecret("plain")
	quoted := `pa"ss\w<o>rd&` + fakeSecret("quoted")[:12]
	fake := &fakeTerraform{plans: []fakePlan{{diags: []tfjson.Diagnostic{
		{Severity: tfjson.DiagnosticSeverityError, Summary: "Invalid value", Detail: `expected one of [a b], got "` + plain + `"`},
		{Severity: tfjson.DiagnosticSeverityError, Summary: "Invalid value " + quoted, Detail: fmt.Sprintf("got %q", quoted)},
	}}}}
	r := &redactingTerraform{Terraform: fake}
	r.hide([]Secret{{Variable: "a", value: plain}, {Variable: "b", value: quoted}, {Variable: "c", value: "short"}})

	diags, _, err := plan(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 2 {
		t.Fatalf("got %d diagnostics, want 2: %+v", len(diags), diags)
	}
	for _, d := range diags {
		text := diagnosticMessage(d)
		for _, v := range []string{plain, quoted, fmt.Sprintf("%q", quoted)[1 : len(fmt.Sprintf("%q", quoted))-1]} {
			if strings.Contains(text, v) {
				t.Errorf("diagnostic holds a secret value: %s", text)
			}
		}
		if !strings.Contains(text, redacted) {
			t.Errorf("diagnostic doesn't say a value was left out: %s", text)
		}
	}
}

func TestRedactingTerraformRedactsErrors(t *testing.T) {
	t.Parallel()
	secret := fakeSecret("error")
	cause := errors.New("exit status 1: " + secret)
	r := &redactingTerraform{Terraform: &fakeTerraform{plans: []fakePlan{{err: cause}}}}
	r.hide([]Secret{{Variable: "a", value: secret}})

	_, _, err := plan(context.Background(), r)
	if err == nil {
		t.Fatal("no error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error holds the secret value: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("error doesn't unwrap to its cause: %v", err)
	}
}

func TestRedactUIOutputKeepsOtherLines(t *testing.T) {
	t.Parallel()
	secret := fakeSecret("ui")
	out := "{\"type\":\"version\",\"terraform\":\"1.9.0\"}\nnot json " + secret + "\n{\"type\":\"diagnostic\",\"diagnostic\":{\"detail\":\"got \\\"" + secret + "\\\"\"}}\n"
	got := string(redactUIOutput([]byte(out), textForms([]string{secret})))
	lines := strings.Split(got, "\n")
	if len(lines) != 4 || lines[3] != "" {
		t.Fatalf("lines changed: %q", got)
	}
	if lines[0] != `{"type":"version","terraform":"1.9.0"}` {
		t.Errorf("a message without the value changed: %s", lines[0])
	}
	if lines[1] != "not json "+redacted {
		t.Errorf("a line that isn't JSON wasn't redacted: %s", lines[1])
	}
	if strings.Contains(got, secret) || !strings.Contains(lines[2], `"type":"diagnostic"`) {
		t.Errorf("diagnostic not redacted as JSON: %s", lines[2])
	}
}

// The gate ran before Generate wrote the README, which shows its details.
func TestRedactResultAndCheckReadme(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	secret := fakeSecret("readme")
	secrets := []Secret{{Variable: "a", value: secret}}
	result := &Result{
		Gate:    Gate{{Name: CheckPlan, Details: []string{`generated.tf:3: Invalid value: got "` + secret + `"`}}, {Name: CheckSecrets, Passed: true}},
		Modules: []ModuleCall{{Name: "vpc", Declined: "plan error: got " + secret}},
	}
	redactResult(result, secrets)
	if err := WriteReadme(dir, result); err != nil {
		t.Fatal(err)
	}
	if err := checkReadme(dir, result, knownValues(secrets)); err != nil {
		t.Fatal(err)
	}
	assertNotWritten(t, dir, secret)
	if !result.Gate.check(CheckSecrets).Passed {
		t.Errorf("G6 failed on a redacted README: %+v", result.Gate.check(CheckSecrets))
	}

	// Should a value get through, G6 fails and the README says so.
	leaky := &Result{Gate: Gate{{Name: CheckPlan, Details: []string{"got " + secret}}, {Name: CheckSecrets, Passed: true}}}
	if err := WriteReadme(dir, leaky); err != nil {
		t.Fatal(err)
	}
	if err := checkReadme(dir, leaky, knownValues(secrets)); err != nil {
		t.Fatal(err)
	}
	g6 := leaky.Gate.check(CheckSecrets)
	if g6.Passed || len(g6.Details) != 1 || !strings.HasPrefix(g6.Details[0], ReadmeFileName+": ") {
		t.Errorf("G6 didn't fail on the README: %+v", g6)
	}
	if readme := readFile(t, dir, ReadmeFileName); !strings.Contains(readme, CheckSecrets+" | **failed**") {
		t.Errorf("README doesn't show G6 failing:\n%s", readme)
	}
}
