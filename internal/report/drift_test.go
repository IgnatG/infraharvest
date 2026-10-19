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
	for _, name := range []string{"report.md", "coverage.json"} {
		if _, err := os.Stat(filepath.Join(out, Dir, name)); err == nil {
			t.Errorf("drift wrote the import's %s", name)
		}
	}
}
