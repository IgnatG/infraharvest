// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

func TestGeneratedRoots(t *testing.T) {
	out := t.TempDir()
	for _, file := range []string{
		"aws/111122223333/eu-west-2/" + rootfile.ProvidersFileName,
		"aws/111122223333/global/" + rootfile.ProvidersFileName,
		"aws/111122223333/eu-west-2/.terraform/modules/state/" + rootfile.ProvidersFileName,
		CheckpointDir + "/incremental/aws/111122223333/eu-west-2/" + rootfile.ProvidersFileName,
		hclfile.ModulesDirName + "/s3_bucket_0123abcd/main.tf",
		report.Dir + "/report.md",
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
	err := Verify(context.Background(), VerifyOptions{Out: t.TempDir(), Engine: EngineTerraform, Format: FormatText}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "no generated roots") {
		t.Errorf("want an error, got %v", err)
	}
}

func TestWriteVerified(t *testing.T) {
	results := []verifiedRoot{
		{Path: "aws/1/eu-west-2", Checks: []report.Check{{Name: gate.CheckFormat, Passed: true}, {Name: gate.CheckScanners, Passed: true, Skipped: true}}},
		{Path: "aws/1/global", Checks: []report.Check{{Name: gate.CheckFormat, Passed: true}, {Name: gate.CheckPlan, Details: []string{"aws_iam_role.a: update"}}}},
	}
	var b bytes.Buffer
	if err := writeVerified(&b, results, FormatText); err != nil {
		t.Fatal(err)
	}
	if want := "aws/1/eu-west-2: passed; skipped G5 scanners\naws/1/global: failed G3 plan\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}

	// The scanners' findings follow, by resource, as report.md lists them.
	checks := gate.Gate{{Name: gate.CheckScanners, Passed: true, Findings: []gate.Finding{
		{Scanner: "trivy", Rule: "AWS-0089", Severity: "LOW", Location: "main.tf", Message: "S3 Bucket Logging", Resource: "aws_s3_bucket.logs"},
		{Scanner: "tflint", Rule: "terraform_required_version", Severity: "warning", Location: "providers.tf:1", Message: "required_version is required"},
	}}}
	managed := []engine.Managed{{Type: "aws_s3_bucket", ID: "acme-logs", Address: "aws_s3_bucket.logs"}}
	results = []verifiedRoot{
		{Path: "aws/eu-west-2", Environment: "prod", Checks: []report.Check{{Name: gate.CheckScanners, Passed: true}}, Findings: report.GateFindings("aws/eu-west-2", "prod", managed, checks)},
		{Path: "aws/1/global", Checks: []report.Check{{Name: gate.CheckScanners, Passed: true}}},
	}
	b.Reset()
	if err := writeVerified(&b, results, FormatText); err != nil {
		t.Fatal(err)
	}
	want := "aws/eu-west-2 (prod): passed\naws/1/global: passed\n\nFindings by resource:\n" +
		"- `aws_s3_bucket.logs` in `aws/eu-west-2` as prod (`acme-logs`):\n" +
		"  - trivy: AWS-0089 (low) main.tf: S3 Bucket Logging\n" +
		"- `aws/eu-west-2` as prod, not about one resource:\n" +
		"  - tflint: terraform_required_version (warning) providers.tf:1: required_version is required\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
	b.Reset()
	if err := writeVerified(&b, results[:1], FormatJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"findings": [`) || !strings.Contains(b.String(), `"id": "acme-logs"`) {
		t.Errorf("--format json lacks the findings:\n%s", b.String())
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
	if err := PrintReport(out, FormatText, &md); err != nil {
		t.Fatal(err)
	}
	if err := PrintReport(out, FormatJSON, &js); err != nil {
		t.Fatal(err)
	}
	if md.String() != r.Markdown() {
		t.Errorf("markdown differs:\n%s", md.String())
	}
	if !strings.Contains(js.String(), `"schema_version": 1`) {
		t.Errorf("json: %s", js.String())
	}
	if err := PrintReport(t.TempDir(), FormatText, &md); err == nil {
		t.Error("want an error without a report")
	}
}

// namedProvider is a provider of the given Terraform provider name.
type namedProvider struct {
	fakeProvider
	name string
}

func (p *namedProvider) Name() string { return p.name }

// A root's provider comes from the provider blocks it configures, so roots
// per environment (envs/<env>/aws/...) and layouts without {provider}
// first keep their catalogue's omit and state-only rules; without
// providers.tf, from the first directory of the path that names one.
func TestRootProvider(t *testing.T) {
	out := t.TempDir()
	byCommand := map[string]string{"aws": "aws", "azure": "azurerm", "google": "google"}
	options := VerifyOptions{Names: []string{"aws", "azure", "google"}, Provider: func(name string) harvest.Provider {
		if tf, ok := byCommand[name]; ok {
			return &namedProvider{name: tf}
		}
		return nil
	}}
	for dir, tc := range map[string]struct{ providers, want string }{
		"envs/prod/azure/1/all":       {`provider "azurerm" {` + "\n  features {}\n}\n", "azurerm"},
		"envs/aws/google/p/global":    {`provider "google-beta" {}` + "\n", "google"},
		"111122223333/eu-west-2":      {`provider "aws" {}` + "\n" + `provider "aws" {` + "\n  alias = \"untagged\"\n}\n", "aws"},
		"envs/staging/aws/1/global":   {"", "aws"},
		"envs/staging/other/1/global": {"", ""},
	} {
		root := filepath.Join(out, filepath.FromSlash(dir))
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if tc.providers != "" {
			if err := os.WriteFile(filepath.Join(root, rootfile.ProvidersFileName), []byte(tc.providers), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got := ""
		if p := rootProvider(out, root, options); p != nil {
			got = p.Name()
		}
		if got != tc.want {
			t.Errorf("%s: got provider %q, want %q", dir, got, tc.want)
		}
	}
}
