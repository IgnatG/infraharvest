// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package secret

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// planFake is a Terraform whose plans report diags, as Terraform's
// machine-readable output does, or fail with err.
type planFake struct {
	tfrun.Terraform
	diags []tfjson.Diagnostic
	err   error
}

func (f *planFake) PlanJSON(_ context.Context, w io.Writer, _ ...tfexec.PlanOption) (bool, error) {
	for _, d := range f.diags {
		line, err := json.Marshal(map[string]any{"type": "diagnostic", "diagnostic": d})
		if err != nil {
			return false, err
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			return false, err
		}
	}
	if f.err == nil && len(f.diags) > 0 {
		return false, errors.New("exit status 1")
	}
	return f.err == nil, f.err
}

func (*planFake) SetStdout(io.Writer) {}

// Providers quote the value they reject, and Terraform doesn't scrub what
// they write: the plans' diagnostics must come back without the values
// Generate hid, raw or quoted.
func TestRedactingTerraformHidesValues(t *testing.T) {
	t.Parallel()
	plain := fakeSecret("plain")
	quoted := `pa"ss\w<o>rd&` + fakeSecret("quoted")[:12]
	fake := &planFake{diags: []tfjson.Diagnostic{
		{Severity: tfjson.DiagnosticSeverityError, Summary: "Invalid value", Detail: `expected one of [a b], got "` + plain + `"`},
		{Severity: tfjson.DiagnosticSeverityError, Summary: "Invalid value " + quoted, Detail: fmt.Sprintf("got %q", quoted)},
	}}
	r := &RedactingTerraform{Terraform: fake}
	r.Hide([]Variable{{Variable: "a", value: plain}, {Variable: "b", value: quoted}, {Variable: "c", value: "short"}})

	diags, _, err := tfrun.Plan(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 2 {
		t.Fatalf("got %d diagnostics, want 2: %+v", len(diags), diags)
	}
	for _, d := range diags {
		text := tfrun.DiagnosticMessage(d)
		for _, v := range []string{plain, quoted, fmt.Sprintf("%q", quoted)[1 : len(fmt.Sprintf("%q", quoted))-1]} {
			if strings.Contains(text, v) {
				t.Errorf("diagnostic holds a secret value: %s", text)
			}
		}
		if !strings.Contains(text, Redacted) {
			t.Errorf("diagnostic doesn't say a value was left out: %s", text)
		}
	}
}

func TestRedactingTerraformRedactsErrors(t *testing.T) {
	t.Parallel()
	secret := fakeSecret("error")
	cause := errors.New("exit status 1: " + secret)
	r := &RedactingTerraform{Terraform: &planFake{err: cause}}
	r.Hide([]Variable{{Variable: "a", value: secret}})

	_, _, err := tfrun.Plan(context.Background(), r)
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
	got := string(redactUIOutput([]byte(out), TextForms([]string{secret})))
	lines := strings.Split(got, "\n")
	if len(lines) != 4 || lines[3] != "" {
		t.Fatalf("lines changed: %q", got)
	}
	if lines[0] != `{"type":"version","terraform":"1.9.0"}` {
		t.Errorf("a message without the value changed: %s", lines[0])
	}
	if lines[1] != "not json "+Redacted {
		t.Errorf("a line that isn't JSON wasn't redacted: %s", lines[1])
	}
	if strings.Contains(got, secret) || !strings.Contains(lines[2], `"type":"diagnostic"`) {
		t.Errorf("diagnostic not redacted as JSON: %s", lines[2])
	}
}
