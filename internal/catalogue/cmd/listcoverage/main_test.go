// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseSchemas(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := parseSchemas(f)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Schema{
		"registry.terraform.io/hashicorp/aws": {
			Resources:     []string{"aws_iam_role", "aws_instance", "aws_vpc"},
			ListResources: []string{"aws_iam_role", "aws_vpc"},
		},
		"registry.terraform.io/hashicorp/google": {
			Resources:     []string{"google_compute_network"},
			ListResources: nil,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseSchemas() = %#v, want %#v", got, want)
	}
}

func TestParseSchemasRejectsBadJSON(t *testing.T) {
	if _, err := parseSchemas(strings.NewReader("{")); err == nil {
		t.Error("parseSchemas() of truncated JSON = nil error")
	}
}

func TestListedTypes(t *testing.T) {
	page := "# Services\n\nThe types `aws_prose` isn't a row.\n\n" +
		"| Service | Terraform types |\n|---|---|\n" +
		"| `api_gateway` | `aws_api_gateway_rest_api`, `aws_api_gateway_stage` |\n" +
		"| `cloud_asset` (opt-in) | whichever types the cloud's inventory returns |\n" +
		"| `vpc` | `aws_vpc` |\n"
	got := listedTypes([]byte(page))
	want := map[string]bool{"aws_api_gateway_rest_api": true, "aws_api_gateway_stage": true, "aws_vpc": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("listedTypes() = %v, want %v", got, want)
	}
}

func TestCompare(t *testing.T) {
	schema := Schema{Resources: []string{"aws_iam_role", "aws_instance", "aws_vpc", "aws_s3_bucket"}, ListResources: []string{"aws_iam_role", "aws_vpc"}}
	tests := []struct {
		name   string
		schema Schema
		listed map[string]bool
		want   Coverage
	}{
		{
			name:   "some listed",
			schema: schema,
			listed: map[string]bool{"aws_vpc": true, "aws_instance": true},
			want:   Coverage{Provider: "hashicorp/aws", Version: "6.0.0", Resources: 4, ListResources: 2, Unlisted: []string{"aws_iam_role"}},
		},
		{
			name:   "all listed",
			schema: schema,
			listed: map[string]bool{"aws_vpc": true, "aws_iam_role": true},
			want:   Coverage{Provider: "hashicorp/aws", Version: "6.0.0", Resources: 4, ListResources: 2, Unlisted: []string{}},
		},
		{
			name:   "no list resources",
			schema: Schema{Resources: []string{"aws_vpc"}},
			listed: map[string]bool{},
			want:   Coverage{Provider: "hashicorp/aws", Version: "6.0.0", Resources: 1, Unlisted: []string{}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compare("hashicorp/aws", "6.0.0", tt.schema, tt.listed)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("compare() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestWriteMarkdown(t *testing.T) {
	const header = "## Terraform list resources\n\n" +
		"The resource types `terraform query` can discover in the newest release of each provider, and those of them no infraharvest lister lists.\n\n" +
		"| Provider | Version | Resources | List resources | % | Not listed by infraharvest |\n|---|---|---:|---:|---:|---:|\n"
	tests := []struct {
		name   string
		report []Coverage
		want   string
	}{
		{
			name:   "every list resource listed",
			report: []Coverage{{Provider: "hashicorp/google", Version: "7.1.0", Resources: 3, ListResources: 1, Unlisted: []string{}}},
			want:   header + "| `hashicorp/google` | 7.1.0 | 3 | 1 | 33.3 | 0 |\n",
		},
		{
			name:   "no resources",
			report: []Coverage{{Provider: "hashicorp/google", Version: "7.1.0", Unlisted: []string{}}},
			want:   header + "| `hashicorp/google` | 7.1.0 | 0 | 0 | 0.0 | 0 |\n",
		},
		{
			name: "some not listed",
			report: []Coverage{
				{Provider: "hashicorp/aws", Version: "6.0.0", Resources: 4, ListResources: 2, Unlisted: []string{"aws_iam_role", "aws_s3_bucket"}},
				{Provider: "hashicorp/google", Version: "7.1.0", Resources: 3, ListResources: 1, Unlisted: []string{}},
			},
			want: header +
				"| `hashicorp/aws` | 6.0.0 | 4 | 2 | 50.0 | 2 |\n" +
				"| `hashicorp/google` | 7.1.0 | 3 | 1 | 33.3 | 0 |\n" +
				"\n<details><summary><code>hashicorp/aws</code> 6.0.0: 2 list resources not listed by infraharvest</summary>\n\n" +
				"- `aws_iam_role`\n- `aws_s3_bucket`\n\n</details>\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			if err := writeMarkdown(&b, tt.report); err != nil {
				t.Fatal(err)
			}
			if b.String() != tt.want {
				t.Errorf("writeMarkdown() =\n%s\nwant\n%s", b.String(), tt.want)
			}
		})
	}
}
