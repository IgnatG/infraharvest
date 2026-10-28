// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// findingsRoot is the root the scanners' reports below are of: a bucket
// and a security group, and a module call of a local module with a
// bucket of its own, which the import blocks import.
var findingsRoot = map[string]string{
	"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "acme-logs"
  tags   = { a = "${var.unused}" }
}

resource "aws_security_group" "web" {
  name        = "web"
  description = "web"

  ingress {
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

module "data" {
  source = "./modules/bucket"
  name   = "acme-data"
}
`,
	"imports.tf": `import {
  to = aws_s3_bucket.logs
  id = "acme-logs"
}

import {
  to = module.data.aws_s3_bucket.this
  id = "acme-data"
}
`,
	"variables.tf": `variable "unused" {
  type = string
}
`,
	"modules/bucket/main.tf": `variable "name" {
  type = string
}

resource "aws_s3_bucket" "this" {
  bucket = var.name
}
`,
}

func writeFindingsRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range findingsRoot {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Each scanner's finding is about the resource the scanner names, or else
// the block at the place it gives: the reports are as tflint 0.59.1,
// trivy 0.76.1, checkov 3 and conftest 0.71.1 write them for
// findingsRoot, trimmed to the fields read and a few findings.
func TestFindingResources(t *testing.T) {
	t.Parallel()
	dir := writeFindingsRoot(t)
	for name, tc := range map[string]struct {
		parse func([]byte) ([]Finding, error)
		out   string
		want  []string
	}{
		// tflint gives a range only: the resource block it is in.
		"tflint": {parseTflint, `{"issues":[{"rule":{"name":"terraform_deprecated_interpolation","severity":"warning","link":"https://github.com/terraform-linters/tflint-ruleset-terraform/blob/v0.13.0/docs/rules/terraform_deprecated_interpolation.md"},"message":"Interpolation-only expressions are deprecated in Terraform v0.12.14","range":{"filename":"main.tf","start":{"line":3,"column":18},"end":{"line":3,"column":33}},"callers":[],"fixable":true,"fixed":false},` +
			// One in an import block is about what it imports, one in a
			// module call about the call, and one in a variable about none.
			`{"rule":{"name":"r","severity":"warning"},"message":"m","range":{"filename":"imports.tf","start":{"line":7,"column":3}}},` +
			`{"rule":{"name":"r","severity":"warning"},"message":"m","range":{"filename":"main.tf","start":{"line":19,"column":3}}},` +
			`{"rule":{"name":"terraform_typed_variables","severity":"warning"},"message":"no type","range":{"filename":"variables.tf","start":{"line":1,"column":1}}}],"errors":[]}`,
			[]string{"aws_s3_bucket.logs", "module.data.aws_s3_bucket.this", "module.data", ""}},
		// trivy names the resource, but in a module call only the call,
		// at the line of the module's file.
		"trivy": {parseTrivy, `{"Results":[{"Target":".","Class":"config","Type":"terraform"},{"Target":"main.tf","Class":"config","Type":"terraform","Misconfigurations":[` +
			`{"Type":"Terraform Security Check","ID":"AWS-0089","Title":"S3 Bucket Logging","Severity":"LOW","Status":"FAIL","CauseMetadata":{"Resource":"aws_s3_bucket.logs","Provider":"AWS","Service":"s3","StartLine":1,"EndLine":4}},` +
			`{"Type":"Terraform Security Check","ID":"AWS-0107","Title":"Security groups should not allow unrestricted ingress to SSH or RDP from any IP address.","Severity":"HIGH","Status":"FAIL","CauseMetadata":{"Resource":"aws_security_group.web","Provider":"AWS","Service":"ec2","StartLine":14,"EndLine":14,"Occurrences":[{"Resource":"ingress","Filename":"main.tf","Location":{"StartLine":10,"EndLine":15}},{"Resource":"aws_security_group.web","Filename":"main.tf","Location":{"StartLine":6,"EndLine":16}}]}}]},` +
			`{"Target":"modules/bucket/main.tf","Class":"config","Type":"terraform","Misconfigurations":[` +
			`{"Type":"Terraform Security Check","ID":"AWS-0089","Title":"S3 Bucket Logging","Severity":"LOW","Status":"FAIL","CauseMetadata":{"Resource":"module.data","Provider":"AWS","Service":"s3","StartLine":5,"EndLine":7,"Occurrences":[{"Resource":"module.data","Filename":"main.tf","Location":{"StartLine":18,"EndLine":21}}]}}]}]}`,
			[]string{"aws_s3_bucket.logs", "aws_security_group.web", "module.data.aws_s3_bucket.this"}},
		// checkov names the resource, in module calls too.
		"checkov": {parseCheckov, `[{"check_type":"terraform","results":{"failed_checks":[` +
			`{"check_id":"CKV_AWS_24","check_name":"Ensure no security groups allow ingress from 0.0.0.0:0 to port 22","file_path":"/main.tf","file_line_range":[6,16],"resource":"aws_security_group.web","severity":null},` +
			`{"check_id":"CKV_AWS_18","check_name":"Ensure the S3 bucket has access logging enabled","file_path":"/main.tf","file_line_range":[1,4],"resource":"aws_s3_bucket.logs","severity":null},` +
			`{"check_id":"CKV_AWS_18","check_name":"Ensure the S3 bucket has access logging enabled","file_path":"/modules/bucket/main.tf","file_line_range":[5,7],"resource":"module.data.aws_s3_bucket.this","severity":null}]}},` +
			// Its secrets scan names no resource, but the place it gives
			// may be in one.
			`{"check_type":"secrets","results":{"failed_checks":[{"check_id":"CKV_SECRET_6","check_name":"Base64 High Entropy String","file_path":"/main.tf","file_line_range":[2,3],"resource":"b0a9d1e4c3f5a6e7d8c9b0a1f2e3d4c5b6a7f8e9","severity":null}]}}]`,
			[]string{"aws_security_group.web", "aws_s3_bucket.logs", "module.data.aws_s3_bucket.this", "aws_s3_bucket.logs"}},
		// conftest gives the file only: a policy names the resource in
		// its result object, else at the start of its message.
		"conftest": {parseConftest, `[{"filename":"main.tf","namespace":"main","successes":0,` +
			`"warnings":[{"msg":"aws_s3_bucket.logs: check versioning","metadata":{"query":"data.main.warn"}},{"msg":"every bucket needs an owner","metadata":{"query":"data.main.warn"}}],` +
			`"failures":[{"msg":"logs has no owner tag","metadata":{"query":"data.main.deny","resource":"aws_s3_bucket.logs"}}]}]`,
			[]string{"aws_s3_bucket.logs", "aws_s3_bucket.logs", ""}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			findings, err := tc.parse([]byte(tc.out))
			if err != nil {
				t.Fatal(err)
			}
			locateFindings(dir, findings)
			var got []string
			for _, f := range findings {
				got = append(got, f.Resource)
				if f.at != (position{}) {
					t.Errorf("%+v keeps its position", f)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A place outside the directory, or in a file that doesn't parse, is in
// no block.
func TestLocateFindingsOutsideTheRoot(t *testing.T) {
	t.Parallel()
	dir := writeFindingsRoot(t)
	if err := os.WriteFile(filepath.Join(dir, "broken.tf"), []byte("resource \"a_b\" \"c\" {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings := []Finding{
		{at: position{file: "../main.tf", line: 1}},
		{at: position{file: "/etc/main.tf", line: 1}},
		{at: position{file: "broken.tf", line: 1}},
		{at: position{file: "missing.tf", line: 1}},
		{at: position{file: "main.tf"}},
	}
	locateFindings(dir, findings)
	for _, f := range findings {
		if f.Resource != "" {
			t.Errorf("got %q, want none", f.Resource)
		}
	}
}

func TestMessageAddress(t *testing.T) {
	t.Parallel()
	for message, want := range map[string]string{
		"aws_s3_bucket.logs has no owner tag":                      "aws_s3_bucket.logs",
		"aws_s3_bucket.logs: no versioning":                        "aws_s3_bucket.logs",
		"aws_s3_bucket.logs":                                       "aws_s3_bucket.logs",
		`module.logs["a"].aws_s3_bucket.this[0] is public`:         `module.logs["a"].aws_s3_bucket.this[0]`,
		"data.aws_iam_policy_document.p allows *":                  "data.aws_iam_policy_document.p",
		"every bucket needs an owner":                              "",
		"logs.bucket has no owner":                                 "",
		"aws_s3_bucket.logs.tags has no owner":                     "",
		"Bucket aws_s3_bucket.logs has no owner, see e.g. the doc": "",
	} {
		if got := messageAddress(message); got != want {
			t.Errorf("%q: got %q, want %q", message, got, want)
		}
	}
}

// A finding is about the resource at its address, or the only one at it
// but for instance keys, or the only one in the module call it names.
func TestResourceAt(t *testing.T) {
	t.Parallel()
	managed := []Managed{
		{Type: "aws_s3_bucket", ID: "acme-logs", Address: "aws_s3_bucket.logs"},
		{Type: "aws_s3_bucket", ID: "acme-data", Address: "module.data.aws_s3_bucket.this[0]"},
		{Type: "aws_iam_role", ID: "a", Address: `module.roles["a"].aws_iam_role.this`},
		{Type: "aws_iam_role", ID: "b", Address: `module.roles["b"].aws_iam_role.this`},
	}
	for address, want := range map[string]string{
		"aws_s3_bucket.logs":                  "acme-logs",
		"module.data.aws_s3_bucket.this[0]":   "acme-data",
		"module.data.aws_s3_bucket.this":      "acme-data",
		"module.data":                         "acme-data",
		`module.roles["a"].aws_iam_role.this`: "a",
		"module.roles.aws_iam_role.this":      "",
		"module.roles":                        "",
		"aws_s3_bucket.other":                 "",
		"":                                    "",
	} {
		m, ok := ResourceAt(managed, address)
		if ok != (want != "") || m.ID != want {
			t.Errorf("%q: got %+v, %v, want %q", address, m, ok, want)
		}
	}
}
