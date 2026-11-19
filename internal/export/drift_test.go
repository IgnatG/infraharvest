// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
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
		change(root, "", "aws_s3_bucket.audit", "aws_s3_bucket", report.DriftKept),
		change(root, "", "aws_s3_bucket.audit", "aws_s3_bucket", report.DriftWritten),
		change(root, "", `aws_s3_bucket.named["team-blue"]`, "aws_s3_bucket", report.DriftManual),
		change(root, "", "module.db.aws_db_instance.main", "aws_db_instance", report.DriftSecret),
		change(root, "", "aws_sqs_queue.jobs", "aws_sqs_queue", report.DriftAbsorbed),
		change(root, "", "aws_sqs_queue.dead", "aws_sqs_queue", report.DriftNotAssessable),
		change(root, "", "aws_sqs_queue.late", "aws_sqs_queue", report.DriftRejectedByPlan),
		change(root, "", "aws_sqs_queue.later", "aws_sqs_queue", report.DriftRejectedByCheck),
		{
			Root: root, Address: "local.tags", Kind: "changed", Attribute: "tags.Env", Before: "a", After: "b", Decision: report.DriftKeep, Outcome: report.DriftKept,
			Covers: []string{"aws_s3_bucket.audit", "aws_sqs_queue.jobs"},
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
		"unknown command":             {Document: DocRun, Command: "apply"},
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
		"apply":            func(doc map[string]any) { doc["command"] = "apply" },
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

// writeOutWithDrift is writeOut, with the drift report of writeDrift beside
// the import's: the output of an import that was checked for drift.
func writeOutWithDrift(t *testing.T) string {
	t.Helper()
	out := writeOut(t)
	content, err := os.ReadFile(filepath.Join(writeDrift(t), report.Dir, report.DriftJSONFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, report.Dir, report.DriftJSONFile), content, 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

func decodeSummary(t *testing.T, content []byte) driftSummaryDoc {
	t.Helper()
	var doc driftSummaryDoc
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestDriftSummary(t *testing.T) {
	t.Parallel()
	out := writeDrift(t)
	content := build(t, Options{Out: out, Document: DocDriftSummary})
	doc := decodeSummary(t, content)
	if doc.Schema != schemaDriftSummary || doc.GeneratedAt != "2026-11-02T10:00:00Z" || !doc.DriftFound {
		t.Errorf("header: %+v", doc)
	}
	if want := (outcomeCounts{Written: 1, Kept: 4, RejectedByPlan: 1, RejectedByCheck: 1, Manual: 1, Secret: 1, NotAssessable: 1, Absorbed: 1}); doc.Totals.Outcomes != want {
		t.Errorf("outcomes %+v, want %+v", doc.Totals.Outcomes, want)
	}
	if doc.Totals.Roots != 4 || doc.Totals.RootsNotChecked != 1 || doc.Totals.Changes != 11 {
		t.Errorf("totals: %+v", doc.Totals)
	}
	if doc.Redaction.Addresses != addressesPlain || doc.Redaction.HashAlg != hashAlg {
		t.Errorf("redaction: %+v", doc.Redaction)
	}

	// The roots, sorted by path and then environment.
	var names []string
	for _, r := range doc.Roots {
		names = append(names, r.Root+" "+r.Environment)
	}
	want := []string{"aws/123456789012/ap-south-1 ", "aws/123456789012/eu-west-2 ", "aws/123456789012/us-east-1 dev", "aws/123456789012/us-east-1 prod"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("roots %q, want %q", names, want)
	}
	notChecked, london, dev := doc.Roots[0], doc.Roots[1], doc.Roots[2]
	if notChecked.Checked || notChecked.Changes != 0 || len(notChecked.Types) != 0 || len(notChecked.Addresses) != 0 {
		t.Errorf("the root that couldn't be checked: %+v", notChecked)
	}
	if !london.Checked || london.Changes != 9 || london.Outcomes.Kept != 2 || london.Outcomes.Written != 1 || london.Outcomes.Manual != 1 {
		t.Errorf("eu-west-2: %+v", london)
	}
	if dev.Changes != 1 || dev.Outcomes.Kept != 1 || len(dev.Addresses) != 1 || dev.Addresses[0] != "aws_vpc.main" {
		t.Errorf("dev: %+v", dev)
	}

	// By type, including the change of tags several resources share, which
	// has none.
	var types []string
	for _, ty := range london.Types {
		types = append(types, ty.Type+"="+strconv.Itoa(ty.Changes))
	}
	if got := strings.Join(types, ","); got != "aws_db_instance=1,aws_s3_bucket=3,aws_sqs_queue=4,shared_tags=1" {
		t.Errorf("types %s", got)
	}
	// The resources that changed, once each; the place that sets shared
	// tags isn't one.
	wantAddresses := []string{
		`aws_s3_bucket.audit`, `aws_s3_bucket.named["team-blue"]`, "aws_sqs_queue.dead", "aws_sqs_queue.jobs",
		"aws_sqs_queue.late", "aws_sqs_queue.later", "module.db.aws_db_instance.main",
	}
	if !slices.Equal(london.Addresses, wantAddresses) {
		t.Errorf("addresses %q, want %q", london.Addresses, wantAddresses)
	}

	// No value, reason, attribute or name of who changed it, nor the
	// message of the root that failed.
	for _, leak := range []string{driftSecretBefore, driftSecretAfter, "alice", "ModifyDBInstance", "CloudTrail", "password", "access denied", "terraform init", "local.tags", `"before"`, `"after"`} {
		if strings.Contains(string(content), leak) {
			t.Errorf("the drift summary holds %q:\n%s", leak, content)
		}
	}
}

func TestDriftSummaryHashAndDropAddresses(t *testing.T) {
	t.Parallel()
	out := writeDrift(t)
	keyA, keyB := []byte("0123456789abcdef-key-a"), []byte("0123456789abcdef-key-b")
	plain := decodeSummary(t, build(t, Options{Out: out, Document: DocDriftSummary}))
	a1 := decodeSummary(t, build(t, Options{Out: out, Document: DocDriftSummary, HashKey: keyA}))
	a2 := decodeSummary(t, build(t, Options{Out: out, Document: DocDriftSummary, HashKey: keyA}))
	b := decodeSummary(t, build(t, Options{Out: out, Document: DocDriftSummary, HashKey: keyB}))
	if a1.Redaction.Addresses != addressesHashed {
		t.Errorf("redaction: %+v", a1.Redaction)
	}
	london := func(d driftSummaryDoc) driftRootSummary { return d.Roots[1] }
	if len(london(a1).Addresses) != len(london(plain).Addresses) {
		t.Fatalf("hashed %d addresses, want %d", len(london(a1).Addresses), len(london(plain).Addresses))
	}
	for _, h := range london(a1).Addresses {
		if len(h) != 64 || strings.Contains(h, "aws_") || strings.Contains(h, "team-blue") {
			t.Errorf("address not hashed: %s", h)
		}
		if !slices.Contains(london(a2).Addresses, h) || slices.Contains(london(b).Addresses, h) {
			t.Errorf("the same key gave other hashes, or another key the same: %s", h)
		}
	}
	// Root paths are hashed with the addresses; environments, types and
	// counts stay readable.
	if london(plain).Root != "aws/123456789012/eu-west-2" {
		t.Errorf("plain root: %q", london(plain).Root)
	}
	if r := london(a1).Root; len(r) != 64 || r != london(a2).Root || r == london(b).Root {
		t.Errorf("root not hashed under the key: %q", r)
	}
	if len(london(a1).Types) != 4 || london(a1).Types[0].Type != "aws_db_instance" || a1.Totals != plain.Totals {
		t.Errorf("hashed: %+v", london(a1))
	}

	// Dropping wins over hashing.
	for _, key := range [][]byte{nil, keyA} {
		content := build(t, Options{Out: out, Document: DocDriftSummary, HashKey: key, DropAddresses: true})
		doc := decodeSummary(t, content)
		if doc.Redaction.Addresses != addressesDropped {
			t.Errorf("redaction: %+v", doc.Redaction)
		}
		for _, r := range doc.Roots {
			if r.Addresses != nil || r.Root != "" {
				t.Errorf("root %q has addresses: %q", r.Root, r.Addresses)
			}
		}
		for _, leak := range []string{`"addresses": [`, `"root"`, "aws/123456789012", "aws_sqs_queue.", "aws_s3_bucket.", "team-blue", "aws_vpc.main"} {
			if strings.Contains(string(content), leak) {
				t.Errorf("the dropped summary holds %q:\n%s", leak, content)
			}
		}
		if doc.Roots[1].Changes != 9 || len(doc.Roots[1].Types) != 4 {
			t.Errorf("dropping addresses changed the counts: %+v", doc.Roots[1])
		}
	}
}

func TestDriftSummaryIsStable(t *testing.T) {
	t.Parallel()
	out := writeDrift(t)
	finished := time.Date(2026, 11, 3, 9, 37, 0, 0, time.UTC)
	var first, second driftSummaryDoc
	for i, doc := range []*driftSummaryDoc{&first, &second} {
		opts := Options{Out: out, Document: DocDriftSummary, FinishedAt: finished}
		opts.Now = func() time.Time { return time.Date(2026, 11, 3, 12+i, 0, 0, 0, time.UTC) }
		content, err := Build(opts)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(content, doc); err != nil {
			t.Fatal(err)
		}
	}
	if first.GeneratedAt != "2026-11-03T09:37:00Z" || !reflect.DeepEqual(first, second) {
		t.Errorf("exporting again gave another document: %+v, %+v", first, second)
	}
	// It has the run ID of the drift run's document, and not the import's.
	run := decodeRun(t, build(t, Options{Out: out, Document: DocRun}))
	if run["run_id"] != first.RunID {
		t.Errorf("run_id %s, the drift run's is %v", first.RunID, run["run_id"])
	}
	both := writeOutWithDrift(t)
	if again := decodeSummary(t, build(t, Options{Out: both, Document: DocDriftSummary})); again.RunID != first.RunID {
		t.Errorf("the import's report beside it changed the run_id: %s, %s", again.RunID, first.RunID)
	}
}

func TestDriftSummaryNeedsDriftJSON(t *testing.T) {
	t.Parallel()
	if _, err := Build(Options{Out: writeOut(t), Document: DocDriftSummary}); err == nil {
		t.Error("a drift summary of an import was built")
	}
	// An older drift.json has no schema field; another one is refused.
	out := t.TempDir()
	for schema, ok := range map[string]bool{"": true, report.DriftSchema: true, "infraharvest/drift/v2": false} {
		content := `{"schema_version": 1, "drift_found": false, "roots": [], "changes": []}`
		if schema != "" {
			content = `{"schema": "` + schema + `", "schema_version": 1, "drift_found": false, "roots": [], "changes": []}`
		}
		if err := os.MkdirAll(filepath.Join(out, report.Dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, report.Dir, report.DriftJSONFile), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Build(Options{Out: out, Document: DocDriftSummary, Now: fixedNow})
		if (err == nil) != ok {
			t.Errorf("schema %q: %v", schema, err)
		}
	}
	// Nothing to count is a summary with no roots.
	empty := `{"schema_version": 1, "drift_found": false, "roots": [], "changes": []}`
	if err := os.WriteFile(filepath.Join(out, report.Dir, report.DriftJSONFile), []byte(empty), 0o644); err != nil {
		t.Fatal(err)
	}
	doc := decodeSummary(t, build(t, Options{Out: out, Document: DocDriftSummary}))
	if doc.DriftFound || doc.Roots == nil || len(doc.Roots) != 0 || doc.Totals.Changes != 0 {
		t.Errorf("empty drift: %+v", doc)
	}
	// An outcome it doesn't know isn't counted as another.
	bad := `{"schema_version": 1, "drift_found": true, "roots": [], "changes": [{"root": "r", "address": "a", "type": "t", "outcome": "fixed"}]}`
	if err := os.WriteFile(filepath.Join(out, report.Dir, report.DriftJSONFile), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(Options{Out: out, Document: DocDriftSummary, Now: fixedNow}); err == nil {
		t.Error("an unknown outcome was counted")
	}
}

// An address that looks like a credential, as a key of a resource can,
// stops the export, as an ID does; unless the addresses are left out.
func TestDriftSummaryRefusesCredentialAddress(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	d := &report.Drift{
		Roots:   []report.DriftRoot{{Path: "aws/1/eu-west-2"}},
		Changes: []report.DriftChange{{Root: "aws/1/eu-west-2", Address: credentialTag, Type: "aws_s3_bucket", Kind: "changed", Decision: report.DriftKeep, Outcome: report.DriftKept}},
	}
	d.Finish()
	if err := d.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(Options{Out: out, Document: DocDriftSummary, Now: fixedNow}); err == nil {
		t.Error("an address with a credential's format was written")
	}
	if _, err := Build(Options{Out: out, Document: DocDriftSummary, Now: fixedNow, HashKey: []byte("0123456789abcdef-key-a")}); err == nil {
		t.Error("an address with a credential's format was written, hashed")
	}
	content := build(t, Options{Out: out, Document: DocDriftSummary, DropAddresses: true})
	if strings.Contains(string(content), credentialTag) {
		t.Errorf("the credential reached the summary:\n%s", content)
	}
}

func TestDriftSummaryRefusesCredentialRootPath(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	root := "aws/" + credentialTag
	d := &report.Drift{
		Roots:   []report.DriftRoot{{Path: root}},
		Changes: []report.DriftChange{{Root: root, Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Kind: "changed", Decision: report.DriftKeep, Outcome: report.DriftKept}},
	}
	d.Finish()
	if err := d.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	for _, key := range [][]byte{nil, []byte("0123456789abcdef-key-a")} {
		if _, err := Build(Options{Out: out, Document: DocDriftSummary, Now: fixedNow, HashKey: key}); err == nil || !strings.Contains(err.Error(), "of a root") {
			t.Errorf("a root path with a credential's format was written, or refused for another reason: %v", err)
		}
	}
}

func TestDriftSummarySchemaIsClosed(t *testing.T) {
	t.Parallel()
	c, err := checkerOf(DocDriftSummary)
	if err != nil {
		t.Fatal(err)
	}
	if c.items["roots"] == nil {
		t.Fatalf("the roots aren't checked on their own: %v", c.items)
	}
	good := build(t, Options{Out: writeDrift(t), Document: DocDriftSummary})
	root := func(doc map[string]any) map[string]any { return doc["roots"].([]any)[1].(map[string]any) }
	for name, mutate := range map[string]func(doc map[string]any){
		"extra field":        func(doc map[string]any) { doc["x"] = 1 },
		"extra root field":   func(doc map[string]any) { root(doc)["attributes"] = map[string]any{} },
		"root without types": func(doc map[string]any) { delete(root(doc), "types") },
		"empty root":         func(doc map[string]any) { root(doc)["root"] = "" },
		"extra outcome":      func(doc map[string]any) { root(doc)["outcomes"].(map[string]any)["fixed"] = 1 },
		"missing outcome":    func(doc map[string]any) { delete(root(doc)["outcomes"].(map[string]any), "kept") },
		"negative count":     func(doc map[string]any) { root(doc)["changes"] = -1 },
		"extra type field":   func(doc map[string]any) { root(doc)["types"].([]any)[0].(map[string]any)["x"] = 1 },
		"duplicate address": func(doc map[string]any) {
			a := root(doc)["addresses"].([]any)
			root(doc)["addresses"] = append(a, a[0])
		},
		"empty address":       func(doc map[string]any) { root(doc)["addresses"] = []any{""} },
		"address not text":    func(doc map[string]any) { root(doc)["addresses"] = []any{1} },
		"other redaction":     func(doc map[string]any) { doc["redaction"].(map[string]any)["addresses"] = "some" },
		"no redaction":        func(doc map[string]any) { delete(doc, "redaction") },
		"extra total":         func(doc map[string]any) { doc["totals"].(map[string]any)["x"] = 1 },
		"other schema":        func(doc map[string]any) { doc["schema"] = "infraharvest/drift-summary/v2" },
		"roots null":          func(doc map[string]any) { doc["roots"] = nil },
		"no generated_at":     func(doc map[string]any) { delete(doc, "generated_at") },
		"run ID not a UUID":   func(doc map[string]any) { doc["run_id"] = "x" },
		"environment is null": func(doc map[string]any) { root(doc)["environment"] = nil },
	} {
		var doc map[string]any
		if err := json.Unmarshal(good, &doc); err != nil {
			t.Fatal(err)
		}
		mutate(doc)
		content, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		var instance any
		if err := json.Unmarshal(content, &instance); err != nil {
			t.Fatal(err)
		}
		whole, split := c.whole.Validate(instance), validate(DocDriftSummary, content)
		if (whole == nil) != (split == nil) {
			t.Errorf("%s: the whole schema says %v, the check %v", name, whole, split)
		}
		if split == nil {
			t.Errorf("%s: passed", name)
		}
	}
}

// The schemas of drift.json and of the decisions file are published beside
// the documents' but aren't documents of export.
func TestSchemaOfWhatIsNoDocument(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"drift", "drift-decisions"} {
		if _, err := Schema(name); err == nil {
			t.Errorf("the schema of %s was printed as a document's", name)
		}
	}
	if _, err := Schema(DocDriftSummary); err != nil {
		t.Error(err)
	}
}
