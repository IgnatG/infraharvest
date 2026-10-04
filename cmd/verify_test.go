// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/engine"
	"github.com/IgnatG/infraharvest/report"
)

func TestGeneratedRoots(t *testing.T) {
	out := t.TempDir()
	for _, file := range []string{
		"aws/111122223333/eu-west-2/" + engine.GeneratedFileName,
		"aws/111122223333/global/" + engine.GeneratedFileName,
		"aws/111122223333/eu-west-2/.terraform/modules/state/" + engine.GeneratedFileName,
		engine.ModulesDirName + "/s3_bucket_0123abcd/main.tf",
		"report/report.md",
	} {
		path := filepath.Join(out, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	roots, err := generatedRoots(out)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range roots {
		got = append(got, relativePath(out, r))
	}
	if want := []string{"aws/111122223333/eu-west-2", "aws/111122223333/global"}; !reflect.DeepEqual(got, want) {
		t.Errorf("roots: got %v, want %v", got, want)
	}
}

func TestVerifyOutputWithoutRoots(t *testing.T) {
	err := verifyOutput(context.Background(), t.TempDir(), engineTerraform, "", outputHCL, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "no generated roots") {
		t.Errorf("want an error, got %v", err)
	}
}

func TestWriteVerified(t *testing.T) {
	results := []verifiedRoot{
		{Path: "aws/1/eu-west-2", Checks: []report.Check{{Name: engine.CheckFormat, Passed: true}}},
		{Path: "aws/1/global", Checks: []report.Check{{Name: engine.CheckFormat, Passed: true}, {Name: engine.CheckPlan, Details: []string{"aws_iam_role.a: update"}}}},
	}
	var b bytes.Buffer
	if err := writeVerified(&b, results, outputHCL); err != nil {
		t.Fatal(err)
	}
	if want := "aws/1/eu-west-2: passed\naws/1/global: failed G3 plan\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

func TestPrintReport(t *testing.T) {
	out := t.TempDir()
	r := &report.Report{}
	r.Finish(map[string]int{"aws_vpc": 1}, nil, false)
	if err := r.WriteFiles(out); err != nil {
		t.Fatal(err)
	}

	var md, js bytes.Buffer
	if err := printReport(out, outputHCL, &md); err != nil {
		t.Fatal(err)
	}
	if err := printReport(out, outputJSON, &js); err != nil {
		t.Fatal(err)
	}
	if md.String() != r.Markdown() {
		t.Errorf("markdown differs:\n%s", md.String())
	}
	if !strings.Contains(js.String(), `"schema_version": 1`) {
		t.Errorf("json: %s", js.String())
	}
	if err := printReport(t.TempDir(), outputHCL, &md); err == nil {
		t.Error("want an error without a report")
	}
}
