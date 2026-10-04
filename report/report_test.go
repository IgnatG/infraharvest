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

func sample() *Report {
	return &Report{
		Manifest: Manifest{
			Tool:     Component{Name: "infraharvest", Version: "v0.1.0"},
			Engine:   Component{Name: "terraform", Version: "1.16.5"},
			Provider: Provider{Source: "registry.terraform.io/hashicorp/aws", Constraint: "~> 6.67", Version: "6.67.0"},
		},
		Coverage: Coverage{
			Directories: []Directory{
				{Path: "aws/us-east-1", Imported: []Resource{
					{Address: "aws_sqs_queue.jobs", ID: "https://sqs/1/jobs"},
					{Address: "aws_ssm_parameter.token", ID: "/app/token"},
				}, LeftOut: []LeftOut{
					{Address: "aws_lb.web", ID: "arn:lb", Errors: []string{"Invalid combination of arguments"}},
				}, Secrets: []Secret{
					{Variable: "aws_ssm_parameter_token_value", Address: "aws_ssm_parameter.token", Attribute: "value"},
				}},
				{Path: "aws", Error: "terraform init: no network"},
			},
			Skipped: []Skipped{{Type: "aws_main_route_table_association", Count: 1, Reason: "Terraform can't import this resource type"}},
		},
	}
}

func TestFinish(t *testing.T) {
	r := sample()
	discovered := map[string]int{"aws_sqs_queue": 1, "aws_ssm_parameter": 1, "aws_lb": 1, "aws_iam_role": 2, "aws_main_route_table_association": 1}

	r.Finish(discovered, map[string]int{"aws_iam_role": 2}, false)

	if r.Directories[0].Path != "aws" {
		t.Errorf("directories not sorted: %+v", r.Directories)
	}
	want := CoverageTotal{Discovered: 6, Imported: 2, LeftOut: 1, Skipped: 1, Failed: 2}
	if r.Totals != want {
		t.Errorf("totals: got %+v, want %+v", r.Totals, want)
	}
	if r.Types[0].Type != "aws_iam_role" || r.Types[0].Failed != 2 {
		t.Errorf("types not sorted or counted: %+v", r.Types)
	}
	if r.ExitCode != ExitIncomplete || r.SchemaVersion != SchemaVersion {
		t.Errorf("exit code %d, schema %d", r.ExitCode, r.SchemaVersion)
	}

	r.Finish(discovered, map[string]int{"aws_iam_role": 2}, true)
	if r.ExitCode != ExitPartial {
		t.Errorf("with --allow-partial: exit code %d, want %d", r.ExitCode, ExitPartial)
	}
}

// Types infraharvest doesn't import don't make an import incomplete.
func TestFinishComplete(t *testing.T) {
	r := &Report{Coverage: Coverage{
		Directories: []Directory{{Path: "aws", Imported: []Resource{{Address: "aws_vpc.main", ID: "vpc-1"}}}},
		Skipped:     []Skipped{{Type: "aws_main_route_table_association", Count: 1}},
	}}

	r.Finish(map[string]int{"aws_vpc": 1, "aws_main_route_table_association": 1}, nil, false)

	if r.ExitCode != ExitOK || r.Incomplete() {
		t.Errorf("want a complete import, got exit code %d", r.ExitCode)
	}
}

func TestWriteFilesIsDeterministic(t *testing.T) {
	read := func() map[string]string {
		out := t.TempDir()
		r := sample()
		r.Finish(map[string]int{"aws_sqs_queue": 1}, nil, false)
		if err := r.WriteFiles(out); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{}
		for _, name := range []string{"coverage.json", "manifest.json", "report.md"} {
			content, err := os.ReadFile(filepath.Join(out, Dir, name))
			if err != nil {
				t.Fatal(err)
			}
			files[name] = string(content)
		}
		return files
	}

	first, second := read(), read()
	for name, content := range first {
		if second[name] != content {
			t.Errorf("%s differs between runs", name)
		}
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(first["manifest.json"]), &manifest); err != nil {
		t.Fatal(err)
	}
	if _, ok := manifest["directories"]; ok {
		t.Error("manifest.json has coverage fields")
	}
	if !strings.Contains(first["coverage.json"], `"exit_code": 1`) {
		t.Errorf("coverage.json has no exit code:\n%s", first["coverage.json"])
	}
}

func TestWriteJSON(t *testing.T) {
	r := sample()
	r.Finish(map[string]int{"aws_sqs_queue": 1}, nil, true)
	var out bytes.Buffer

	if err := r.WriteJSON(&out); err != nil {
		t.Fatal(err)
	}

	var got struct {
		SchemaVersion int `json:"schema_version"`
		Engine        struct {
			Name string `json:"name"`
		} `json:"engine"`
		Directories []struct {
			LeftOut []struct {
				Address string `json:"address"`
			} `json:"left_out"`
		} `json:"directories"`
		ExitCode int `json:"exit_code"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, out.String())
	}
	if got.SchemaVersion != SchemaVersion || got.Engine.Name != "terraform" || got.ExitCode != ExitPartial ||
		len(got.Directories) != 2 || got.Directories[1].LeftOut[0].Address != "aws_lb.web" {
		t.Errorf("unexpected report:\n%s", out.String())
	}
}

func TestMarkdown(t *testing.T) {
	r := sample()
	r.Finish(map[string]int{"aws_sqs_queue": 1, "aws_ssm_parameter": 1, "aws_lb": 1}, nil, false)

	got := r.Markdown()

	for _, want := range []string{
		"infraharvest v0.1.0, terraform 1.16.5, provider `registry.terraform.io/hashicorp/aws` ~> 6.67 (6.67.0).",
		"| 3 | 2 | 0 | 1 | 1 | 0 |",
		"| `aws_lb` | 1 | 0 | 0 | 1 | 0 | 0 |",
		"- `aws_lb.web` in `aws/us-east-1`: Invalid combination of arguments",
		"- `aws_ssm_parameter_token_value` in `aws/us-east-1`: value of `aws_ssm_parameter.token`",
		"- `aws_main_route_table_association` (1): Terraform can't import this resource type",
		"- `aws`: terraform init: no network",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
