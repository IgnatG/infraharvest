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
		{"--schema", "run", "--command", "verify"},
		{"--schema", "run", "--request-id", "not valid"},
		{"--request-id", "req-1"},
		{"--command", "drift"},
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
