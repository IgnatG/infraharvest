// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/report"
)

func writeExportReport(t *testing.T) string {
	t.Helper()
	out := t.TempDir()
	r := &report.Report{}
	r.Tool = report.Component{Name: "infraharvest", Version: "v0.1.0"}
	r.Engine = report.Component{Name: "terraform", Version: "1.17.0"}
	r.Provider = report.Provider{Source: "hashicorp/aws"}
	r.Exclude("aws/123456789012/eu-west-2", "aws_vpc", "vpc-1", report.Decided("default VPC"))
	r.Finish(map[string]int{"aws_vpc": 1}, nil, false)
	if err := r.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	return out
}

func runExport(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newExportCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), err
}

func TestExportCommand(t *testing.T) {
	t.Setenv("INFRAHARVEST_EXPORT_KEY", "")
	out := writeExportReport(t)

	stdout, err := runExport(t, out, "--schema", "run")
	if err != nil || !strings.Contains(stdout, `"schema": "infraharvest/run/v1"`) {
		t.Fatalf("run document: %v\n%s", err, stdout)
	}

	file := filepath.Join(t.TempDir(), "inventory.json")
	if stdout, err := runExport(t, out, "--out-file", file); err != nil || stdout != "" {
		t.Fatalf("--out-file: %v, %q", err, stdout)
	}
	content, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(content), "vpc-1") {
		t.Fatalf("inventory file: %v\n%s", err, content)
	}

	stdout, err = runExport(t, "--schema", "gate", "--print-schema")
	var schema struct {
		ID string `json:"$id"`
	}
	if err != nil || json.Unmarshal([]byte(stdout), &schema) != nil || !strings.HasSuffix(schema.ID, "gate-v1.schema.json") {
		t.Fatalf("--print-schema: %v\n%s", err, stdout)
	}

	if _, err := runExport(t, out, "--hash-ids"); err == nil {
		t.Error("--hash-ids without a key worked")
	}
	if _, err := runExport(t, out, "--hash-key-file", file); err == nil {
		t.Error("--hash-key-file without --hash-ids worked")
	}
	t.Setenv("INFRAHARVEST_EXPORT_KEY", "an-environment-key-0123")
	stdout, err = runExport(t, out, "--hash-ids")
	if err != nil || strings.Contains(stdout, "vpc-1") || !strings.Contains(stdout, `"ids_hashed": true`) {
		t.Fatalf("--hash-ids: %v\n%s", err, stdout)
	}
	if _, err := runExport(t, out, "--schema", "everything"); err == nil {
		t.Error("an unknown document worked")
	}
}

func TestExportTimes(t *testing.T) {
	t.Setenv("INFRAHARVEST_EXPORT_KEY", "")
	out := writeExportReport(t)

	stdout, err := runExport(t, out, "--schema", "run", "--started-at", "2026-11-03T09:30:00Z", "--finished-at", "2026-11-03T09:37:00Z")
	if err != nil || !strings.Contains(stdout, `"started_at": "2026-11-03T09:30:00Z"`) || !strings.Contains(stdout, `"finished_at": "2026-11-03T09:37:00Z"`) {
		t.Fatalf("run times: %v\n%s", err, stdout)
	}
	stdout, err = runExport(t, out, "--finished-at", "2026-11-03T11:37:00+02:00")
	if err != nil || !strings.Contains(stdout, `"generated_at": "2026-11-03T09:37:00Z"`) {
		t.Fatalf("generated_at: %v\n%s", err, stdout)
	}
	for _, args := range [][]string{
		{"--started-at", "yesterday"},
		{"--finished-at", "2026-11-03"},
		{"--started-at", "2026-11-03T09:37:00Z", "--finished-at", "2026-11-03T09:30:00Z"},
	} {
		if _, err := runExport(t, append([]string{out}, args...)...); err == nil {
			t.Errorf("%v worked", args)
		}
	}
}

func TestExportRunCommandAndRequestID(t *testing.T) {
	t.Setenv("INFRAHARVEST_EXPORT_KEY", "")
	out := writeExportReport(t)
	d := &report.Drift{Roots: []report.DriftRoot{{Path: "aws/123456789012/eu-west-2", Resources: 1}}}
	d.Finish()
	if err := d.WriteFiles(out); err != nil {
		t.Fatal(err)
	}

	// With both reports, the import's is the run, as before.
	stdout, err := runExport(t, out, "--schema", "run", "--request-id", "req-1")
	if err != nil || !strings.Contains(stdout, `"command": "import"`) || !strings.Contains(stdout, `"request_id": "req-1"`) {
		t.Fatalf("import run: %v\n%s", err, stdout)
	}
	stdout, err = runExport(t, out, "--schema", "run", "--command", "drift", "--request-id", "req-1")
	if err != nil || !strings.Contains(stdout, `"command": "drift"`) || !strings.Contains(stdout, `"request_id": "req-1"`) || strings.Contains(stdout, "exit_code") {
		t.Fatalf("drift run: %v\n%s", err, stdout)
	}
	for _, args := range [][]string{
		{"--schema", "run", "--command", "apply"},
		{"--schema", "run", "--request-id", "not valid"},
		{"--request-id", "req-1"},
		{"--command", "drift"},
	} {
		if _, err := runExport(t, append([]string{out}, args...)...); err == nil {
			t.Errorf("%v worked", args)
		}
	}
}

// discover and verify are named with --command, and each has the documents
// of its own run: the OCSF inventory of what was saved, or the gate.
func TestExportDiscoverAndVerify(t *testing.T) {
	t.Setenv("INFRAHARVEST_EXPORT_KEY", "")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "acme/infra")
	t.Setenv("GITHUB_SHA", strings.Repeat("a", 40))
	out := t.TempDir()
	inventories := filepath.Join(out, ".infraharvest", "inventory")
	if err := os.MkdirAll(inventories, 0o755); err != nil {
		t.Fatal(err)
	}
	inventory := `{"version":2,"provider":"aws","scope":"aws/123456789012/eu-west-2","services":["vpc"],"records":[{"service":"vpc","provider":"aws","type":"aws_vpc","id":"vpc-1","name":"main"}]}`
	if err := os.WriteFile(filepath.Join(inventories, "aws-0123456789abcdef.json"), []byte(inventory), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(out, report.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	roots := `[{"path":"aws/123456789012/eu-west-2","checks":[{"name":"G1 format","passed":true},{"name":"G3 plan","passed":false,"details":["aws_vpc.main: update"]}]}]`
	if err := os.WriteFile(filepath.Join(out, report.Dir, report.VerifyJSONFile), []byte(roots), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"--schema", "run", "--command", "discover", "--request-id", "req-1"}, []string{`"command": "discover"`, `"outcome": "ok"`, `"request_id": "req-1"`}},
		{[]string{"--schema", "ocsf-inventory", "--command", "discover"}, []string{`"ocsf_version"`, `"aws_vpc"`, `"status:unmanaged"`}},
		{[]string{"--schema", "run", "--command", "verify"}, []string{`"command": "verify"`, `"outcome": "failed"`, `"check_failed"`}},
		{[]string{"--schema", "gate", "--command", "verify"}, []string{`"passed": false`, `"plan_failed"`}},
	} {
		stdout, err := runExport(t, append([]string{out}, tc.args...)...)
		if err != nil {
			t.Errorf("%v: %v", tc.args, err)
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(stdout, want) {
				t.Errorf("%v: no %s in\n%s", tc.args, want, stdout)
			}
		}
		if strings.Contains(stdout, "aws_vpc.main") {
			t.Errorf("%v: an address reached the document", tc.args)
		}
	}
	for _, args := range [][]string{
		{"--schema", "gate", "--command", "discover"},
		{"--schema", "ocsf-inventory", "--command", "verify"},
		{"--schema", "run"}, // neither report is the import's or the drift's
	} {
		if _, err := runExport(t, append([]string{out}, args...)...); err == nil {
			t.Errorf("%v worked", args)
		}
	}
}

func TestExportDriftSummary(t *testing.T) {
	t.Setenv("INFRAHARVEST_EXPORT_KEY", "")
	out := t.TempDir()
	d := &report.Drift{
		Roots: []report.DriftRoot{{Path: "aws/123456789012/eu-west-2", Resources: 2}},
		Changes: []report.DriftChange{{
			Root: "aws/123456789012/eu-west-2", Address: "aws_s3_bucket.audit", Type: "aws_s3_bucket", Attribute: "tags.Env",
			Before: "prod-value-1", After: "dev-value-2", Kind: "changed", Decision: report.DriftKeep, Outcome: report.DriftKept,
		}},
	}
	d.Finish()
	if err := d.WriteFiles(out); err != nil {
		t.Fatal(err)
	}

	stdout, err := runExport(t, out, "--schema", "drift-summary")
	if err != nil || !strings.Contains(stdout, `"schema": "infraharvest/drift-summary/v1"`) || !strings.Contains(stdout, `"aws_s3_bucket.audit"`) ||
		strings.Contains(stdout, "prod-value-1") || strings.Contains(stdout, "dev-value-2") {
		t.Fatalf("drift summary: %v\n%s", err, stdout)
	}
	stdout, err = runExport(t, out, "--schema", "drift-summary", "--drop-addresses")
	if err != nil || strings.Contains(stdout, "aws_s3_bucket.audit") || !strings.Contains(stdout, `"addresses": "dropped"`) {
		t.Fatalf("--drop-addresses: %v\n%s", err, stdout)
	}
	t.Setenv("INFRAHARVEST_EXPORT_KEY", "an-environment-key-0123")
	stdout, err = runExport(t, out, "--schema", "drift-summary", "--hash-ids")
	if err != nil || strings.Contains(stdout, "aws_s3_bucket.audit") || !strings.Contains(stdout, `"addresses": "hashed"`) {
		t.Fatalf("--hash-ids: %v\n%s", err, stdout)
	}
	stdout, err = runExport(t, "--schema", "drift-summary", "--print-schema")
	var schema struct {
		ID string `json:"$id"`
	}
	if err != nil || json.Unmarshal([]byte(stdout), &schema) != nil || !strings.HasSuffix(schema.ID, "drift-summary-v1.schema.json") {
		t.Fatalf("--print-schema: %v\n%s", err, stdout)
	}
	// A directory of an import has no drift to summarise.
	if _, err := runExport(t, writeExportReport(t), "--schema", "drift-summary"); err == nil {
		t.Error("a drift summary of an import worked")
	}
}

func TestExportOCSFInventory(t *testing.T) {
	t.Setenv("INFRAHARVEST_EXPORT_KEY", "")
	t.Setenv("GITHUB_SERVER_URL", "")
	t.Setenv("GITHUB_REPOSITORY", "")
	t.Setenv("GITHUB_SHA", "")
	out := writeExportReport(t)
	const sha = "0123456789abcdef0123456789abcdef01234567"

	stdout, err := runExport(t, out, "--schema", "ocsf-inventory", "--repo-url", "https://github.com/o/r", "--commit-sha", sha)
	if err != nil || !strings.Contains(stdout, `"schema": "infraharvest/ocsf-inventory/v1"`) || !strings.Contains(stdout, `"class_uid": 5023`) {
		t.Fatalf("ocsf-inventory: %v\n%s", err, stdout)
	}
	stdout, err = runExport(t, "--schema", "ocsf-inventory", "--print-schema")
	var schema struct {
		ID string `json:"$id"`
	}
	if err != nil || json.Unmarshal([]byte(stdout), &schema) != nil || !strings.HasSuffix(schema.ID, "ocsf-inventory-v1.schema.json") {
		t.Fatalf("--print-schema: %v\n%s", err, stdout)
	}
	// The repository and the commit are for the OCSF inventory only, and
	// must be what they say.
	for _, args := range [][]string{
		{"--repo-url", "https://github.com/o/r"},
		{"--schema", "run", "--commit-sha", sha},
		{"--schema", "ocsf-inventory", "--repo-url", "https://user:pw@github.com/o/r"},
		{"--schema", "ocsf-inventory", "--commit-sha", "main"},
	} {
		if _, err := runExport(t, append([]string{out}, args...)...); err == nil {
			t.Errorf("%v worked", args)
		}
	}
}

// The GitHub Actions environment names the repository and the commit for
// the OCSF inventory only, and the flags win.
func TestExportRepoFromEnvironment(t *testing.T) {
	t.Setenv("INFRAHARVEST_EXPORT_KEY", "")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com/")
	t.Setenv("GITHUB_REPOSITORY", "owner/infra")
	t.Setenv("GITHUB_SHA", "0123456789abcdef0123456789abcdef01234567")
	out := writeExportReport(t)

	// Another document isn't refused for what the environment sets.
	for _, schema := range []string{"inventory", "run", "gate", "graph"} {
		if _, err := runExport(t, out, "--schema", schema); err != nil {
			t.Errorf("%s: %v", schema, err)
		}
	}
	if _, err := runExport(t, out, "--schema", "ocsf-inventory"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		flagURL, flagSHA, wantURL, wantSHA string
	}{
		{"", "", "https://github.com/owner/infra", "0123456789abcdef0123456789abcdef01234567"},
		{"https://git.example.com/o/r", "", "https://git.example.com/o/r", "0123456789abcdef0123456789abcdef01234567"},
		{"", strings.Repeat("ab", 20), "https://github.com/owner/infra", strings.Repeat("ab", 20)},
	} {
		url, sha := repoFromEnv(tc.flagURL, tc.flagSHA, os.Getenv)
		if url != tc.wantURL || sha != tc.wantSHA {
			t.Errorf("%+v: got %s, %s", tc, url, sha)
		}
	}
	none := func(string) string { return "" }
	if url, sha := repoFromEnv("", "", none); url != "" || sha != "" {
		t.Errorf("without the environment: %q, %q", url, sha)
	}
	onlyServer := func(k string) string {
		if k == "GITHUB_SERVER_URL" {
			return "https://github.com"
		}
		return ""
	}
	if url, _ := repoFromEnv("", "", onlyServer); url != "" {
		t.Errorf("a server without a repository: %q", url)
	}
}

// --command incremental describes the import's report as an incremental
// import, with the import's fields, and only the run document takes it.
func TestExportIncrementalRun(t *testing.T) {
	t.Setenv("INFRAHARVEST_EXPORT_KEY", "")
	out := writeExportReport(t)
	stdout, err := runExport(t, out, "--schema", "run", "--command", "incremental", "--request-id", "req-1")
	if err != nil || !strings.Contains(stdout, `"command": "incremental"`) || !strings.Contains(stdout, `"exit_code"`) || !strings.Contains(stdout, `"coverage"`) {
		t.Fatalf("incremental run: %v\n%s", err, stdout)
	}
	if _, err := runExport(t, out, "--schema", "gate", "--command", "incremental"); err == nil {
		t.Error("incremental took the gate")
	}
	// Without the import's report there is nothing to describe.
	if _, err := runExport(t, t.TempDir(), "--schema", "run", "--command", "incremental"); err == nil {
		t.Error("incremental worked without a report")
	}
}

// The OCSF findings are the checks of the gate that failed or were skipped;
// the repository comes from the flags or the environment as the OCSF
// inventory's does, and the paths are hashed or left out.
func TestExportOCSFFindings(t *testing.T) {
	t.Setenv("INFRAHARVEST_EXPORT_KEY", "")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "owner/infra")
	t.Setenv("GITHUB_SHA", "0123456789abcdef0123456789abcdef01234567")
	out := t.TempDir()
	r := &report.Report{}
	r.Tool = report.Component{Name: "infraharvest", Version: "v0.1.0"}
	r.Engine = report.Component{Name: "terraform", Version: "1.17.0"}
	r.Provider = report.Provider{Source: "hashicorp/aws"}
	r.Directories = []report.Directory{{
		Path: "aws/123456789012/eu-west-2", Scope: "aws/123456789012/eu-west-2",
		Checks: []report.Check{{Name: "G6 secrets", Passed: false, Details: []string{"main.tf holds a value"}}, {Name: "G1 format", Passed: true}},
	}}
	r.Finish(nil, nil, false)
	if err := r.WriteFiles(out); err != nil {
		t.Fatal(err)
	}

	stdout, err := runExport(t, out, "--schema", "ocsf-findings")
	for _, want := range []string{`"schema": "infraharvest/ocsf-findings/v1"`, `"class_uid": 2004`, `"title": "G6 secrets"`, `"repo_url": "https://github.com/owner/infra"`, `"path": "aws/123456789012/eu-west-2"`} {
		if err != nil || !strings.Contains(stdout, want) {
			t.Fatalf("missing %s: %v\n%s", want, err, stdout)
		}
	}
	if strings.Contains(stdout, "main.tf") || strings.Contains(stdout, "G1 format") {
		t.Errorf("the document holds a detail or a check that passed:\n%s", stdout)
	}
	stdout, err = runExport(t, out, "--schema", "ocsf-findings", "--drop-addresses")
	if err != nil || strings.Contains(stdout, `"path"`) || strings.Contains(stdout, `"uid": "aws/`) {
		t.Errorf("--drop-addresses: %v\n%s", err, stdout)
	}
	t.Setenv("INFRAHARVEST_EXPORT_KEY", "0123456789abcdef-key-a")
	stdout, err = runExport(t, out, "--schema", "ocsf-findings", "--hash-ids")
	if err != nil || strings.Contains(stdout, `"path": "aws/`) || !strings.Contains(stdout, `"ids_hashed": true`) {
		t.Errorf("--hash-ids: %v\n%s", err, stdout)
	}
	stdout, err = runExport(t, "--schema", "ocsf-findings", "--print-schema")
	if err != nil || !strings.Contains(stdout, "ocsf-findings-v1.schema.json") {
		t.Errorf("--print-schema: %v\n%s", err, stdout)
	}
}
