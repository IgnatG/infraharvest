// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package selection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "selection.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const edited = `version: 1
defaults:
  include: true
rules:
  - exclude: { type: aws_cloudwatch_log_group, id: "/aws/lambda/*" }
  - include: { type: [aws_cloudwatch_log_group], name: "*keep*" }
resources:
  - type: aws_vpc
    id: vpc-default
    include: false
    reason: default VPC, which AWS creates in every region
  - type: aws_vpc
    id: vpc-0abc1234
    include: true
  - type: aws_s3_bucket
    id: old-archive
    include: false
    note: legacy bucket, to be deleted
`

func TestDecide(t *testing.T) {
	f, err := Load(write(t, edited))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		resourceType, id, name string
		want                   Decision
	}{
		{"aws_vpc", "vpc-0abc1234", "", Decision{Include: true}},
		{"aws_vpc", "vpc-default", "", Decision{Reason: "default VPC, which AWS creates in every region"}},
		{"aws_s3_bucket", "old-archive", "", Decision{Reason: "excluded in the selection file"}},
		// Not listed: rules, then defaults.
		{"aws_cloudwatch_log_group", "/aws/lambda/fn", "fn", Decision{Reason: "excluded by a rule in the selection file"}},
		{"aws_cloudwatch_log_group", "/aws/lambda/keep-me", "keep-me", Decision{Include: true}},
		{"aws_sqs_queue", "https://sqs/1/new", "new", Decision{Include: true}},
	} {
		if got := f.Decide(tc.resourceType, tc.id, tc.name); got != tc.want {
			t.Errorf("%s %s: got %+v, want %+v", tc.resourceType, tc.id, got, tc.want)
		}
	}
}

func TestDefaultsExclude(t *testing.T) {
	f, err := Load(write(t, "version: 1\ndefaults:\n  include: false\nresources: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Decide("aws_vpc", "vpc-new", ""); got.Include || got.Reason == "" {
		t.Errorf("got %+v, want excluded with a reason", got)
	}
}

func TestLoadRejectsMistakes(t *testing.T) {
	for name, content := range map[string]string{
		"unknown field":     "version: 1\ndefaults:\n  include: true\nresources:\n  - type: aws_vpc\n    id: a\n    includ: true\n",
		"other version":     "version: 2\nresources: []\n",
		"rule without verb": "version: 1\nrules:\n  - {}\nresources: []\n",
		"tag match":         "version: 1\nrules:\n  - exclude: { tag: { ManagedBy: cloudformation } }\nresources: []\n",
	} {
		if _, err := Load(write(t, content)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestSaveRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.yaml")
	f := &File{Version: Version, Defaults: Defaults{Include: true}, Resources: []Resource{
		{Type: "aws_vpc", ID: "vpc-b", Include: true},
		{Type: "aws_vpc", ID: "vpc-a", Name: "default", Include: false, Reason: "default VPC"},
		{Type: "aws_iam_role", ID: "app", Include: true},
	}}

	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), "# infraharvest selection file") {
		t.Errorf("no header:\n%s", content)
	}
	if strings.Index(string(content), "aws_iam_role") > strings.Index(string(content), "vpc-a") || strings.Index(string(content), "vpc-a") > strings.Index(string(content), "vpc-b") {
		t.Errorf("resources not sorted:\n%s", content)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Decide("aws_vpc", "vpc-a", ""); got.Include || got.Reason != "default VPC" {
		t.Errorf("round trip lost the decision: %+v", got)
	}
}
