// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package picker

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/selection"
)

const (
	euWest = "aws/123456789012/eu-west-2"
	global = "aws/123456789012/global"
)

func testFile() *selection.File {
	return &selection.File{Version: selection.Version, Defaults: selection.Defaults{Include: true}, Resources: []selection.Resource{
		{Type: "aws_s3_bucket", ID: "logs", Scope: euWest, Include: true},
		{Type: "aws_s3_bucket", ID: "assets", Scope: euWest, Include: true},
		{Type: "aws_vpc", ID: "vpc-default", Scope: euWest, Reason: "default VPC"},
		{Type: "aws_iam_role", ID: "ci", Scope: global, Include: true, New: true},
	}}
}

func labels(m *Model) []string {
	var out []string
	for _, r := range m.rows() {
		out = append(out, strings.Repeat(" ", r.depth)+r.label)
	}
	return out
}

func press(m *Model, keys ...string) {
	for _, k := range keys {
		text := ""
		if len(k) == 1 {
			text = k
		}
		m.key(k, text)
	}
}

func TestRows(t *testing.T) {
	m := New("selection.yaml", testFile())
	// Scopes start open, types closed.
	want := []string{euWest, " aws_s3_bucket", " aws_vpc", global, " aws_iam_role"}
	if got := labels(m); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("rows: got %q, want %q", got, want)
	}
	press(m, "down", "right")
	want = []string{euWest, " aws_s3_bucket", "  assets", "  logs", " aws_vpc", global, " aws_iam_role"}
	if got := labels(m); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("rows after opening the buckets: got %q, want %q", got, want)
	}
	// Left on a resource closes its group, and moves there.
	press(m, "down", "left")
	if m.cursor != 1 || len(m.rows()) != 5 {
		t.Errorf("cursor %d, rows %q", m.cursor, labels(m))
	}
}

func TestToggleGroup(t *testing.T) {
	f := testFile()
	m := New("selection.yaml", f)
	press(m, "down") // aws_s3_bucket, all included
	press(m, "space")
	if f.Resources[0].Include || f.Resources[1].Include || !m.changed {
		t.Errorf("space on an included group: want it excluded, got %+v", f.Resources[:2])
	}
	press(m, "space")
	if !f.Resources[0].Include || !f.Resources[1].Include {
		t.Errorf("space on an excluded group: want it included, got %+v", f.Resources[:2])
	}
	// A partly included group is included whole.
	f.Resources[0].Include = false
	press(m, "space")
	if !f.Resources[0].Include {
		t.Error("space on a partly included group: want it included")
	}
}

func TestDecidingReviews(t *testing.T) {
	f := testFile()
	m := New("selection.yaml", f)
	press(m, "u") // new only: the IAM role
	if got := labels(m); len(got) != 2 || got[1] != " aws_iam_role" {
		t.Fatalf("new only: %q", got)
	}
	press(m, "down", "right", "down", "space")
	if f.Resources[3].Include || f.Resources[3].New {
		t.Errorf("deciding on a new resource: want it excluded and reviewed, got %+v", f.Resources[3])
	}
}

func TestSearch(t *testing.T) {
	f := testFile()
	m := New("selection.yaml", f)
	press(m, "/", "l", "o", "g", "enter")
	if m.searching || m.search != "log" {
		t.Fatalf("searching %v for %q", m.searching, m.search)
	}
	// A search opens every group with a match.
	want := []string{euWest, " aws_s3_bucket", "  logs"}
	if got := labels(m); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("rows: got %q, want %q", got, want)
	}
	press(m, "n")
	if f.Resources[0].Include || !f.Resources[1].Include {
		t.Errorf("n excludes what is shown only: %+v", f.Resources[:2])
	}
	press(m, "esc")
	if m.search != "" || len(m.rows()) != 5 {
		t.Errorf("esc: search %q, rows %q", m.search, labels(m))
	}
}

func TestSummary(t *testing.T) {
	m := New("selection.yaml", testFile())
	got := m.summary()
	for _, want := range []string{"3 of 4 resources selected", "2 roots", "up to 2 calls of terraform-aws-modules/s3-bucket/aws", "1 new to review"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q misses %q", got, want)
		}
	}
	if !strings.Contains(m.render(), got) {
		t.Error("the view doesn't show the summary")
	}
}

func TestSaveAndQuit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.yaml")
	f := testFile()
	m := New(path, f)
	press(m, "down", "space") // exclude the buckets
	// Quitting with changes asks first.
	if quit := m.key("q", "q"); quit || !m.confirmQuit || !strings.Contains(m.render(), "Unsaved changes") {
		t.Fatalf("q with changes: quit %v, confirm %v", quit, m.confirmQuit)
	}
	if quit := m.key("s", "s"); !quit || !m.saved {
		t.Fatalf("s: quit %v, saved %v", quit, m.saved)
	}
	saved, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Has("aws_s3_bucket", "logs") && saved.Decide("aws_s3_bucket", "logs", "").Include {
		t.Error("the saved file still includes the bucket")
	}

	unchanged := New(path, testFile())
	if quit := unchanged.key("q", "q"); !quit || unchanged.saved {
		t.Errorf("q without changes: quit %v, saved %v", quit, unchanged.saved)
	}
}

func TestScroll(t *testing.T) {
	f := &selection.File{Version: selection.Version}
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		f.Resources = append(f.Resources, selection.Resource{Type: "aws_sqs_queue", ID: id, Include: true})
	}
	m := New("selection.yaml", f)
	m.height = 3
	press(m, "right", "end")
	if m.cursor != 6 || m.offset != 4 {
		t.Errorf("end: cursor %d, offset %d", m.cursor, m.offset)
	}
	press(m, "home")
	if m.cursor != 0 || m.offset != 0 {
		t.Errorf("home: cursor %d, offset %d", m.cursor, m.offset)
	}
}
