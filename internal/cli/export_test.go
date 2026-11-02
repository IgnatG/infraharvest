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
