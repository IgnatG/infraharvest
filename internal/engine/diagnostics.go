// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

// changeSummary counts the changes a plan has, and its imports: an edit
// that loses an import block changes the summary too.
type changeSummary struct {
	Add    int `json:"add"`
	Change int `json:"change"`
	Remove int `json:"remove"`
	Import int `json:"import"`
}

// plan runs terraform plan and returns its error diagnostics and its change
// summary, which is nil if Terraform reported none, as when the plan fails.
// It returns an error only when Terraform failed without reporting any
// diagnostics, for example when it couldn't start or ctx was cancelled, or
// when its output couldn't be read.
func plan(ctx context.Context, tf Terraform, opts ...tfexec.PlanOption) ([]tfjson.Diagnostic, *changeSummary, error) {
	var out bytes.Buffer
	_, err := tf.PlanJSON(ctx, &out, opts...)
	tf.SetStdout(io.Discard) // PlanJSON leaves out as the output of later commands
	uiDiags, scanErr := parseUIDiagnostics(out.Bytes())
	if scanErr != nil {
		return nil, nil, fmt.Errorf("read the plan's output: %w", scanErr)
	}
	diags := errorDiagnostics(uiDiags)
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
func parseChangeSummary(out []byte) (*changeSummary, error) {
	var summary *changeSummary
	err := eachUIMessage(out, func(line []byte) {
		var msg struct {
			Type    string        `json:"type"`
			Changes changeSummary `json:"changes"`
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

func errorDiagnostics(diags []tfjson.Diagnostic) []tfjson.Diagnostic {
	var errs []tfjson.Diagnostic
	for _, d := range diags {
		if d.Severity == tfjson.DiagnosticSeverityError {
			errs = append(errs, d)
		}
	}
	return errs
}

// diagnosticsError joins diagnostics into one error, one line each.
func diagnosticsError(diags []tfjson.Diagnostic) error {
	errs := make([]error, 0, len(diags))
	for _, d := range diags {
		errs = append(errs, errors.New(formatDiagnostic(d)))
	}
	return errors.Join(errs...)
}

// formatDiagnostic renders d as "file:line: summary: detail" on one line.
func formatDiagnostic(d tfjson.Diagnostic) string {
	if d.Range != nil && d.Range.Filename != "" {
		return fmt.Sprintf("%s:%d: %s", filepath.Base(d.Range.Filename), d.Range.Start.Line, diagnosticMessage(d))
	}
	return diagnosticMessage(d)
}

// byResource assigns each diagnostic to the resource it is about, using, in
// order: the address Terraform reports, the block its range points into in
// generated.tf or imports.tf, and the one resource address its message
// names. It returns the diagnostics it can't assign to exactly one resource
// separately: they concern the whole directory, such as the provider
// configuration.
func byResource(diags []tfjson.Diagnostic, generated, imports *hclFile) (map[string][]tfjson.Diagnostic, []tfjson.Diagnostic) {
	addresses := imports.importAddresses()
	assigned := map[string][]tfjson.Diagnostic{}
	var unassigned []tfjson.Diagnostic
	for _, d := range diags {
		addr := ""
		switch {
		case d.Address != "":
			addr = trimInstanceKey(d.Address)
		case d.Range != nil && filepath.Base(d.Range.Filename) == GeneratedFileName:
			addr = generated.resourceAt(d.Range.Start.Line)
		case d.Range != nil && filepath.Base(d.Range.Filename) == ImportsFileName:
			addr = imports.importAt(d.Range.Start.Line)
		default:
			addr = onlyMentioned(d.Summary+" "+d.Detail, addresses)
		}
		if addr == "" || !addresses[addr] {
			unassigned = append(unassigned, d)
			continue
		}
		assigned[addr] = append(assigned[addr], d)
	}
	return assigned, unassigned
}

// trimInstanceKey turns aws_vpc.a["x"] or aws_vpc.a[0] into aws_vpc.a.
func trimInstanceKey(addr string) string {
	if i := strings.IndexByte(addr, '['); i >= 0 {
		return addr[:i]
	}
	return addr
}

var addressLike = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_-]*\.[A-Za-z_][A-Za-z0-9_-]*`)

// onlyMentioned returns the single address in addresses that text names, or
// "" if it names none or several.
func onlyMentioned(text string, addresses map[string]bool) string {
	found := ""
	for _, m := range addressLike.FindAllString(text, -1) {
		if !addresses[m] || m == found {
			continue
		}
		if found != "" {
			return ""
		}
		found = m
	}
	return found
}
