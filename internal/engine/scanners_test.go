// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func scanner(name string, blocking bool, findings ...Finding) Scanner {
	return Scanner{Name: name, Blocking: blocking, Run: func(context.Context, string) ([]Finding, error) { return findings, nil }}
}

func TestRunScanners(t *testing.T) {
	t.Parallel()
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
			check := runScanners(context.Background(), t.TempDir(), tc.scanners, nil)
			if check.Passed != tc.passed || !strings.Contains(strings.Join(check.Details, "\n"), tc.detail) {
				t.Errorf("got passed=%v %v, want passed=%v with %q", check.Passed, check.Details, tc.passed, tc.detail)
			}
			if check.Skipped != (len(tc.scanners) == 0) {
				t.Errorf("skipped=%v with %d scanners", check.Skipped, len(tc.scanners))
			}
		})
	}
}

// The check lists a bounded number of each scanner's findings, but keeps
// them all, of the scanner that found them, about the resource each is
// at.
func TestRunScannersKeepsEveryFinding(t *testing.T) {
	t.Parallel()
	dir := writeFindingsRoot(t)
	var many []Finding
	for i := range maxReportedFindings + 5 {
		many = append(many, Finding{Rule: fmt.Sprintf("AVD-%03d", i), Severity: "LOW", Resource: "aws_s3_bucket.logs"})
	}
	lint := Finding{Rule: "terraform_deprecated_interpolation", Severity: "warning", Location: "main.tf:3", at: position{file: "main.tf", line: 3}}

	check := runScanners(t.Context(), dir, []Scanner{scanner("tflint", true, lint), scanner("trivy", false, many...)}, nil)

	if len(check.Findings) != len(many)+1 {
		t.Fatalf("got %d findings, want %d", len(check.Findings), len(many)+1)
	}
	if f := check.Findings[0]; f.Scanner != "tflint" || f.Resource != "aws_s3_bucket.logs" {
		t.Errorf("tflint's finding: got %+v", f)
	}
	for _, f := range check.Findings[1:] {
		if f.Scanner != "trivy" || f.Resource != "aws_s3_bucket.logs" {
			t.Errorf("trivy's finding: got %+v", f)
		}
	}
	if got := check.Details[len(check.Details)-1]; got != "trivy: 5 more" {
		t.Errorf("details end with %q, want the bound", got)
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

	check := runScanners(context.Background(), t.TempDir(), []Scanner{first, second}, nil)

	if !check.Passed || len(check.Details) != 3 || check.Details[0] != "tflint: no findings" || !strings.HasPrefix(check.Details[1], "trivy: 1 findings") || check.Details[2] != "trivy: AVD-AWS-0089 (high)" {
		t.Errorf("got passed=%v %q", check.Passed, check.Details)
	}
}

func TestParseScannerOutput(t *testing.T) {
	t.Parallel()
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

// The scanner slots belong to the run that passes them: a gate whose
// slots another root of its import holds waits for one, and a gate of
// another run, with slots of its own or none given, doesn't, as a
// long-running process such as the MCP server runs one import after
// another and at the same time.
func TestScannerSlotsBelongToTheRun(t *testing.T) {
	t.Parallel()
	taken := &ScannerSlots{}
	for range cap(taken.channel()) {
		taken.channel() <- struct{}{}
	}
	for name, tc := range map[string]struct {
		slots *ScannerSlots
		ran   bool
	}{
		"the import's, all taken": {taken, false},
		"another run's":           {&ScannerSlots{}, true},
		"none given":              {nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeConfig(t, dir, GeneratedFileName, "resource \"aws_vpc\" \"main\" {\n  cidr_block = \"10.0.0.0/16\"\n}\n")
			ran := false
			policy := fullGate(Options{Scanners: []Scanner{{Name: "spy", Run: func(context.Context, string) ([]Finding, error) {
				ran = true
				return nil, nil
			}}}, ScannerSlots: tc.slots})
			policy.checks = []string{CheckScanners}
			// One that waits for a slot waits until the gate gives up.
			wait := 30 * time.Second
			if !tc.ran {
				wait = 200 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), wait)
			defer cancel()
			gate, _, err := policy.run(ctx, &fakeTerraform{dir: dir}, dir)
			if waited := errors.Is(err, context.DeadlineExceeded); ran != tc.ran || waited == tc.ran || (err != nil && !waited) {
				t.Errorf("scanners ran: %t, want %t: %+v, %v", ran, tc.ran, gate, err)
			}
		})
	}
}

// Scanners read configuration, which needs no credentials: checkov uploads
// what it scans with a platform key, a cloud credential is no business of
// any scanner, and neither is a CI token, with which one could mint a
// cloud token over OIDC.
func TestScannerEnv(t *testing.T) {
	t.Parallel()
	got := scannerEnv([]string{
		"PATH=/usr/bin", "HOME=/home/me", "AWS_SECRET_ACCESS_KEY=x", "aws_profile=prod", "ARM_CLIENT_SECRET=x", "GOOGLE_APPLICATION_CREDENTIALS=/k.json",
		"TF_TOKEN_app_terraform_io=x", "TFE_TOKEN=x", "VAULT_TOKEN=x", "VAULT_ADDR=https://vault", "BC_API_KEY=x", "PRISMA_API_URL=x",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN=x", "ACTIONS_ID_TOKEN_REQUEST_URL=https://x", "ACTIONS_RUNTIME_TOKEN=x", "GITHUB_TOKEN=x", "GH_TOKEN=x",
		"CI_JOB_TOKEN=x", "SYSTEM_ACCESSTOKEN=x", "SSH_AUTH_SOCK=/tmp/agent", "NPM_TOKEN=x", "DB_PASSWORD=x", "MY_SECRET=x", "SOME_API_KEY=x",
		"TRIVY_CACHE_DIR=/c", "HTTPS_PROXY=http://proxy:3128", "SSL_CERT_FILE=/ca.pem", "TMPDIR=/tmp", "GITHUB_ACTIONS=true",
	})
	want := []string{"PATH=/usr/bin", "HOME=/home/me", "TRIVY_CACHE_DIR=/c", "HTTPS_PROXY=http://proxy:3128", "SSL_CERT_FILE=/ca.pem", "TMPDIR=/tmp", "GITHUB_ACTIONS=true"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// trivy runs in the directory it scans, so it would read that directory's
// trivy.yaml and .trivyignore: it gets an empty configuration and ignore
// file of infraharvest's own instead.
func TestTrivyIgnoresTheDirectorysConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in trivy is a shell script")
	}
	bin, dir := t.TempDir(), t.TempDir()
	args := filepath.Join(t.TempDir(), "args")
	// Logs each argument, and whether the files --config and
	// --ignorefile name are there and empty.
	script := `#!/bin/sh
for a; do
  echo "$a" >> '` + args + `'
  case "$a" in --config=*|--ignorefile=*) if [ -e "${a#*=}" ] && [ ! -s "${a#*=}" ]; then echo empty; else echo missing-or-not-empty; fi >> '` + args + `';; esac
done
echo '{}'
`
	if err := os.WriteFile(filepath.Join(bin, "trivy"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	scanners := DefaultScanners(nil)
	if len(scanners) != 1 || scanners[0].Name != "trivy" {
		t.Fatalf("scanners: %v", scanners)
	}
	if _, err := scanners[0].Run(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	for _, flag := range []string{"--config=", "--ignorefile="} {
		i := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, flag) })
		if i < 0 || i+1 >= len(lines) {
			t.Fatalf("no %s in %q", flag, lines)
		}
		if path := strings.TrimPrefix(lines[i], flag); path == "" || strings.HasPrefix(path, dir) || lines[i+1] != "empty" {
			t.Errorf("%s%s: %s, want an empty file outside %s", flag, path, lines[i+1], dir)
		}
	}
}
