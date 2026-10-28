// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// CheckScanners is the verification gate's check with external scanners (G5).
const CheckScanners = "G5 scanners"

// scanTimeout bounds one scanner's run on one directory.
const scanTimeout = 10 * time.Minute

// maxReportedFindings bounds the findings a check lists per scanner.
const maxReportedFindings = 20

// ScannerSlots bound the scanner processes running at once, as many as the
// machine has CPUs, and at least two, over every directory the gate checks
// at the same time: the roots of one import, which --parallel checks
// together, share one (see Options.ScannerSlots). They belong to whoever
// runs those roots, so another run in the process, such as another MCP
// call, has slots of its own. The zero value is ready to use.
type ScannerSlots struct {
	once  sync.Once
	slots chan struct{}
}

// channel returns the slots, made on first use.
func (s *ScannerSlots) channel() chan struct{} {
	s.once.Do(func() { s.slots = make(chan struct{}, max(2, runtime.NumCPU())) })
	return s.slots
}

// Finding is one thing a scanner reports.
type Finding struct {
	Rule, Severity, Location, Message string
	// Resource is the address, in the directory scanned, of the resource
	// or module call the finding is about, such as aws_s3_bucket.logs or
	// module.logs.aws_s3_bucket.this: as the scanner names it, or else
	// the block at the place it gives (see locateFindings). A finding
	// about no resource, such as one about a variable, has none.
	Resource string `json:",omitempty"`
	// Scanner is the name of the scanner that reported it.
	Scanner string `json:",omitempty"`
	// at is where the scanner found it, for locateFindings.
	at position
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
// trivy and checkov; and, with policies, conftest, which checks the
// configuration against the user's OPA policies in those directories,
// absolute paths. The caller checks conftest is installed when there are
// policies: asked for, it is never left out quietly.
func DefaultScanners(policies []string) []Scanner {
	var scanners []Scanner
	if path, err := exec.LookPath("tflint"); err == nil {
		scanners = append(scanners, Scanner{Name: "tflint", Blocking: true, Run: func(ctx context.Context, dir string) ([]Finding, error) {
			// A configuration of infraharvest's own, not the user's
			// ~/.tflint.hcl, whose plugins would run and whose rules would
			// change what fails the check: the bundled Terraform rules, at
			// their recommended preset.
			config, err := writeTemp("infraharvest-tflint-*.hcl", []byte(tflintConfig))
			if err != nil {
				return nil, err
			}
			defer os.Remove(config)
			out, err := runTool(ctx, dir, path, "--chdir="+dir, "--config="+config, "--format=json", "--force")
			if err != nil {
				return nil, err
			}
			return parseTflint(out)
		}})
	}
	if path, err := exec.LookPath("trivy"); err == nil {
		scanners = append(scanners, Scanner{Name: "trivy", Run: func(ctx context.Context, dir string) ([]Finding, error) {
			out, err := runTool(ctx, dir, path, "config", "--format=json", "--quiet", "--exit-code=0", "--skip-dirs=**/.terraform", dir)
			if err != nil {
				return nil, err
			}
			return parseTrivy(out)
		}})
	}
	if path, err := exec.LookPath("checkov"); err == nil {
		scanners = append(scanners, Scanner{Name: "checkov", Run: func(ctx context.Context, dir string) ([]Finding, error) {
			// --skip-download: nothing goes to or comes from the Prisma
			// Cloud platform, which an API key in the environment would
			// otherwise upload the configuration to. Its secrets scan reads
			// every file but the local state of a root applied without a
			// backend, which holds secret values (see LocalStateFile).
			out, err := runTool(ctx, dir, path, "--directory", dir, "--skip-path", ".terraform", "--skip-path", `\.tfstate`, "--output", "json", "--quiet", "--compact", "--soft-fail", "--skip-download")
			if err != nil {
				return nil, err
			}
			return parseCheckov(out)
		}})
	}
	if path, err := exec.LookPath(ConftestBinary); err == nil && len(policies) > 0 {
		scanners = append(scanners, conftestScanner(path, policies))
	}
	return scanners
}

// ConftestBinary is the scanner that checks configuration against OPA
// policies.
const ConftestBinary = "conftest"

// conftestScanner returns the scanner that runs conftest, at path, with
// the policies in the directories policies, on the .tf files of the
// directory it scans, read with its HCL2 parser, in every namespace the
// policies have. Its findings are listed, never blocking, as trivy's are:
// a policy says what the infrastructure should be, which the
// configuration mirrors as it is. The policies are local directories
// only, and nothing is fetched: no --update, which downloads policies
// before the test. They run without the built-in functions that reach
// the network or read files (see deniedBuiltins), so that a policy can't
// send the configuration anywhere, or read what conftest wasn't given.
func conftestScanner(path string, policies []string) Scanner {
	return Scanner{Name: ConftestBinary, Run: func(ctx context.Context, dir string) ([]Finding, error) {
		files, err := filepath.Glob(filepath.Join(dir, "*.tf"))
		if err != nil || len(files) == 0 {
			return nil, err
		}
		content, err := conftestCapabilities()
		if err != nil {
			return nil, fmt.Errorf("OPA capabilities: %w", err)
		}
		capabilities, err := writeTemp("infraharvest-opa-capabilities-*.json", content)
		if err != nil {
			return nil, err
		}
		defer os.Remove(capabilities)
		args := []string{"test", "--parser=hcl2", "--all-namespaces", "--output=json", "--no-color", "--no-fail", "--capabilities=" + capabilities}
		for _, p := range policies {
			args = append(args, "--policy="+p)
		}
		// Named relative to dir, where it runs, so that findings name the
		// file as the other scanners do.
		args = append(args, "--")
		for _, f := range files {
			args = append(args, filepath.Base(f))
		}
		out, err := runTool(ctx, dir, path, args...)
		if err != nil {
			return nil, err
		}
		return parseConftest(out)
	}}
}

// writeTemp writes content to a new temporary file, named after pattern
// as os.CreateTemp names it, and returns its path, for the caller to
// remove.
func writeTemp(pattern string, content []byte) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	_, werr := f.Write(content)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(f.Name())
		return "", werr
	}
	return f.Name(), nil
}

// tflintConfig is the configuration tflint runs with: its bundled
// Terraform rules only.
const tflintConfig = `plugin "terraform" {
  enabled = true
  preset  = "recommended"
}
`

// runTool runs a scanner in dir, the directory it scans, so that it reads
// no configuration file of the directory infraharvest runs in, such as a
// .checkov.yaml or trivy.yaml, and returns its standard output. The
// scanner gets the environment without credentials (see scannerEnv).
func runTool(ctx context.Context, dir, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Env = scannerEnv(os.Environ())
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

// scannerCredentials are the prefixes of the environment variables that
// hold credentials, which no scanner needs to read configuration: the
// clouds', Terraform's, and checkov's platform key (BC_API_KEY), with which
// it uploads what it scans.
var scannerCredentials = []string{
	"AWS_", "ARM_", "AZURE_", "GOOGLE_", "CLOUDSDK_", "GCLOUD_", "TF_TOKEN_", "TF_VAR_",
	"BC_", "PRISMA_", "GITHUB_TOKEN", "GH_TOKEN",
}

// scannerEnv returns environ without the variables scannerCredentials
// name.
func scannerEnv(environ []string) []string {
	var env []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if !slices.ContainsFunc(scannerCredentials, func(prefix string) bool { return strings.HasPrefix(upper, prefix) }) {
			env = append(env, kv)
		}
	}
	return env
}

// runScanners runs scanners on dir, at the same time, as they only read
// it, each in one of slots (nil gives the call slots of its own), and
// reports them in their order. Blocking scanners' findings at warning
// level or above fail the check; the others' are listed, up to
// maxReportedFindings each, and the check keeps them all, with the
// resource each is about (see Check.Findings). With no scanner
// installed, the check is skipped.
func runScanners(ctx context.Context, dir string, scanners []Scanner, slots *ScannerSlots) Check {
	check := Check{Name: CheckScanners, Passed: true}
	if len(scanners) == 0 {
		check.Skipped = true
		check.Details = []string{"no scanner installed (tflint, trivy, checkov)"}
		return check
	}
	// Absolute, so that a directory starting with a dash isn't taken for
	// a flag, and so that findings name the same path whatever the
	// working directory.
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	type result struct {
		findings []Finding
		err      error
	}
	if slots == nil {
		slots = &ScannerSlots{}
	}
	bound := slots.channel()
	results := make([]result, len(scanners))
	var wg sync.WaitGroup
	for i, s := range scanners {
		wg.Go(func() {
			select {
			case bound <- struct{}{}:
				defer func() { <-bound }()
			case <-ctx.Done():
				results[i].err = ctx.Err()
				return
			}
			results[i].findings, results[i].err = s.Run(ctx, dir)
		})
	}
	wg.Wait()
	for i, s := range scanners {
		findings, err := results[i].findings, results[i].err
		if err != nil {
			check.Passed = false
			check.Details = append(check.Details, fmt.Sprintf("%s couldn't run: %v", s.Name, err))
			continue
		}
		locateFindings(dir, findings)
		for i := range findings {
			findings[i].Scanner = s.Name
		}
		sort.Slice(findings, func(i, j int) bool { return findings[i].String() < findings[j].String() })
		check.Findings = append(check.Findings, findings...)
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
		findings = append(findings, Finding{Rule: i.Rule.Name, Severity: i.Rule.Severity, Location: fmt.Sprintf("%s:%d", i.Range.Filename, i.Range.Start.Line), Message: i.Message,
			at: position{file: filepath.ToSlash(i.Range.Filename), line: i.Range.Start.Line}})
	}
	for _, e := range result.Errors {
		findings = append(findings, Finding{Rule: "error", Severity: "error", Message: e.Message})
	}
	return findings, nil
}

// parseTrivy reads trivy's report: its failed checks, each about the
// resource its cause names, which, for a resource in a module call, is
// the call alone, at the line of the module's file the resource is at
// (see locateFindings).
func parseTrivy(out []byte) ([]Finding, error) {
	var result struct {
		Results []struct {
			Target            string `json:"Target"`
			Misconfigurations []struct {
				ID            string `json:"ID"`
				Title         string `json:"Title"`
				Severity      string `json:"Severity"`
				Status        string `json:"Status"`
				CauseMetadata struct {
					Resource  string `json:"Resource"`
					StartLine int    `json:"StartLine"`
				} `json:"CauseMetadata"`
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
				findings = append(findings, Finding{Rule: m.ID, Severity: m.Severity, Location: r.Target, Message: m.Title,
					Resource: addressOf(m.CauseMetadata.Resource), at: position{file: filepath.ToSlash(r.Target), line: m.CauseMetadata.StartLine}})
			}
		}
	}
	return findings, nil
}

// parseCheckov reads checkov's report: one object, or a list with one per
// framework. A failed check names the resource it is about, in module
// calls or not; its secrets scan's checks name none (see addressOf).
func parseCheckov(out []byte) ([]Finding, error) {
	type report struct {
		Results struct {
			FailedChecks []struct {
				CheckID   string `json:"check_id"`
				CheckName string `json:"check_name"`
				Severity  string `json:"severity"`
				FilePath  string `json:"file_path"`
				Lines     []int  `json:"file_line_range"`
				Resource  string `json:"resource"`
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
			file := strings.TrimPrefix(c.FilePath, "/")
			location, at := file, position{file: file}
			if len(c.Lines) > 0 {
				location = fmt.Sprintf("%s:%d", location, c.Lines[0])
				at.line = c.Lines[0]
			}
			findings = append(findings, Finding{Rule: c.CheckID, Severity: c.Severity, Location: location, Message: c.CheckName, Resource: addressOf(c.Resource), at: at})
		}
	}
	return findings, nil
}

// parseConftest reads conftest's report: a result per file and namespace,
// whose failures (deny rules) and warnings (warn rules) are findings, of
// the namespace's rule, at the file, saying the policy's message. A rule
// that returns an object, such as {"msg": ..., "resource":
// "aws_s3_bucket.logs"}, has its keys besides msg in the result's
// metadata: its resource is the address the finding is about; else the
// address the message starts with, if any, is. conftest gives no line.
func parseConftest(out []byte) ([]Finding, error) {
	type result struct {
		Msg      string         `json:"msg"`
		Metadata map[string]any `json:"metadata"`
	}
	about := func(r result) string {
		if resource, ok := r.Metadata["resource"].(string); ok {
			if address := addressOf(strings.TrimSpace(resource)); address != "" {
				return address
			}
		}
		return messageAddress(r.Msg)
	}
	var results []struct {
		Filename  string   `json:"filename"`
		Namespace string   `json:"namespace"`
		Warnings  []result `json:"warnings"`
		Failures  []result `json:"failures"`
	}
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("conftest output: %w", err)
	}
	var findings []Finding
	for _, r := range results {
		for _, f := range r.Failures {
			findings = append(findings, Finding{Rule: r.Namespace, Severity: "failure", Location: r.Filename, Message: f.Msg, Resource: about(f)})
		}
		for _, w := range r.Warnings {
			findings = append(findings, Finding{Rule: r.Namespace, Severity: "warning", Location: r.Filename, Message: w.Msg, Resource: about(w)})
		}
	}
	return findings, nil
}
