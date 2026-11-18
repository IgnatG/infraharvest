// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/IgnatG/infraharvest/schemas"
)

func sampleDrift() *Drift {
	d := &Drift{
		Roots: []DriftRoot{
			{Path: "aws/123/eu-west-2", Backend: "s3", Resources: 3, NotApplied: []string{"aws_s3_bucket.fresh"}, Placeholders: []string{"db_password"}},
			{Path: "aws/123/eu-west-1", Error: "terraform init: exit status 1\nError: access denied"},
		},
		Changes: []DriftChange{
			{Root: "aws/123/eu-west-2", Address: "aws_security_group.web", Type: "aws_security_group", Attribute: "description", Before: "a", After: "b", Kind: "changed", Decision: DriftKeep, Outcome: DriftAbsorbed},
			{Root: "aws/123/eu-west-2", Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "tags.Env", Before: "prod", After: "dev", Kind: "changed", Source: &DriftSource{File: "main.tf", Line: 3}, Decision: DriftKeep, Outcome: DriftKept},
			{Root: "aws/123/eu-west-2", Address: "aws_db_instance.db", Type: "aws_db_instance", Attribute: "password", Before: "(sensitive value)", After: "(sensitive value)", Kind: "changed", Decision: DriftKeep, Outcome: DriftNotAssessable, Reason: "a sensitive value whose variable has no value here"},
			{Root: "aws/123/eu-west-2", Address: "aws_sqs_queue.gone", Type: "aws_sqs_queue", Kind: DriftDeleted, Decision: DriftKeep, Outcome: DriftKept, Reason: "the next apply creates the resource again"},
			{Root: "aws/123/eu-west-2", Address: "aws_instance.web", Type: "aws_instance", Attribute: "subnet_id", Before: "subnet-a", After: "subnet-b", Kind: "changed", Source: &DriftSource{File: "main.tf", Line: 9}, Decision: DriftAccept, Outcome: DriftWritten, ForcesReplacement: true},
			{Root: "aws/123/eu-west-2", Address: "aws_instance.web", Type: "aws_instance", Attribute: "user_data", Before: "a", After: "b", Kind: "changed", Decision: DriftAccept, Outcome: DriftManual, Reason: "base64encode() computes it: edit it by hand"},
			{Root: "aws/123/eu-west-2", Address: "aws_ssm_parameter.token", Type: "aws_ssm_parameter", Attribute: "value", Before: "(sensitive value)", After: "(sensitive value)", Kind: "changed", Decision: DriftAccept, Outcome: DriftSecret, Reason: "a secret, which is never written into the code"},
			{Root: "aws/123/eu-west-2", Address: "aws_sqs_queue.jobs", Type: "aws_sqs_queue", Attribute: "delay_seconds", Before: 0, After: 30, Kind: "changed", Decision: DriftAccept, Outcome: DriftRejectedByPlan, Reason: "the plan of the edited code still changes delay_seconds"},
		},
	}
	d.Finish()
	return d
}

func TestDriftFinish(t *testing.T) {
	t.Parallel()
	d := sampleDrift()
	want := DriftTotals{Roots: 2, Failed: 1, Changes: 8, Written: 1, Kept: 2, RejectedByPlan: 1, Manual: 1, Secret: 1, NotAssessable: 1, Absorbed: 1}
	if d.Totals != want {
		t.Errorf("totals %+v, want %+v", d.Totals, want)
	}
	if !d.DriftFound || d.SchemaVersion != SchemaVersion {
		t.Errorf("drift found %v, schema version %d", d.DriftFound, d.SchemaVersion)
	}
	if d.Roots[0].Path != "aws/123/eu-west-1" || d.Changes[0].Address != "aws_db_instance.db" {
		t.Errorf("not sorted: %s, %s", d.Roots[0].Path, d.Changes[0].Address)
	}

	absorbed := &Drift{Roots: []DriftRoot{{Path: "a"}}, Changes: []DriftChange{{Root: "a", Outcome: DriftAbsorbed}, {Root: "a", Outcome: DriftWritten}}}
	absorbed.Finish()
	if absorbed.DriftFound {
		t.Error("changes absorbed or written count as drift found")
	}
}

func TestDriftJSON(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := sampleDrift().WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["schema_version"] != float64(1) || got["drift_found"] != true {
		t.Errorf("schema_version %v, drift_found %v", got["schema_version"], got["drift_found"])
	}
	changes, _ := got["changes"].([]any)
	if len(changes) != 8 {
		t.Fatalf("%d changes", len(changes))
	}
	first, _ := changes[0].(map[string]any)
	for _, key := range []string{"root", "address", "type", "attribute", "before", "after", "kind", "decision", "outcome", "reason"} {
		if _, ok := first[key]; !ok {
			t.Errorf("a change has no %s: %v", key, first)
		}
	}

	empty := &Drift{}
	empty.Finish()
	b.Reset()
	if err := empty.WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"changes": []`) || !strings.Contains(b.String(), `"roots": []`) {
		t.Errorf("an empty report doesn't list empty roots and changes:\n%s", b.String())
	}
}

func TestDriftMarkdown(t *testing.T) {
	t.Parallel()
	md := sampleDrift().Markdown()
	for _, want := range []string{
		"# Drift report",
		"| 2 | 1 | 8 | 1 | 2 | 2 | 2 | 1 |",
		"## Accepted into the code",
		"- `aws_instance.web` in `aws/123/eu-west-2`, `subnet_id`: `\"subnet-a\"` → `\"subnet-b\"` (`main.tf:9`); keeping the code instead would have replaced the resource on the next apply",
		"## Kept: the next apply reverts the cloud",
		"- `aws_s3_bucket.logs` in `aws/123/eu-west-2`, `tags.Env`: `\"prod\"` → `\"dev\"` (`main.tf:3`)",
		"- `aws_sqs_queue.gone` in `aws/123/eu-west-2`: deleted in the cloud; the next apply creates the resource again",
		"## Needs a hand edit",
		"`user_data`: `\"a\"` → `\"b\"`; base64encode() computes it: edit it by hand",
		"`delay_seconds`: `0` → `30`; the plan of the edited code still changes delay_seconds",
		"## Secrets and not assessable",
		"`value`: `\"(sensitive value)\"` → `\"(sensitive value)\"`; a secret, which is never written into the code",
		"`password`: `\"(sensitive value)\"` → `\"(sensitive value)\"`",
		"## Absorbed",
		"## Not checked\n\n- `aws/123/eu-west-1`: terraform init: exit status 1 Error: access denied",
		"## Not applied yet",
		"- `aws_s3_bucket.fresh` in `aws/123/eu-west-2`",
		"## Variables without a value",
		"- `aws/123/eu-west-2`: `db_password`",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("drift.md has no %q:\n%s", want, md)
		}
	}
	order := []string{"## Accepted into the code", "## Kept", "## Needs a hand edit", "## Secrets and not assessable", "## Absorbed"}
	for i := 1; i < len(order); i++ {
		if strings.Index(md, order[i-1]) > strings.Index(md, order[i]) {
			t.Errorf("%s comes after %s", order[i-1], order[i])
		}
	}
	if !strings.Contains(md, "written into the roots' files") {
		t.Error("drift.md says nothing was changed, with changes written")
	}

	none := &Drift{Roots: []DriftRoot{{Path: "a", Resources: 1}}}
	none.Finish()
	if !strings.Contains(none.Markdown(), "No drift") {
		t.Errorf("a report without drift doesn't say so:\n%s", none.Markdown())
	}
}

func TestDriftValueIsShortened(t *testing.T) {
	t.Parallel()
	got := driftValue(strings.Repeat("x", 200) + "`")
	if r := []rune(got); len(r) != maxDriftValue+2 || strings.Count(got, "`") != 2 {
		t.Errorf("got %s", got)
	}
}

func TestDriftWriteFiles(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	if err := sampleDrift().WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{DriftJSONFile, DriftMarkdownFile} {
		if _, err := os.Stat(filepath.Join(out, Dir, name)); err != nil {
			t.Error(err)
		}
	}
	for _, name := range []string{"report.md", "report.json"} {
		if _, err := os.Stat(filepath.Join(out, Dir, name)); err == nil {
			t.Errorf("drift wrote the import's %s", name)
		}
	}
}

// A change of tags several resources share is one change, which lists
// the resources it covers.
func TestDriftSharedTags(t *testing.T) {
	t.Parallel()
	d := &Drift{
		Roots: []DriftRoot{{Path: "aws/123/eu-west-2", Backend: "s3", Resources: 2}},
		Changes: []DriftChange{{
			Root: "aws/123/eu-west-2", Address: "local.tags", Attribute: "tags.Env", Before: "prod", After: "dev", Kind: "changed",
			Source: &DriftSource{File: "locals.tf", Line: 3}, Covers: []string{"aws_s3_bucket.logs", "aws_sqs_queue.jobs"},
			Decision: DriftAccept, Outcome: DriftWritten,
		}},
	}
	d.Finish()
	if d.Totals.Changes != 1 || d.Totals.Written != 1 {
		t.Errorf("totals %+v", d.Totals)
	}
	var b bytes.Buffer
	if err := d.WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Changes []struct {
			Address string   `json:"address"`
			Covers  []string `json:"covers"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Changes) != 1 || got.Changes[0].Address != "local.tags" || len(got.Changes[0].Covers) != 2 {
		t.Errorf("drift.json changes %+v", got.Changes)
	}
	md := d.Markdown()
	want := "- `local.tags` in `aws/123/eu-west-2`, `tags.Env`: `\"prod\"` → `\"dev\"`, on every resource that gets it from there: `aws_s3_bucket.logs`, `aws_sqs_queue.jobs` (`locals.tf:3`)"
	if !strings.Contains(md, want) || strings.Count(md, "tags.Env") != 1 {
		t.Errorf("drift.md:\n%s\nwant once:\n%s", md, want)
	}
}

// A change of a resource in a module call names the call, points at its
// argument, and says how the module reads it.
func TestDriftModuleCall(t *testing.T) {
	t.Parallel()
	d := &Drift{
		Roots: []DriftRoot{{Path: "aws/123/eu-west-2", Backend: "s3", Resources: 1}},
		Changes: []DriftChange{{
			Root: "aws/123/eu-west-2", Address: "module.logs.aws_s3_bucket.this", Type: "aws_s3_bucket", ModuleCall: "module.logs",
			Attribute: "tags.Env", Before: "prod", After: "dev", Kind: "changed",
			Source:   &DriftSource{File: "generated.tf", Line: 7, Expression: "{ Env = \"prod\" }", ModulePath: "../../../modules/s3_bucket_0a1b2c3d/main.tf:3 var.tags"},
			Decision: DriftAccept, Outcome: DriftWritten,
		}},
	}
	d.Finish()
	var b bytes.Buffer
	if err := d.WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Changes []struct {
			ModuleCall string `json:"module_call"`
			Source     struct {
				File       string `json:"file"`
				Line       int    `json:"line"`
				ModulePath string `json:"module_path"`
			} `json:"source"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Changes) != 1 || got.Changes[0].ModuleCall != "module.logs" || got.Changes[0].Source.File != "generated.tf" || got.Changes[0].Source.ModulePath != "../../../modules/s3_bucket_0a1b2c3d/main.tf:3 var.tags" {
		t.Errorf("drift.json changes %+v", got.Changes)
	}
	want := "(`generated.tf:7`, which the module reads at `../../../modules/s3_bucket_0a1b2c3d/main.tf:3 var.tags`)"
	if md := d.Markdown(); !strings.Contains(md, want) {
		t.Errorf("drift.md:\n%s\nwant:\n%s", md, want)
	}
}

// fullDrift has every field drift.json can hold, so that the schema is
// checked against all of them.
func fullDrift() *Drift {
	d := &Drift{
		Roots: []DriftRoot{
			{Path: "aws/123/eu-west-2", Environment: "dev", Backend: "s3", Resources: 3, NotApplied: []string{"aws_s3_bucket.fresh"}, Placeholders: []string{"db_password"}, Error: "terraform init failed"},
		},
		Changes: []DriftChange{
			{
				Root: "aws/123/eu-west-2", Environment: "dev", Address: "module.web.aws_instance.web", Type: "aws_instance", ModuleCall: "web",
				Attribute: "tags.Env", Before: map[string]any{"a": []any{1, "x", nil}}, After: false, Kind: "changed",
				Source:   &DriftSource{File: "main.tf", Line: 4, Expression: "var.tags", ModulePath: "../modules/web/main.tf:4 var.tags"},
				Decision: DriftAccept, DecisionSource: DriftDecidedByEntry, DecisionStale: true, Outcome: DriftManual, Reason: "edit it by hand", ForcesReplacement: true,
				LastChange: &DriftLastChange{By: "arn:aws:iam::1:user/a", Call: "ModifyInstanceAttribute", At: "2026-11-03T09:30:00Z", Log: "CloudTrail"},
			},
			{Root: "aws/123/eu-west-2", Address: "local.tags", Kind: "changed", Covers: []string{"aws_s3_bucket.a", "aws_s3_bucket.b"}, Decision: DriftKeep, Outcome: DriftKept},
		},
	}
	d.Finish()
	return d
}

// driftSchema returns the schema drift.json is written against.
func driftSchema(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	content, err := schemas.FS.ReadFile("drift-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(content, &s); err != nil {
		t.Fatal(err)
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func validateDriftJSON(t *testing.T, schema *jsonschema.Resolved, content []byte) error {
	t.Helper()
	var doc any
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatal(err)
	}
	return schema.Validate(doc)
}

func TestDriftJSONMatchesItsSchema(t *testing.T) {
	t.Parallel()
	schema := driftSchema(t)
	empty := &Drift{}
	empty.Finish()
	for name, d := range map[string]*Drift{"sample": sampleDrift(), "every field": fullDrift(), "empty": empty} {
		var b bytes.Buffer
		if err := d.WriteJSON(&b); err != nil {
			t.Fatal(err)
		}
		if err := validateDriftJSON(t, schema, b.Bytes()); err != nil {
			t.Errorf("%s: %v\n%s", name, err, b.String())
		}
		if !strings.Contains(b.String(), `"schema": "infraharvest/drift/v1"`) {
			t.Errorf("%s: no schema field:\n%s", name, b.String())
		}
	}
	// drift.json on disk is the same document.
	out := t.TempDir()
	if err := fullDrift().WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(out, Dir, DriftJSONFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDriftJSON(t, schema, content); err != nil {
		t.Errorf("drift.json: %v", err)
	}
}

func TestDriftSchemaIsClosed(t *testing.T) {
	t.Parallel()
	schema := driftSchema(t)
	var b bytes.Buffer
	if err := fullDrift().WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	// The changes are sorted by address: the second is the one with every
	// field.
	change := func(doc map[string]any) map[string]any { return doc["changes"].([]any)[1].(map[string]any) }
	for name, mutate := range map[string]func(doc map[string]any){
		"top-level field":  func(doc map[string]any) { doc["x"] = 1 },
		"no schema":        func(doc map[string]any) { delete(doc, "schema") },
		"other schema":     func(doc map[string]any) { doc["schema"] = "infraharvest/drift/v2" },
		"change field":     func(doc map[string]any) { change(doc)["x"] = 1 },
		"outcome":          func(doc map[string]any) { change(doc)["outcome"] = "fixed" },
		"root field":       func(doc map[string]any) { doc["roots"].([]any)[0].(map[string]any)["x"] = 1 },
		"totals field":     func(doc map[string]any) { doc["totals"].(map[string]any)["x"] = 1 },
		"source field":     func(doc map[string]any) { change(doc)["source"].(map[string]any)["x"] = 1 },
		"last change":      func(doc map[string]any) { delete(change(doc)["last_change"].(map[string]any), "by") },
		"no changes":       func(doc map[string]any) { delete(doc, "changes") },
		"negative count":   func(doc map[string]any) { doc["totals"].(map[string]any)["kept"] = -1 },
		"decision":         func(doc map[string]any) { change(doc)["decision"] = "maybe" },
		"decision_source":  func(doc map[string]any) { change(doc)["decision_source"] = "me" },
		"kind":             func(doc map[string]any) { change(doc)["kind"] = "moved" },
		"schema_version 2": func(doc map[string]any) { doc["schema_version"] = 2 },
	} {
		var doc map[string]any
		if err := json.Unmarshal(b.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		mutate(doc)
		content, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateDriftJSON(t, schema, content); err == nil {
			t.Errorf("%s: passed", name)
		}
	}
}
