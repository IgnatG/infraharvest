// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"strings"
	"testing"
)

const bucketGenerated = `resource "aws_s3_bucket" "artifacts" {
  bucket = "artifacts"
  policy = "{}"
  versioning {
    enabled = true
  }
  website {
  }
}

resource "aws_s3_bucket_versioning" "artifacts" {
  bucket = "artifacts"
  versioning_configuration {
    status = "Enabled"
  }
}
`

func TestGenerateOmitsArguments(t *testing.T) {
	dir := t.TempDir()
	tf := &fakeTerraform{dir: dir, generated: bucketGenerated}
	imports := []Import{{Type: "aws_s3_bucket", Name: "artifacts", ID: "artifacts"}, {Type: "aws_s3_bucket_versioning", Name: "artifacts", ID: "artifacts"}}

	_, err := Generate(context.Background(), tf, dir, imports, Options{Omit: map[string][]string{"aws_s3_bucket": {"policy", "versioning", "website", "logging"}}})
	if err != nil {
		t.Fatal(err)
	}

	got := readFile(t, dir, GeneratedFileName)
	for _, removed := range []string{"policy", "versioning {", "website"} {
		if strings.Contains(got, removed) {
			t.Errorf("%q not left out:\n%s", removed, got)
		}
	}
	// Only the bucket's arguments: the versioning resource keeps its own.
	for _, kept := range []string{`bucket = "artifacts"`, `resource "aws_s3_bucket_versioning" "artifacts"`, "versioning_configuration {"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q missing:\n%s", kept, got)
		}
	}
	// Leaving arguments out moves lines, so the errors are planned for again.
	if strings.Join(tf.calls, ",") != "init,plan,plan,plan,show,fmt,validate,plan,show" {
		t.Errorf("calls: got %v, want [init plan plan plan show fmt validate plan show]", tf.calls)
	}
}

func TestOmitArgumentsWithoutMatches(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, GeneratedFileName, bucketGenerated).path

	changed, err := omitArguments(path, map[string][]string{"aws_sqs_queue": {"policy"}})

	if err != nil || changed {
		t.Errorf("want no change, got changed=%v err=%v", changed, err)
	}
}
