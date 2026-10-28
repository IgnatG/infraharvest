// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// findingsReport is a report of a root whose scanners found something
// about a bucket, the bucket of a module call, a module call of two
// roles, and the provider.
func findingsReport() *Report {
	scope := "aws/1/eu-west-2"
	r := &Report{}
	r.AddListed(Listed{Scope: scope, Service: "s3", Type: "aws_s3_bucket", ID: "acme-logs"})
	r.AddListed(Listed{Scope: scope, Service: "s3", Type: "aws_s3_bucket", ID: "acme-data"})
	r.AddListed(Listed{Scope: scope, Service: "iam", Type: "aws_iam_role", ID: "a"})
	r.AddDirectory("aws/1/eu-west-2", scope, &engine.Result{
		Imported: []engine.Import{{Type: "aws_s3_bucket", Name: "logs", ID: "acme-logs"}, {Type: "aws_s3_bucket", Name: "data", ID: "acme-data"}},
		Addresses: []engine.Managed{
			{Type: "aws_s3_bucket", ID: "acme-logs", Address: "aws_s3_bucket.logs"},
			{Type: "aws_s3_bucket", ID: "acme-data", Address: "module.data.aws_s3_bucket.this[0]"},
			{Type: "aws_iam_role", ID: "a", Address: `module.roles["a"].aws_iam_role.this`},
			{Type: "aws_iam_role", ID: "b", Address: `module.roles["b"].aws_iam_role.this`},
		},
		Gate: engine.Gate{
			{Name: engine.CheckFormat, Passed: true},
			{Name: engine.CheckScanners, Passed: true, Details: []string{"trivy: 4 findings"}, Findings: []engine.Finding{
				{Scanner: "trivy", Rule: "AWS-0089", Severity: "LOW", Location: "main.tf", Message: "S3 Bucket Logging", Resource: "aws_s3_bucket.logs"},
				{Scanner: "conftest", Rule: "s3", Severity: "failure", Location: "main.tf", Message: "logs has no owner tag", Resource: "aws_s3_bucket.logs"},
				{Scanner: "checkov", Rule: "CKV_AWS_18", Location: "modules/bucket/main.tf:5", Message: "Ensure the S3 bucket has access logging enabled", Resource: "module.data.aws_s3_bucket.this"},
				{Scanner: "trivy", Rule: "AWS-0057", Severity: "HIGH", Location: "main.tf", Message: "IAM policy uses wildcards", Resource: "module.roles"},
				{Scanner: "tflint", Rule: "terraform_required_version", Severity: "warning", Location: "providers.tf:1", Message: "required_version is required"},
			}},
		},
	})
	return r
}

// Each finding is about the resource the root imports at the address it
// names, with the type, import ID and service of its selection entry; a
// finding about a module call of several resources keeps the call, and
// one about none the root alone.
func TestFindings(t *testing.T) {
	t.Parallel()
	r := findingsReport()
	r.Finish(nil, nil, false)

	root, scope := "aws/1/eu-west-2", "aws/1/eu-west-2"
	want := []Finding{
		{Root: root, Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", ID: "acme-logs", Scope: scope, Service: "s3", Scanner: "conftest", Rule: "s3", Severity: "failure", Location: "main.tf", Message: "logs has no owner tag"},
		{Root: root, Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", ID: "acme-logs", Scope: scope, Service: "s3", Scanner: "trivy", Rule: "AWS-0089", Severity: "LOW", Location: "main.tf", Message: "S3 Bucket Logging"},
		{Root: root, Address: "module.data.aws_s3_bucket.this[0]", Type: "aws_s3_bucket", ID: "acme-data", Scope: scope, Service: "s3", Scanner: "checkov", Rule: "CKV_AWS_18", Location: "modules/bucket/main.tf:5", Message: "Ensure the S3 bucket has access logging enabled"},
		{Root: root, Address: "module.roles", Scope: scope, Scanner: "trivy", Rule: "AWS-0057", Severity: "HIGH", Location: "main.tf", Message: "IAM policy uses wildcards"},
		{Root: root, Scope: scope, Scanner: "tflint", Rule: "terraform_required_version", Severity: "warning", Location: "providers.tf:1", Message: "required_version is required"},
	}
	if !reflect.DeepEqual(r.Findings, want) {
		t.Errorf("got %+v,\nwant %+v", r.Findings, want)
	}

	md := r.Markdown()
	wantMD := "## Findings by resource\n\n" +
		"The scanners of the verification gate (G5), with your policies (`--policy`), found these in the roots, by the resource each is about, where they name one. trivy's, checkov's and your policies' describe the infrastructure's own settings, which the configuration mirrors: they don't fail the import, and are for you to review before applying, to change in the cloud, or in the code once it is applied. `coverage.json` lists them all, with each resource's type and import ID.\n\n" +
		"- `aws_s3_bucket.logs` in `aws/1/eu-west-2` (`acme-logs`, s3):\n" +
		"  - conftest: s3 (failure) main.tf: logs has no owner tag\n" +
		"  - trivy: AWS-0089 (low) main.tf: S3 Bucket Logging\n" +
		"- `module.data.aws_s3_bucket.this[0]` in `aws/1/eu-west-2` (`acme-data`, s3):\n" +
		"  - checkov: CKV_AWS_18 modules/bucket/main.tf:5: Ensure the S3 bucket has access logging enabled\n" +
		"- `module.roles` in `aws/1/eu-west-2`:\n" +
		"  - trivy: AWS-0057 (high) main.tf: IAM policy uses wildcards\n" +
		"- `aws/1/eu-west-2`, not about one resource:\n" +
		"  - tflint: terraform_required_version (warning) providers.tf:1: required_version is required\n"
	if !strings.Contains(md, wantMD) {
		t.Errorf("report.md lacks the findings:\n%s", md)
	}
}

// report.md lists a bounded number of findings, saying how many more
// coverage.json has, and no section without any.
func TestFindingsMarkdownIsBounded(t *testing.T) {
	t.Parallel()
	r := &Report{}
	for i := range maxMarkdownFindings + 3 {
		r.Findings = append(r.Findings, Finding{Root: "aws", Scanner: "trivy", Rule: fmt.Sprintf("AWS-%04d", i)})
	}
	r.Finish(nil, nil, false)

	md := r.Markdown()
	if got := strings.Count(md, "  - trivy: "); got != maxMarkdownFindings {
		t.Errorf("%d findings listed, want %d", got, maxMarkdownFindings)
	}
	if !strings.Contains(md, "\n3 more findings aren't listed here: `coverage.json` has them all.\n") {
		t.Errorf("no count of the rest:\n%s", md)
	}
	if md := (&Report{}).Markdown(); strings.Contains(md, "Findings by resource") {
		t.Errorf("a section without findings:\n%s", md)
	}
}

// coverage.json has the findings, and the report read back from it, as
// infraharvest report prints it, too.
func TestFindingsInCoverage(t *testing.T) {
	t.Parallel()
	r := findingsReport()
	r.Finish(nil, nil, false)
	out := t.TempDir()
	if err := r.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	read, err := Read(out)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read.Findings, r.Findings) {
		t.Errorf("read back %+v, want %+v", read.Findings, r.Findings)
	}
	var printed bytes.Buffer
	if err := read.WriteJSON(&printed); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Findings []map[string]string `json:"findings"`
	}
	if err := json.Unmarshal(printed.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"root": "aws/1/eu-west-2", "address": "aws_s3_bucket.logs", "type": "aws_s3_bucket", "id": "acme-logs",
		"scope": "aws/1/eu-west-2", "service": "s3", "scanner": "conftest", "rule": "s3", "severity": "failure",
		"location": "main.tf", "message": "logs has no owner tag",
	}
	if len(got.Findings) != len(r.Findings) || !reflect.DeepEqual(got.Findings[0], want) {
		t.Errorf("got %v, want first %v", got.Findings, want)
	}
}

// The findings of the reports of an import's parts are the import's.
func TestMergeFindings(t *testing.T) {
	t.Parallel()
	r, other := &Report{}, findingsReport()
	r.Merge(other)
	r.Finish(nil, nil, false)
	if len(r.Findings) != 5 || r.Findings[0].Service != "s3" {
		t.Errorf("got %+v", r.Findings)
	}
}
