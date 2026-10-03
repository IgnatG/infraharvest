// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
)

func errorAt(line int, summary, detail string) tfjson.Diagnostic {
	return tfjson.Diagnostic{
		Severity: tfjson.DiagnosticSeverityError,
		Summary:  summary,
		Detail:   detail,
		Range:    &tfjson.Range{Filename: GeneratedFileName, Start: tfjson.Pos{Line: line}, End: tfjson.Pos{Line: line}},
	}
}

// Shaped like what Terraform generates for the AWS provider, with the
// validation errors it then reports.
const rejectedConfig = `resource "aws_subnet" "a" {
  availability_zone          = "us-east-1a"
  availability_zone_id       = "us-east-1-az1"
  cidr_block                 = "10.42.1.0/24"
  enable_lni_at_device_index = 0
  vpc_id                     = "vpc-1"
}

resource "aws_kinesis_stream" "b" {
  shard_count            = 1
  warm_throughput_mib_ps = 0
}

resource "aws_dynamodb_table" "c" {
  name = "items"
  point_in_time_recovery {
    enabled                 = false
    recovery_period_in_days = 0
  }
}

resource "aws_ssm_parameter" "d" {
  name  = "/app/env"
  type  = "String"
  value = null # sensitive
}
`

func TestRemoveRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), GeneratedFileName)
	if err := os.WriteFile(path, []byte(rejectedConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	conflict := "Conflicting configuration arguments"
	diags := []tfjson.Diagnostic{
		errorAt(2, conflict, `"availability_zone": conflicts with availability_zone_id`),
		errorAt(3, conflict, `"availability_zone_id": conflicts with availability_zone`),
		errorAt(5, "enable_lni_at_device_index must not be zero, got 0", ""),
		errorAt(10, conflict, `"shard_count": conflicts with warm_throughput_mib_ps`),
		errorAt(11, conflict, `"warm_throughput_mib_ps": conflicts with shard_count`),
		errorAt(18, "expected point_in_time_recovery.0.recovery_period_in_days to be in the range (1 - 35), got 0", ""),
		errorAt(25, "Invalid combination of arguments", `"value": one of insecure_value,value,value_wo must be specified`),
		{Severity: tfjson.DiagnosticSeverityWarning, Summary: "ignored", Range: &tfjson.Range{Filename: GeneratedFileName, Start: tfjson.Pos{Line: 4}}},
		{Severity: tfjson.DiagnosticSeverityError, Summary: "ignored", Range: &tfjson.Range{Filename: ImportsFileName, Start: tfjson.Pos{Line: 6}}},
	}

	changed, err := rewrite(path, func(f *hclwrite.File, syntax *hclsyntax.Body) bool {
		return removeRejected(f, syntax, GeneratedFileName, diags)
	})
	if err != nil || !changed {
		t.Fatalf("want a change, got changed=%v err=%v", changed, err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"availability_zone_id", "enable_lni_at_device_index", "warm_throughput_mib_ps", "recovery_period_in_days"} {
		if strings.Contains(string(got), removed) {
			t.Errorf("%s not removed:\n%s", removed, got)
		}
	}
	for _, kept := range []string{`availability_zone = "us-east-1a"`, "cidr_block", "shard_count = 1", "enabled = false", "value = null # sensitive"} {
		if !strings.Contains(string(got), kept) {
			t.Errorf("%s not kept:\n%s", kept, got)
		}
	}
}

func TestRemoveRejectedLeavesOtherErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), GeneratedFileName)
	if err := os.WriteFile(path, []byte(rejectedConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	diags := []tfjson.Diagnostic{
		errorAt(22, "Invalid combination of arguments", ""), // the block header
		errorAt(4, "invalid CIDR block", ""),                // a non-zero value
	}

	changed, err := rewrite(path, func(f *hclwrite.File, syntax *hclsyntax.Body) bool {
		return removeRejected(f, syntax, GeneratedFileName, diags)
	})

	if err != nil || changed {
		t.Errorf("want no change, got changed=%v err=%v", changed, err)
	}
}
