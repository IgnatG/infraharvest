// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/report"
)

// Values that must never reach an export of drift.
const (
	driftSecretBefore = "old-Hunter2-value-0xCAFE"
	driftSecretAfter  = "new-Hunter2-value-0xBEEF"
)

// driftChanges are the changes of writeDrift: two roots, one of an
// environment, one change of tags several resources share, and one change
// of each outcome.
func driftChanges() []report.DriftChange {
	const root = "aws/123456789012/eu-west-2"
	change := func(root, env, address, typ, outcome string) report.DriftChange {
		decision := report.DriftKeep
		if outcome != report.DriftKept && outcome != report.DriftAbsorbed {
			decision = report.DriftAccept
		}
		return report.DriftChange{
			Root: root, Environment: env, Address: address, Type: typ, Attribute: "password",
			Before: driftSecretBefore, After: driftSecretAfter, Kind: "changed", Decision: decision, Outcome: outcome,
			Reason:     "changed by " + driftSecretAfter,
			LastChange: &report.DriftLastChange{By: "arn:aws:iam::123456789012:user/alice", Call: "ModifyDBInstance", At: "2026-11-03T09:30:00Z", Log: "CloudTrail"},
		}
	}
	return []report.DriftChange{
		change(root, "", "aws_s3_bucket.logs", "aws_s3_bucket", report.DriftKept),
		change(root, "", "aws_s3_bucket.logs", "aws_s3_bucket", report.DriftWritten),
		change(root, "", `aws_s3_bucket.named["team-blue"]`, "aws_s3_bucket", report.DriftManual),
		change(root, "", "module.db.aws_db_instance.main", "aws_db_instance", report.DriftSecret),
		change(root, "", "aws_sqs_queue.jobs", "aws_sqs_queue", report.DriftAbsorbed),
		change(root, "", "aws_sqs_queue.dead", "aws_sqs_queue", report.DriftNotAssessable),
		change(root, "", "aws_sqs_queue.late", "aws_sqs_queue", report.DriftRejectedByPlan),
		change(root, "", "aws_sqs_queue.later", "aws_sqs_queue", report.DriftRejectedByCheck),
		{
			Root: root, Address: "local.tags", Kind: "changed", Attribute: "tags.Env", Before: "a", After: "b", Decision: report.DriftKeep, Outcome: report.DriftKept,
			Covers: []string{"aws_s3_bucket.logs", "aws_sqs_queue.jobs"},
		},
		change("aws/123456789012/us-east-1", "prod", "aws_vpc.main", "aws_vpc", report.DriftKept),
		change("aws/123456789012/us-east-1", "dev", "aws_vpc.main", "aws_vpc", report.DriftKept),
	}
}

// writeDrift writes the drift report of two roots checked and a third that
// couldn't be, into a new output directory, which it returns.
func writeDrift(t *testing.T) string {
	t.Helper()
	out := t.TempDir()
	d := &report.Drift{
		Roots: []report.DriftRoot{
			{Path: "aws/123456789012/eu-west-2", Resources: 4},
			{Path: "aws/123456789012/us-east-1", Environment: "prod", Resources: 1},
			{Path: "aws/123456789012/us-east-1", Environment: "dev", Resources: 1},
			{Path: "aws/123456789012/ap-south-1", Error: "terraform init: access denied for " + driftSecretAfter},
		},
		Changes: driftChanges(),
	}
	d.Finish()
	if err := d.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	return out
}

func decodeRun(t *testing.T, content []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestRunOfDrift(t *testing.T) {
	t.Parallel()
	out := writeDrift(t)
	content := build(t, Options{Out: out, Document: DocRun})
	doc := decodeRun(t, content)
	if doc["schema"] != schemaRun || doc["command"] != CommandDrift || doc["outcome"] != outcomePartial {
		t.Errorf("run: %v", doc)
	}
	for _, key := range []string{"exit_code", "tool", "engine", "coverage", "started_at", "finished_at", "request_id"} {
		if _, ok := doc[key]; ok {
			t.Errorf("a drift run has %s: %v", key, doc[key])
		}
	}
	if codes, _ := doc["failure_codes"].([]any); len(codes) != 1 || codes[0] != failRootsNotChecked {
		t.Errorf("failure codes: %v", doc["failure_codes"])
	}
	// Its ID is made of drift.json, as an import's is of report.json.
	drift, err := os.ReadFile(filepath.Join(out, report.Dir, report.DriftJSONFile))
	if err != nil {
		t.Fatal(err)
	}
	if doc["run_id"] != runID(drift) {
		t.Errorf("run_id %v, want %s", doc["run_id"], runID(drift))
	}
	for _, leak := range []string{driftSecretBefore, driftSecretAfter, "alice", "aws_s3_bucket", "password"} {
		if strings.Contains(string(content), leak) {
			t.Errorf("the run of drift holds %q:\n%s", leak, content)
		}
	}
}

func TestOutcomeOfDrift(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		roots   []report.DriftRoot
		outcome string
		codes   int
	}{
		"none":         {nil, outcomeOK, 0},
		"all checked":  {[]report.DriftRoot{{Path: "a"}, {Path: "b"}}, outcomeOK, 0},
		"one failed":   {[]report.DriftRoot{{Path: "a"}, {Path: "b", Error: "x"}}, outcomePartial, 1},
		"all failed":   {[]report.DriftRoot{{Path: "a", Error: "x"}, {Path: "b", Error: "y"}}, outcomeFailed, 1},
		"the only one": {[]report.DriftRoot{{Path: "a", Error: "x"}}, outcomeFailed, 1},
	} {
		out := t.TempDir()
		d := &report.Drift{Roots: tc.roots}
		d.Finish()
		if err := d.WriteFiles(out); err != nil {
			t.Fatal(err)
		}
		doc := decodeRun(t, build(t, Options{Out: out, Document: DocRun}))
		codes, _ := doc["failure_codes"].([]any)
		if doc["outcome"] != tc.outcome || len(codes) != tc.codes {
			t.Errorf("%s: outcome %v, codes %v; want %s and %d", name, doc["outcome"], codes, tc.outcome, tc.codes)
		}
	}
}

func TestRunOfDriftTimesAndRequestID(t *testing.T) {
	t.Parallel()
	out := writeDrift(t)
	doc := decodeRun(t, build(t, Options{
		Out: out, Document: DocRun, RequestID: "req_01:abc.def-9",
		StartedAt: fixedNow().Add(-time.Minute), FinishedAt: fixedNow(),
	}))
	if doc["started_at"] != "2026-11-02T09:59:00Z" || doc["finished_at"] != "2026-11-02T10:00:00Z" || doc["request_id"] != "req_01:abc.def-9" {
		t.Errorf("run: %v", doc)
	}
}

// With the reports of both commands, the run is the import's, as before,
// unless --command says drift; with one, it is that one's.
func TestRunCommandIsRead(t *testing.T) {
	t.Parallel()
	both := writeOut(t)
	drift, err := os.ReadFile(filepath.Join(writeDrift(t), report.Dir, report.DriftJSONFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(both, report.Dir, report.DriftJSONFile), drift, 0o644); err != nil {
		t.Fatal(err)
	}
	only := writeDrift(t)
	for name, tc := range map[string]struct {
		out, command, want string
	}{
		"both, none given":    {both, "", CommandImport},
		"both, import":        {both, CommandImport, CommandImport},
		"both, drift":         {both, CommandDrift, CommandDrift},
		"drift alone":         {only, "", CommandDrift},
		"drift alone, drift":  {only, CommandDrift, CommandDrift},
		"import alone, drift": {writeOut(t), CommandDrift, ""},
		"drift alone, import": {only, CommandImport, ""},
	} {
		content, err := Build(Options{Out: tc.out, Document: DocRun, Command: tc.command, Now: fixedNow})
		if tc.want == "" {
			if err == nil {
				t.Errorf("%s: worked without the report of that command", name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got := decodeRun(t, content)["command"]; got != tc.want {
			t.Errorf("%s: command %v, want %s", name, got, tc.want)
		}
	}
	// An import's run is the same, with or without a drift report beside
	// it.
	if a, b := build(t, Options{Out: both, Document: DocRun}), build(t, Options{Out: writeOut(t), Document: DocRun}); string(a) != string(b) {
		t.Errorf("a drift report changed the import's run:\n%s\n%s", a, b)
	}
}

func TestOptionsAreChecked(t *testing.T) {
	t.Parallel()
	out := writeOut(t)
	for name, opts := range map[string]Options{
		"unknown command":             {Document: DocRun, Command: "verify"},
		"command of the inventory":    {Document: DocInventory, Command: CommandImport},
		"request ID with a space":     {Document: DocRun, RequestID: "a b"},
		"request ID with a slash":     {Document: DocRun, RequestID: "a/b"},
		"request ID, too long":        {Document: DocRun, RequestID: strings.Repeat("a", 129)},
		"request ID of the gate":      {Document: DocGate, RequestID: "abc"},
		"request ID of the graph":     {Document: DocGraph, RequestID: "abc"},
		"request ID, a new line":      {Document: DocRun, RequestID: "abc\n"},
		"request ID, non-ASCII":       {Document: DocRun, RequestID: "abc\u00e9"},
		"request ID of the inventory": {Document: DocInventory, RequestID: "abc"},
	} {
		opts.Out = out
		if _, err := Build(opts); err == nil {
			t.Errorf("%s: worked", name)
		}
	}
	for _, id := range []string{"a", strings.Repeat("a", 128), "Az09._:-", "01HZX:req.1"} {
		doc := decodeRun(t, build(t, Options{Out: out, Document: DocRun, RequestID: id}))
		if doc["request_id"] != id {
			t.Errorf("request_id %v, want %s", doc["request_id"], id)
		}
	}
}

// The run document's schema asks an import for what a drift run doesn't
// have, and for nothing else.
func TestRunSchemaPerCommand(t *testing.T) {
	t.Parallel()
	importRun := decodeRun(t, build(t, Options{Out: writeOut(t), Document: DocRun}))
	driftRun := decodeRun(t, build(t, Options{Out: writeDrift(t), Document: DocRun}))
	encode := func(doc map[string]any) []byte {
		content, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return content
	}
	if err := validate(DocRun, encode(importRun)); err != nil {
		t.Errorf("import: %v", err)
	}
	if err := validate(DocRun, encode(driftRun)); err != nil {
		t.Errorf("drift: %v", err)
	}
	for _, key := range []string{"exit_code", "tool", "engine", "coverage"} {
		doc := decodeRun(t, encode(importRun))
		delete(doc, key)
		if err := validate(DocRun, encode(doc)); err == nil {
			t.Errorf("an import without %s passed", key)
		}
	}
	for name, mutate := range map[string]func(doc map[string]any){
		"no command":       func(doc map[string]any) { delete(doc, "command") },
		"verify":           func(doc map[string]any) { doc["command"] = "verify" },
		"discover":         func(doc map[string]any) { doc["command"] = "discover" },
		"request ID space": func(doc map[string]any) { doc["request_id"] = "a b" },
		"request ID empty": func(doc map[string]any) { doc["request_id"] = "" },
		"failure code":     func(doc map[string]any) { doc["failure_codes"] = []string{"roots_failed"} },
		"extra field":      func(doc map[string]any) { doc["roots"] = 1 },
	} {
		doc := decodeRun(t, encode(driftRun))
		mutate(doc)
		if err := validate(DocRun, encode(doc)); err == nil {
			t.Errorf("%s: passed", name)
		}
	}
}
