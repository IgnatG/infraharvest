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
	"path/filepath"
	"strings"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
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
	uiDiags, scanErr := parseUIDiagnostics(out.Bytes())
	if scanErr != nil {
		return nil, nil, fmt.Errorf("read the plan's output: %w", scanErr)
	}
	diags := ErrorDiagnostics(uiDiags)
	if err != nil && len(diags) == 0 {
		return nil, nil, err
	}
	summary, scanErr := parseChangeSummary(out.Bytes())
	if scanErr != nil {
		return nil, nil, fmt.Errorf("read the plan's output: %w", scanErr)
	}
	return diags, summary, nil
}

// parseChangeSummary returns the change summary in Terraform's
// machine-readable UI output, or nil.
func parseChangeSummary(out []byte) (*ChangeSummary, error) {
	var summary *ChangeSummary
	err := eachUIMessage(out, func(line []byte) {
		var msg struct {
			Type    string        `json:"type"`
			Changes ChangeSummary `json:"changes"`
		}
		if json.Unmarshal(line, &msg) == nil && msg.Type == "change_summary" {
			changes := msg.Changes
			summary = &changes
		}
	})
	return summary, err
}

// parseUIDiagnostics returns the diagnostics in Terraform's machine-readable
// UI output (one JSON message per line).
func parseUIDiagnostics(out []byte) ([]tfjson.Diagnostic, error) {
	var diags []tfjson.Diagnostic
	err := eachUIMessage(out, func(line []byte) {
		var msg struct {
			Type       string            `json:"type"`
			Diagnostic tfjson.Diagnostic `json:"diagnostic"`
		}
		if json.Unmarshal(line, &msg) == nil && msg.Type == "diagnostic" {
			diags = append(diags, msg.Diagnostic)
		}
	})
	return diags, err
}

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
