// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/version"
)

// writeDiscovered writes an output directory of a discover run: saved
// inventories, with attributes and tags, and no report. failures are what
// the listing says went wrong, if anything.
func writeDiscovered(t *testing.T, failures ...string) string {
	t.Helper()
	out := t.TempDir()
	dir := filepath.Join(out, inventoryDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, inventory map[string]any) {
		content, err := json.Marshal(inventory)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("aws-0123456789abcdef.json", map[string]any{
		"version": 2, "provider": "aws", "scope": scope, "services": []string{"s3", "vpc"}, "failures": failures,
		"records": []map[string]any{
			{"service": "s3", "provider": "aws", "type": "aws_s3_bucket", "id": "logs-bucket", "name": "logs",
				"tags":       map[string]string{keepTagKey: keepTagValue, secretTagKey: secretTagValue},
				"attributes": map[string]string{"master_password": secretAttribute}},
			{"service": "vpc", "provider": "aws", "type": "aws_vpc", "id": "vpc-1", "name": "main",
				"attributes": map[string]string{"secret_key": secretAttribute}},
		},
	})
	write("aws-fedcba9876543210.json", map[string]any{
		"version": 2, "provider": "aws", "scope": "aws/123456789012/us-east-1", "services": []string{"vpc"},
		"records": []map[string]any{{"service": "vpc", "provider": "aws", "type": "aws_vpc", "id": "vpc-2"}},
	})
	return out
}

func TestRunOfDiscover(t *testing.T) {
	t.Parallel()
	out := writeDiscovered(t)
	started, finished := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC), time.Date(2026, 11, 3, 9, 31, 0, 0, time.UTC)
	content := build(t, Options{Out: out, Document: DocRun, Command: CommandDiscover, RequestID: "01HZX-7", StartedAt: started, FinishedAt: finished})
	doc := decodeRun(t, content)
	for key, want := range map[string]any{
		"command": "discover", "outcome": "ok", "request_id": "01HZX-7",
		"started_at": "2026-11-03T09:30:00Z", "finished_at": "2026-11-03T09:31:00Z",
	} {
		if doc[key] != want {
			t.Errorf("%s is %v, want %v", key, doc[key], want)
		}
	}
	for _, key := range []string{"exit_code", "tool", "engine", "coverage", "failure_codes"} {
		if _, ok := doc[key]; ok {
			t.Errorf("a discover run has %s", key)
		}
	}
	if strings.Contains(string(content), secretAttribute) || strings.Contains(string(content), secretTagValue) {
		t.Errorf("a secret reached the run document:\n%s", content)
	}
	// The same inventories are the same run, and another listing is another.
	if again := build(t, Options{Out: out, Document: DocRun, Command: CommandDiscover}); decodeRun(t, again)["run_id"] != doc["run_id"] {
		t.Error("the run_id changed with nothing")
	}
	other := writeDiscovered(t)
	if err := os.Remove(filepath.Join(other, inventoryDir, "aws-fedcba9876543210.json")); err != nil {
		t.Fatal(err)
	}
	if decodeRun(t, build(t, Options{Out: other, Document: DocRun, Command: CommandDiscover}))["run_id"] == doc["run_id"] {
		t.Error("a listing of fewer scopes has the same run_id")
	}
}

func TestOutcomeOfDiscover(t *testing.T) {
	t.Parallel()
	partial := decodeRun(t, build(t, Options{Out: writeDiscovered(t, "tags: access denied for "+secretAttribute), Document: DocRun, Command: CommandDiscover}))
	if partial["outcome"] != "partial" || !slices.Equal(anyStrings(partial["failure_codes"]), []string{"run_failed"}) {
		t.Errorf("a listing that failed in part: %v", partial)
	}
	// Nothing was listed, and something failed.
	out := t.TempDir()
	dir := filepath.Join(out, inventoryDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	empty := `{"version":2,"provider":"aws","scope":"` + scope + `","services":["s3"],"failures":["s3: access denied"],"records":[]}`
	if err := os.WriteFile(filepath.Join(dir, "aws-0123456789abcdef.json"), []byte(empty), 0o600); err != nil {
		t.Fatal(err)
	}
	failed := decodeRun(t, build(t, Options{Out: out, Document: DocRun, Command: CommandDiscover}))
	if failed["outcome"] != "failed" || !slices.Equal(anyStrings(failed["failure_codes"]), []string{"run_failed"}) {
		t.Errorf("a listing that found nothing: %v", failed)
	}
	if strings.Contains(string(mustJSON(t, partial)), "access denied") {
		t.Error("a failure's message reached the run document")
	}
}

func anyStrings(v any) []string {
	var out []string
	items, _ := v.([]any)
	for _, item := range items {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	content, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestOCSFInventoryOfDiscover(t *testing.T) {
	t.Parallel()
	out := writeDiscovered(t)
	content := build(t, Options{Out: out, Document: DocOCSFInventory, Command: CommandDiscover, DropTags: []string{keepTagKey}})
	var doc ocsfDoc
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Events) != 3 {
		t.Fatalf("%d events, want one for each of the 3 resources", len(doc.Events))
	}
	run := decodeRun(t, build(t, Options{Out: out, Document: DocRun, Command: CommandDiscover}))
	if doc.RunID != run["run_id"] {
		t.Errorf("the OCSF inventory's run_id is %s, the run's %v", doc.RunID, run["run_id"])
	}
	for _, event := range doc.Events {
		if event.Cloud.Provider != "AWS" || event.Metadata.Product.Version != version.Version {
			t.Errorf("event: cloud %s, product %+v", event.Cloud.Provider, event.Metadata.Product)
		}
		res := event.Resources[0]
		if !slices.Contains(res.Labels, "status:unmanaged") || !slices.Contains(res.Labels, "status_basis:none") || res.IaC == nil || *res.IaC != (iacSource{OutcomeID: 4, Outcome: importNotImported}) {
			t.Errorf("%s %s: labels %v, iac %v", res.Type, res.UID, res.Labels, res.IaC)
		}
	}
	if doc.Events[0].Cloud.Region != "eu-west-2" || doc.Events[2].Cloud.Region != "us-east-1" {
		t.Errorf("regions: %s, %s", doc.Events[0].Cloud.Region, doc.Events[2].Cloud.Region)
	}
	for _, secret := range []string{secretAttribute, secretTagValue, keepTagValue} {
		if strings.Contains(string(content), secret) {
			t.Errorf("%q reached the OCSF inventory", secret)
		}
	}
	// The cloud comes from the provider when the scope says none.
	bare := t.TempDir()
	dir := filepath.Join(bare, inventoryDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	inventory := `{"version":2,"provider":"google","services":["gcs"],"records":[{"service":"gcs","provider":"google","type":"google_storage_bucket","id":"b"}]}`
	if err := os.WriteFile(filepath.Join(dir, "google-0123456789abcdef.json"), []byte(inventory), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(build(t, Options{Out: bare, Document: DocOCSFInventory, Command: CommandDiscover}), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Events[0].Cloud.Provider != "GCP" {
		t.Errorf("provider %s, want GCP", doc.Events[0].Cloud.Provider)
	}
}

func TestDiscoverNeedsAnInventory(t *testing.T) {
	t.Parallel()
	// A drift report is there, but the run is discover's.
	for _, out := range []string{t.TempDir(), writeDrift(t)} {
		for _, doc := range []string{DocRun, DocOCSFInventory} {
			if _, err := Build(Options{Out: out, Document: doc, Command: CommandDiscover}); err == nil || !strings.Contains(err.Error(), "run discover first") {
				t.Errorf("%s: %v", doc, err)
			}
		}
	}
}

// writeVerifyFile writes verify.json into a new output directory: the
// roots as infraharvest verify --format json has them, with details and
// findings that name a secret.
func writeVerifyFile(t *testing.T, roots []map[string]any) string {
	t.Helper()
	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(out, report.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	content, err := json.MarshalIndent(roots, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, report.Dir, report.VerifyJSONFile), content, 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

func verifiedRoot(path string, checks ...report.Check) map[string]any {
	return map[string]any{
		"path": path, "checks": checks,
		"findings": []map[string]any{{"address": "aws_s3_bucket.logs", "message": "holds " + secretAttribute}},
	}
}

func passedChecks() []report.Check {
	return []report.Check{
		{Name: "G1 format", Passed: true},
		{Name: "G2 validate", Passed: true},
		{Name: "G5 scanners", Passed: true, Skipped: true, Details: []string{"no scanner installed (tflint, trivy, checkov)"}},
	}
}

func failedChecks() []report.Check {
	return []report.Check{
		{Name: "G1 format", Passed: true},
		{Name: "G6 secrets", Passed: false, Details: []string{"main.tf would hold " + secretAttribute}},
	}
}

func TestRunOfVerify(t *testing.T) {
	t.Parallel()
	uncheckable := map[string]any{"path": "aws/1/us-east-1", "checks": []report.Check{}, "error": "terraform init: access denied for " + secretAttribute}
	for name, tc := range map[string]struct {
		roots   []map[string]any
		outcome string
		codes   []string
		passed  bool
	}{
		"every check passed":   {[]map[string]any{verifiedRoot("a", passedChecks()...), verifiedRoot("b", passedChecks()...)}, "ok", nil, true},
		"a check failed":       {[]map[string]any{verifiedRoot("a", passedChecks()...), verifiedRoot("b", failedChecks()...)}, "failed", []string{"check_failed"}, false},
		"a root not checked":   {[]map[string]any{verifiedRoot("a", passedChecks()...), uncheckable}, "partial", []string{"roots_not_checked"}, false},
		"no root checked":      {[]map[string]any{uncheckable}, "failed", []string{"roots_not_checked"}, false},
		"both of them":         {[]map[string]any{verifiedRoot("a", failedChecks()...), uncheckable}, "failed", []string{"check_failed", "roots_not_checked"}, false},
		"a skipped check only": {[]map[string]any{verifiedRoot("a", report.Check{Name: "G5 scanners", Passed: true, Skipped: true})}, "ok", nil, true},
	} {
		out := writeVerifyFile(t, tc.roots)
		started, finished := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC), time.Date(2026, 11, 3, 9, 40, 0, 0, time.UTC)
		runContent := build(t, Options{Out: out, Document: DocRun, Command: CommandVerify, RequestID: "req-1", StartedAt: started, FinishedAt: finished})
		run := decodeRun(t, runContent)
		if run["command"] != "verify" || run["outcome"] != tc.outcome || !slices.Equal(anyStrings(run["failure_codes"]), tc.codes) {
			t.Errorf("%s: run %v", name, run)
		}
		if run["request_id"] != "req-1" || run["started_at"] != "2026-11-03T09:30:00Z" || run["finished_at"] != "2026-11-03T09:40:00Z" {
			t.Errorf("%s: request or times of %v", name, run)
		}
		for _, key := range []string{"exit_code", "tool", "engine", "coverage"} {
			if _, ok := run[key]; ok {
				t.Errorf("%s: a verify run has %s", name, key)
			}
		}
		gateContent := build(t, Options{Out: out, Document: DocGate, Command: CommandVerify})
		var gate gateDoc
		if err := json.Unmarshal(gateContent, &gate); err != nil {
			t.Fatal(err)
		}
		if gate.Passed != tc.passed || gate.RunID != run["run_id"] {
			t.Errorf("%s: gate passed %v, run_id %s (the run's %v)", name, gate.Passed, gate.RunID, run["run_id"])
		}
		for _, secret := range []string{secretAttribute, "aws_s3_bucket.logs", "access denied"} {
			if strings.Contains(string(runContent), secret) || strings.Contains(string(gateContent), secret) {
				t.Errorf("%s: %q reached a document", name, secret)
			}
		}
	}
}

func TestGateOfVerify(t *testing.T) {
	t.Parallel()
	out := writeVerifyFile(t, []map[string]any{verifiedRoot("a", passedChecks()...), verifiedRoot("b", failedChecks()...)})
	var gate gateDoc
	if err := json.Unmarshal(build(t, Options{Out: out, Document: DocGate, Command: CommandVerify}), &gate); err != nil {
		t.Fatal(err)
	}
	got := map[string]gateCheck{}
	for _, c := range gate.Checks {
		got[c.ID] = c
	}
	// G1 ran in both roots, G6 failed in one, and G5 was skipped in the
	// only root that ran it.
	if !got["G1"].Passed || got["G1"].Skipped || got["G6"].Passed || !slices.Equal(got["G6"].ReasonCodes, []string{"secrets_found"}) ||
		!got["G5"].Skipped || !slices.Equal(got["G5"].ReasonCodes, []string{"tool_missing"}) || len(gate.Checks) != 4 {
		t.Errorf("checks: %+v", gate.Checks)
	}
}

func TestVerifyNeedsItsFile(t *testing.T) {
	t.Parallel()
	for _, out := range []string{t.TempDir(), writeOut(t), writeDrift(t)} {
		for _, doc := range []string{DocRun, DocGate} {
			if _, err := Build(Options{Out: out, Document: doc, Command: CommandVerify}); err == nil {
				t.Errorf("%s of a directory without %s was built", doc, report.VerifyJSONFile)
			}
		}
	}
	for name, content := range map[string]string{"empty": "[]", "not a list": "{}", "not JSON": "roots"} {
		out := t.TempDir()
		if err := os.MkdirAll(filepath.Join(out, report.Dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, report.Dir, report.VerifyJSONFile), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Build(Options{Out: out, Document: DocRun, Command: CommandVerify}); err == nil {
			t.Errorf("%s: built", name)
		}
	}
}

// Discover and verify have to be named, and name only the documents their
// runs have.
func TestCommandsAndTheirDocuments(t *testing.T) {
	t.Parallel()
	discovered := writeDiscovered(t)
	verified := writeVerifyFile(t, []map[string]any{verifiedRoot("a", passedChecks()...)})
	for name, tc := range map[string]struct {
		out, doc, command string
		ok                bool
	}{
		"discover, run":          {discovered, DocRun, CommandDiscover, true},
		"discover, ocsf":         {discovered, DocOCSFInventory, CommandDiscover, true},
		"discover, inventory":    {discovered, DocInventory, CommandDiscover, false},
		"discover, gate":         {discovered, DocGate, CommandDiscover, false},
		"discover, graph":        {discovered, DocGraph, CommandDiscover, false},
		"discover, drift":        {discovered, DocDriftSummary, CommandDiscover, false},
		"verify, run":            {verified, DocRun, CommandVerify, true},
		"verify, gate":           {verified, DocGate, CommandVerify, true},
		"verify, ocsf":           {verified, DocOCSFInventory, CommandVerify, false},
		"verify, inventory":      {verified, DocInventory, CommandVerify, false},
		"verify, drift":          {verified, DocDriftSummary, CommandVerify, false},
		"import, ocsf":           {writeOut(t), DocOCSFInventory, CommandImport, false},
		"drift, gate":            {writeDrift(t), DocGate, CommandDrift, false},
		"unnamed, verify only":   {verified, DocRun, "", false},
		"unnamed, discover only": {discovered, DocRun, "", false},
	} {
		_, err := Build(Options{Out: tc.out, Document: tc.doc, Command: tc.command, Now: fixedNow})
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A directory of an import that was verified is still the import's.
	both := writeOut(t)
	file, err := os.ReadFile(filepath.Join(verified, report.Dir, report.VerifyJSONFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(both, report.Dir, report.VerifyJSONFile), file, 0o644); err != nil {
		t.Fatal(err)
	}
	if decodeRun(t, build(t, Options{Out: both, Document: DocRun}))["command"] != "import" {
		t.Error("verify.json changed which run the directory is of")
	}
}

// The run document's schema asks an import for what the other commands
// don't have.
func TestRunSchemaOfDiscoverAndVerify(t *testing.T) {
	t.Parallel()
	for command, out := range map[string]string{
		CommandDiscover: writeDiscovered(t),
		CommandVerify:   writeVerifyFile(t, []map[string]any{verifiedRoot("a", passedChecks()...)}),
	} {
		doc := decodeRun(t, build(t, Options{Out: out, Document: DocRun, Command: command}))
		if err := validate(DocRun, mustJSON(t, doc)); err != nil {
			t.Errorf("%s: %v", command, err)
		}
		doc["exit_code"], doc["roots"] = 0, 1
		if err := validate(DocRun, mustJSON(t, doc)); err == nil {
			t.Errorf("%s: an extra field passed", command)
		}
	}
}
