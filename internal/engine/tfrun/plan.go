// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// ChangeSummary counts the changes a plan has, and its imports: an edit
// that loses an import block changes the summary too.
type ChangeSummary struct {
	Add    int `json:"add"`
	Change int `json:"change"`
	Remove int `json:"remove"`
	Import int `json:"import"`
}

// Plan runs terraform plan and returns its error diagnostics and its change
// summary, which is nil if Terraform reported none, as when the plan fails.
// It returns an error when Terraform failed without reporting any
// diagnostics, for example when it couldn't start, when ctx is done or the
// plan timed out, whatever Terraform reported before it was stopped, or
// when its output couldn't be read.
func Plan(ctx context.Context, tf Terraform, opts ...tfexec.PlanOption) ([]tfjson.Diagnostic, *ChangeSummary, error) {
	var out bytes.Buffer
	_, err := tf.PlanJSON(ctx, &out, opts...)
	tf.SetStdout(io.Discard) // PlanJSON leaves out as the output of later commands
	// A stopped plan reports what it was doing, such as "Operation
	// cancelled": that isn't an error of the configuration.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, nil, ctxErr
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, nil, err
	}
	uiDiags, summary, scanErr := parsePlanOutput(out.Bytes())
	if scanErr != nil {
		return nil, nil, fmt.Errorf("read the plan's output: %w", scanErr)
	}
	diags := ErrorDiagnostics(uiDiags)
	if err != nil && len(diags) == 0 {
		return nil, nil, err
	}
	return diags, summary, nil
}

// parsePlanOutput returns the diagnostics and the change summary in
// Terraform's machine-readable UI output, in one pass. A plan's output
// has a line or two per resource, each as large as what the plan shows of
// it, and only a few are diagnostics or the summary: a line is decoded
// only if it has "diagnostic" or "change_summary" as a string, as a line
// of either type has its type as one.
func parsePlanOutput(out []byte) ([]tfjson.Diagnostic, *ChangeSummary, error) {
	var diags []tfjson.Diagnostic
	var summary *ChangeSummary
	err := eachUIMessage(out, func(line []byte) {
		if bytes.Contains(line, diagnosticType) {
			var msg struct {
				Type       string            `json:"type"`
				Diagnostic tfjson.Diagnostic `json:"diagnostic"`
			}
			if json.Unmarshal(line, &msg) == nil && msg.Type == "diagnostic" {
				diags = append(diags, msg.Diagnostic)
				return
			}
		}
		if bytes.Contains(line, changeSummaryType) {
			var msg struct {
				Type    string        `json:"type"`
				Changes ChangeSummary `json:"changes"`
			}
			if json.Unmarshal(line, &msg) == nil && msg.Type == "change_summary" {
				changes := msg.Changes
				summary = &changes
			}
		}
	})
	return diags, summary, err
}

// The types of the UI output's lines parsePlanOutput reads, as strings.
var (
	diagnosticType    = []byte(`"diagnostic"`)
	changeSummaryType = []byte(`"change_summary"`)
)

// eachUIMessage calls visit with each line of Terraform's machine-readable
// UI output. It fails on a line too long to read, so that no message is
// passed over quietly.
func eachUIMessage(out []byte, visit func(line []byte)) error {
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		visit(scanner.Bytes())
	}
	return scanner.Err()
}

// ErrorDiagnostics returns the diagnostics of diags that are errors.
func ErrorDiagnostics(diags []tfjson.Diagnostic) []tfjson.Diagnostic {
	var errs []tfjson.Diagnostic
	for _, d := range diags {
		if d.Severity == tfjson.DiagnosticSeverityError {
			errs = append(errs, d)
		}
	}
	return errs
}

// DiagnosticsError joins diagnostics into one error, one line each.
func DiagnosticsError(diags []tfjson.Diagnostic) error {
	errs := make([]error, 0, len(diags))
	for _, d := range diags {
		errs = append(errs, errors.New(FormatDiagnostic(d)))
	}
	return errors.Join(errs...)
}

// FormatDiagnostic renders d as "file:line: summary: detail" on one line.
func FormatDiagnostic(d tfjson.Diagnostic) string {
	if d.Range != nil && d.Range.Filename != "" {
		return fmt.Sprintf("%s:%d: %s", filepath.Base(d.Range.Filename), d.Range.Start.Line, DiagnosticMessage(d))
	}
	return DiagnosticMessage(d)
}

// DiagnosticMessage renders d as "summary: detail" on one line.
func DiagnosticMessage(d tfjson.Diagnostic) string {
	if detail := strings.Join(strings.Fields(d.Detail), " "); detail != "" {
		return d.Summary + ": " + detail
	}
	return d.Summary
}

// ShowPlan plans with vars into a plan file and returns it as
// JSON with its change summary, or the errors if the configuration doesn't
// plan. The plan file holds secret values: it lives in a private temporary
// directory, removed before ShowPlan returns.
func ShowPlan(ctx context.Context, tf Terraform, vars []tfexec.PlanOption) (*tfjson.Plan, *ChangeSummary, []tfjson.Diagnostic, error) {
	RemoveStaleSnapshots()
	tmp, err := os.MkdirTemp("", PlanPattern)
	if err != nil {
		return nil, nil, nil, err
	}
	defer os.RemoveAll(tmp)
	// The mode is 0700 already; this also covers Windows, which the mode
	// doesn't.
	if _, err := fsutil.MakePrivate(tmp); err != nil {
		return nil, nil, nil, err
	}
	return showPlanTo(ctx, tf, filepath.Join(tmp, "plan"), vars)
}

// PlanPattern names the private temporary directories of ShowPlan's plan
// files.
const PlanPattern = "infraharvest-plan-"

// showPlanTo is ShowPlan into planFile, which it leaves for the caller to
// remove, in a private directory of the caller's.
func showPlanTo(ctx context.Context, tf Terraform, planFile string, vars []tfexec.PlanOption) (*tfjson.Plan, *ChangeSummary, []tfjson.Diagnostic, error) {
	opts := append(append([]tfexec.PlanOption(nil), vars...), tfexec.Out(planFile))
	diags, summary, err := Plan(ctx, tf, opts...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("terraform plan: %w", err)
	}
	if len(diags) > 0 || summary == nil {
		return nil, nil, diags, nil
	}
	// The directory is private already; this also keeps the file private
	// where the directory's permissions aren't inherited.
	if _, err := os.Stat(planFile); err == nil {
		_ = os.Chmod(planFile, 0o600)
	}
	p, err := tf.ShowPlanFile(ctx, planFile)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("terraform show: %w", err)
	}
	return p, summary, nil, nil
}
