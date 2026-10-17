// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func scanner(name string, blocking bool, findings ...Finding) Scanner {
	return Scanner{Name: name, Blocking: blocking, Run: func(context.Context, string) ([]Finding, error) { return findings, nil }}
}

func TestRunScanners(t *testing.T) {
	for name, tc := range map[string]struct {
		scanners []Scanner
		passed   bool
		detail   string
	}{
		"none installed": {nil, true, "no scanner installed"},
		"clean":          {[]Scanner{scanner("tflint", true)}, true, "tflint: no findings"},
		"a lint warning": {
			[]Scanner{scanner("tflint", true, Finding{Rule: "terraform_unused_declarations", Severity: "warning", Location: "variables.tf:1"})},
			false, "tflint: terraform_unused_declarations (warning) variables.tf:1",
		},
		"a lint notice": {[]Scanner{scanner("tflint", true, Finding{Rule: "terraform_naming_convention", Severity: "notice"})}, true, "tflint: 1 notices"},
		"infrastructure settings": {
			[]Scanner{scanner("trivy", false, Finding{Rule: "AVD-AWS-0089", Severity: "HIGH", Message: "Bucket has logging disabled"})},
			true, "trivy: AVD-AWS-0089 (high): Bucket has logging disabled",
		},
	} {
		t.Run(name, func(t *testing.T) {
			check := runScanners(context.Background(), t.TempDir(), tc.scanners)
			if check.Passed != tc.passed || !strings.Contains(strings.Join(check.Details, "\n"), tc.detail) {
				t.Errorf("got passed=%v %v, want passed=%v with %q", check.Passed, check.Details, tc.passed, tc.detail)
			}
			if check.Skipped != (len(tc.scanners) == 0) {
				t.Errorf("skipped=%v with %d scanners", check.Skipped, len(tc.scanners))
			}
		})
	}
}

// The scanners of a directory run at the same time, and the check lists
// them in their order, whichever finishes first.
func TestRunScannersAtTheSameTime(t *testing.T) {
	started := make(chan struct{})
	// first waits for second to start, which it can only if they run at
	// the same time, and finishes last.
	first := Scanner{Name: "tflint", Blocking: true, Run: func(context.Context, string) ([]Finding, error) {
		select {
		case <-started:
			return nil, nil
		case <-time.After(30 * time.Second):
			return nil, errors.New("ran after the others")
		}
	}}
	second := Scanner{Name: "trivy", Run: func(context.Context, string) ([]Finding, error) {
		close(started)
		return []Finding{{Rule: "AVD-AWS-0089", Severity: "HIGH"}}, nil
	}}

	check := runScanners(context.Background(), t.TempDir(), []Scanner{first, second})

	if !check.Passed || len(check.Details) != 3 || check.Details[0] != "tflint: no findings" || !strings.HasPrefix(check.Details[1], "trivy: 1 findings") || check.Details[2] != "trivy: AVD-AWS-0089 (high)" {
		t.Errorf("got passed=%v %q", check.Passed, check.Details)
	}
}

func TestParseScannerOutput(t *testing.T) {
	tflint, err := parseTflint([]byte(`{"issues":[{"rule":{"name":"terraform_typed_variables","severity":"warning"},"message":"no type","range":{"filename":"variables.tf","start":{"line":3}}}],"errors":[]}`))
	if err != nil || len(tflint) != 1 || tflint[0].String() != "terraform_typed_variables (warning) variables.tf:3: no type" {
		t.Errorf("tflint: %v, %v", tflint, err)
	}
	trivy, err := parseTrivy([]byte(`{"Results":[{"Target":"generated.tf","Misconfigurations":[{"ID":"AVD-AWS-0086","Title":"No public access block","Severity":"HIGH","Status":"FAIL"},{"ID":"AVD-AWS-0088","Status":"PASS"}]}]}`))
	if err != nil || len(trivy) != 1 || trivy[0].Rule != "AVD-AWS-0086" {
		t.Errorf("trivy: %v, %v", trivy, err)
	}
	for _, out := range []string{
		`{"results":{"failed_checks":[{"check_id":"CKV_AWS_18","check_name":"Ensure access logging","file_path":"/generated.tf","file_line_range":[1,3]}]}}`,
		`[{"results":{"failed_checks":[{"check_id":"CKV_AWS_18","check_name":"Ensure access logging","file_path":"/generated.tf","file_line_range":[1,3]}]}}]`,
	} {
		checkov, err := parseCheckov([]byte(out))
		if err != nil || len(checkov) != 1 || checkov[0].String() != "CKV_AWS_18 generated.tf:1: Ensure access logging" {
			t.Errorf("checkov: %v, %v", checkov, err)
		}
	}
}
