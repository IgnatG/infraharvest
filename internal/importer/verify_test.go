// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"bytes"
	"context"
	"errors"
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

// A root that can't be checked is reported with why, the others are still
// checked, in order whatever the parallelism, and the exit code says a root
// couldn't be checked.
func TestVerifyTargetsGoOnPastARootThatCantBeChecked(t *testing.T) {
	targets := []verifyTarget{
		{dir: "/out/aws/1/eu-west-2", path: "aws/1/eu-west-2"},
		{dir: "/out/aws/1/global", path: "aws/1/global"},
		{dir: "/out/aws/1/us-east-1", path: "aws/1/us-east-1", err: errors.New("vars/ can't be read")},
		{dir: "/out/aws/2/eu-west-2", path: "aws/2/eu-west-2"},
	}
	check := func(_ context.Context, target verifyTarget) verifiedRoot {
		root := verifiedRoot{Path: target.path}
		switch {
		case target.err != nil:
			root.Error = target.err.Error()
		case target.path == "aws/1/global":
			root.Error = "terraform init: registry unreachable"
		default:
			root.Checks = []report.Check{{Name: gate.CheckFormat, Passed: true}}
		}
		return root
	}
	for _, parallel := range []int{1, 3} {
		results := verifyTargets(context.Background(), parallel, targets, check)
		var got []string
		for _, r := range results {
			got = append(got, r.Path+"|"+r.Error)
		}
		want := []string{"aws/1/eu-west-2|", "aws/1/global|terraform init: registry unreachable", "aws/1/us-east-1|vars/ can't be read", "aws/2/eu-west-2|"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parallel %d: got %v, want %v", parallel, got, want)
		}
		var exit *ExitError
		if err := verifyExit(results); !errors.As(err, &exit) || exit.Code != report.ExitCouldNotRun || !strings.Contains(err.Error(), "2 roots couldn't be checked") {
			t.Errorf("parallel %d: exit %v, want ExitCouldNotRun for 2 roots", parallel, err)
		}
	}

	var b bytes.Buffer
	if err := writeVerified(&b, []verifiedRoot{{Path: "aws/1/global", Environment: "dev", Error: "terraform init: registry unreachable"}}, FormatText); err != nil {
		t.Fatal(err)
	}
	if want := "aws/1/global (dev): couldn't be checked: terraform init: registry unreachable\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

// A panic in one root's check stops those not started, which say why.
func TestVerifyTargetsPanic(t *testing.T) {
	targets := []verifyTarget{{path: "a"}, {path: "b"}, {path: "c"}}
	results := verifyTargets(context.Background(), 1, targets, func(_ context.Context, target verifyTarget) verifiedRoot {
		if target.path == "a" {
			panic("boom")
		}
		return verifiedRoot{Path: target.path}
	})
	if results[0].Error == "" || !strings.Contains(results[1].Error, "a panicked") || !strings.Contains(results[2].Error, "a panicked") {
		t.Errorf("results: %+v", results)
	}
}

// Only check failures exit 1; nothing failed exits 0.
func TestVerifyExitChecks(t *testing.T) {
	if err := verifyExit([]verifiedRoot{{Path: "a", Checks: []report.Check{{Name: gate.CheckFormat, Passed: true}}}}); err != nil {
		t.Errorf("all passed: %v", err)
	}
	var exit *ExitError
	err := verifyExit([]verifiedRoot{{Path: "a", Checks: []report.Check{{Name: gate.CheckPlan, Details: []string{"x: update"}}}}})
	if !errors.As(err, &exit) || exit.Code != report.ExitIncomplete {
		t.Errorf("a check failed: %v, want ExitIncomplete", err)
	}
}

func TestVerifyRejectsNegativeParallel(t *testing.T) {
	err := Verify(context.Background(), VerifyOptions{Out: t.TempDir(), Engine: EngineTerraform, Format: FormatText, Parallel: -1}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--parallel") {
		t.Errorf("want a --parallel error, got %v", err)
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

// verify.json holds what --format json prints, whatever format verify
// prints, and sits in the report directory, which it makes.
func TestWriteVerifiedFile(t *testing.T) {
	results := []verifiedRoot{
		{Path: "aws/1/eu-west-2", Checks: []report.Check{{Name: gate.CheckFormat, Passed: true}}},
		{Path: "aws/1/global", Environment: "dev", Error: "terraform init: registry unreachable"},
	}
	out := t.TempDir()
	if err := writeVerifiedFile(out, results); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(out, report.Dir, report.VerifyJSONFile))
	if err != nil {
		t.Fatal(err)
	}
	var printed bytes.Buffer
	if err := writeVerified(&printed, results, FormatJSON); err != nil {
		t.Fatal(err)
	}
	if string(got) != printed.String() {
		t.Errorf("verify.json:\n%s\nwant what --format json prints:\n%s", got, printed.String())
	}
	// Written again, it replaces the file.
	if err := writeVerifiedFile(out, results[:1]); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(filepath.Join(out, report.Dir, report.VerifyJSONFile))
	if err != nil || strings.Contains(string(again), "registry unreachable") {
		t.Errorf("verify.json after a second run: %v\n%s", err, again)
	}
}

// A verify.json that can't be written is a warning: the checks' results
// still decide the exit code, as they do when it can be.
func TestFinishVerifyKeepsTheExitCodeWhenTheFileCantBeWritten(t *testing.T) {
	passed := verifiedRoot{Path: "a", Checks: []report.Check{{Name: gate.CheckFormat, Passed: true}}}
	failed := verifiedRoot{Path: "b", Checks: []report.Check{{Name: gate.CheckPlan, Details: []string{"x: update"}}}}
	uncheckable := verifiedRoot{Path: "c", Error: "terraform init: registry unreachable"}
	for name, tc := range map[string]struct {
		results []verifiedRoot
		want    int // exit code; 0: none
	}{
		"all passed":          {[]verifiedRoot{passed}, 0},
		"a check failed":      {[]verifiedRoot{passed, failed}, report.ExitIncomplete},
		"a root not checked":  {[]verifiedRoot{passed, uncheckable}, report.ExitCouldNotRun},
		"both of them failed": {[]verifiedRoot{failed, uncheckable}, report.ExitCouldNotRun},
	} {
		t.Run(name, func(t *testing.T) {
			logged := captureLog(t)
			// The report directory can't be made: a file is in its place, as
			// an output directory that is mounted read-only can't make it either.
			out := t.TempDir()
			if err := os.WriteFile(filepath.Join(out, report.Dir), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			err := finishVerify(out, tc.results)
			var exit *ExitError
			switch {
			case tc.want == 0 && err != nil:
				t.Errorf("exit %v, want none", err)
			case tc.want != 0 && (!errors.As(err, &exit) || exit.Code != tc.want):
				t.Errorf("exit %v, want code %d", err, tc.want)
			}
			if !strings.Contains(logged.String(), "verify.json isn't written") {
				t.Errorf("no warning that verify.json isn't written:\n%s", logged)
			}
			// Where it can be written, it is, and the exit code is the same.
			out = t.TempDir()
			err = finishVerify(out, tc.results)
			if (err == nil) != (tc.want == 0) {
				t.Errorf("with a writable directory, exit %v, want code %d", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(out, report.Dir, report.VerifyJSONFile)); err != nil {
				t.Errorf("verify.json: %v", err)
			}
		})
	}
}
