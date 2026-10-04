// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// CheckScanners is the verification gate's check with external scanners (G5).
const CheckScanners = "G5 scanners"

// scanTimeout bounds one scanner's run on one directory.
const scanTimeout = 10 * time.Minute

// maxReportedFindings bounds the findings a check lists per scanner.
const maxReportedFindings = 20

// Finding is one thing a scanner reports.
type Finding struct {
	Rule, Severity, Location, Message string
}

func (f Finding) String() string {
	s := f.Rule
	if f.Severity != "" {
		s += " (" + strings.ToLower(f.Severity) + ")"
	}
	if f.Location != "" {
		s += " " + f.Location
	}
	if f.Message != "" {
		s += ": " + f.Message
	}
	return s
}

// Scanner is an external tool the gate runs on each directory.
type Scanner struct {
	Name string
	// Blocking scanners check the configuration itself, such as tflint:
	// their findings are infraharvest's doing and fail the check. Others,
	// such as trivy, check the settings of the infrastructure, which the
	// configuration must mirror: their findings are reported, never fixed.
	Blocking bool
	Run      func(ctx context.Context, dir string) ([]Finding, error)
}

// DefaultScanners returns the scanners installed on PATH: tflint (blocking),
// trivy and checkov.
func DefaultScanners() []Scanner {
	var scanners []Scanner
	if path, err := exec.LookPath("tflint"); err == nil {
		scanners = append(scanners, Scanner{Name: "tflint", Blocking: true, Run: func(ctx context.Context, dir string) ([]Finding, error) {
			out, err := runTool(ctx, path, "--chdir="+dir, "--format=json", "--force")
			if err != nil {
				return nil, err
			}
			return parseTflint(out)
		}})
	}
	if path, err := exec.LookPath("trivy"); err == nil {
		scanners = append(scanners, Scanner{Name: "trivy", Run: func(ctx context.Context, dir string) ([]Finding, error) {
			out, err := runTool(ctx, path, "config", "--format=json", "--quiet", "--exit-code=0", "--skip-dirs=**/.terraform", dir)
			if err != nil {
				return nil, err
			}
			return parseTrivy(out)
		}})
	}
	if path, err := exec.LookPath("checkov"); err == nil {
		scanners = append(scanners, Scanner{Name: "checkov", Run: func(ctx context.Context, dir string) ([]Finding, error) {
			out, err := runTool(ctx, path, "--directory", dir, "--skip-path", ".terraform", "--output", "json", "--quiet", "--compact", "--soft-fail")
			if err != nil {
				return nil, err
			}
			return parseCheckov(out)
		}})
	}
	return scanners
}

// runTool runs a scanner and returns its standard output.
func runTool(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		// Scanners exit non-zero when they find something; the output says.
		if !errors.As(err, &exitErr) || stdout.Len() == 0 {
			return nil, fmt.Errorf("%s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
		}
	}
	return stdout.Bytes(), nil
}

// runScanners runs scanners on dir. Blocking scanners' findings at warning
// level or above fail the check; the others' are listed.
func runScanners(ctx context.Context, dir string, scanners []Scanner) Check {
	check := Check{Name: CheckScanners, Passed: true}
	if len(scanners) == 0 {
		check.Details = []string{"no scanner installed (tflint, trivy, checkov)"}
		return check
	}
	for _, s := range scanners {
		findings, err := s.Run(ctx, dir)
		if err != nil {
			check.Passed = false
			check.Details = append(check.Details, fmt.Sprintf("%s couldn't run: %v", s.Name, err))
			continue
		}
		sort.Slice(findings, func(i, j int) bool { return findings[i].String() < findings[j].String() })
		var blocking []Finding
		if s.Blocking {
			for _, f := range findings {
				if !strings.EqualFold(f.Severity, "notice") && !strings.EqualFold(f.Severity, "info") {
					blocking = append(blocking, f)
				}
			}
		}
		switch {
		case len(blocking) > 0:
			check.Passed = false
			check.Details = append(check.Details, fmt.Sprintf("%s: %d findings in the generated configuration", s.Name, len(blocking)))
		case len(findings) == 0:
			check.Details = append(check.Details, s.Name+": no findings")
			continue
		case s.Blocking:
			check.Details = append(check.Details, fmt.Sprintf("%s: %d notices", s.Name, len(findings)))
		default:
			check.Details = append(check.Details, fmt.Sprintf("%s: %d findings about the infrastructure's own settings, which the configuration mirrors: reported, not changed", s.Name, len(findings)))
		}
		for i, f := range findings {
			if i == maxReportedFindings {
				check.Details = append(check.Details, fmt.Sprintf("%s: %d more", s.Name, len(findings)-i))
				break
			}
			check.Details = append(check.Details, s.Name+": "+f.String())
		}
	}
	return check
}

func parseTflint(out []byte) ([]Finding, error) {
	var result struct {
		Issues []struct {
			Rule struct {
				Name     string `json:"name"`
				Severity string `json:"severity"`
			} `json:"rule"`
			Message string `json:"message"`
			Range   struct {
				Filename string `json:"filename"`
				Start    struct {
					Line int `json:"line"`
				} `json:"start"`
			} `json:"range"`
		} `json:"issues"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("tflint output: %w", err)
	}
	var findings []Finding
	for _, i := range result.Issues {
		findings = append(findings, Finding{Rule: i.Rule.Name, Severity: i.Rule.Severity, Location: fmt.Sprintf("%s:%d", i.Range.Filename, i.Range.Start.Line), Message: i.Message})
	}
	for _, e := range result.Errors {
		findings = append(findings, Finding{Rule: "error", Severity: "error", Message: e.Message})
	}
	return findings, nil
}

func parseTrivy(out []byte) ([]Finding, error) {
	var result struct {
		Results []struct {
			Target            string `json:"Target"`
			Misconfigurations []struct {
				ID       string `json:"ID"`
				Title    string `json:"Title"`
				Severity string `json:"Severity"`
				Status   string `json:"Status"`
			} `json:"Misconfigurations"`
		} `json:"Results"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("trivy output: %w", err)
	}
	var findings []Finding
	for _, r := range result.Results {
		for _, m := range r.Misconfigurations {
			if m.Status == "FAIL" {
				findings = append(findings, Finding{Rule: m.ID, Severity: m.Severity, Location: r.Target, Message: m.Title})
			}
		}
	}
	return findings, nil
}

// parseCheckov reads checkov's report: one object, or a list with one per
// framework.
func parseCheckov(out []byte) ([]Finding, error) {
	type report struct {
		Results struct {
			FailedChecks []struct {
				CheckID   string `json:"check_id"`
				CheckName string `json:"check_name"`
				Severity  string `json:"severity"`
				FilePath  string `json:"file_path"`
				Lines     []int  `json:"file_line_range"`
			} `json:"failed_checks"`
		} `json:"results"`
	}
	var reports []report
	if trimmed := bytes.TrimSpace(out); len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &reports); err != nil {
			return nil, fmt.Errorf("checkov output: %w", err)
		}
	} else {
		var r report
		if err := json.Unmarshal(trimmed, &r); err != nil {
			return nil, fmt.Errorf("checkov output: %w", err)
		}
		reports = []report{r}
	}
	var findings []Finding
	for _, r := range reports {
		for _, c := range r.Results.FailedChecks {
			location := strings.TrimPrefix(c.FilePath, "/")
			if len(c.Lines) > 0 {
				location = fmt.Sprintf("%s:%d", location, c.Lines[0])
			}
			findings = append(findings, Finding{Rule: c.CheckID, Severity: c.Severity, Location: location, Message: c.CheckName})
		}
	}
	return findings, nil
}
