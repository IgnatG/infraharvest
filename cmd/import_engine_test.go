// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/engine"
	"github.com/IgnatG/infraharvest/terraformutils"
)

func TestImportsByDir(t *testing.T) {
	resources := map[string][]terraformutils.Resource{
		"sqs": {terraformutils.NewSimpleResource("https://sqs/1/orders", "orders", "aws_sqs_queue", "aws", nil)},
		"sns": {terraformutils.NewSimpleResource("arn:aws:sns:topic", "alerts", "aws_sns_topic", "aws", nil)},
		"s3":  nil, // a service with no resources gets no directory
	}

	t.Run("one directory per service", func(t *testing.T) {
		options := ImportOptions{PathPattern: DefaultPathPattern, PathOutput: "out"}

		got, _ := importsByDir("aws", options, resources, listerID)

		want := map[string][]engine.Import{
			filepath.Join("out", "aws", "sqs"): {{Type: "aws_sqs_queue", Name: "orders", ID: "https://sqs/1/orders"}},
			filepath.Join("out", "aws", "sns"): {{Type: "aws_sns_topic", Name: "alerts", ID: "arn:aws:sns:topic"}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("one directory for a pattern without {service}", func(t *testing.T) {
		options := ImportOptions{PathPattern: "{output}/{provider}/", PathOutput: "out"}

		got, _ := importsByDir("aws", options, resources, listerID)

		if len(got) != 1 || len(got[filepath.Join("out", "aws")]) != 2 {
			t.Errorf("want both resources in out/aws, got %v", got)
		}
	})
}

func listerID(r terraformutils.Resource) (string, bool) { return r.InstanceState.ID, true }

func TestImportsByDirUsesImportIDs(t *testing.T) {
	resources := map[string][]terraformutils.Resource{
		"route_table": {
			terraformutils.NewSimpleResource("rtbassoc-1", "a", "aws_route_table_association", "aws", nil),
			terraformutils.NewSimpleResource("rtbassoc-2", "main", "aws_main_route_table_association", "aws", nil),
		},
	}
	importID := func(r terraformutils.Resource) (string, bool) {
		if r.InstanceInfo.Type == "aws_main_route_table_association" {
			return "", false
		}
		return "subnet-1/rtb-1", true
	}
	options := ImportOptions{PathPattern: "{output}/{provider}/", PathOutput: "out"}

	got, skipped := importsByDir("aws", options, resources, importID)

	want := map[string][]engine.Import{
		filepath.Join("out", "aws"): {{Type: "aws_route_table_association", Name: "a", ID: "subnet-1/rtb-1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if !reflect.DeepEqual(skipped, map[string]int{"aws_main_route_table_association": 1}) {
		t.Errorf("skipped: got %v", skipped)
	}
}

type idProvider struct {
	terraformutils.ProviderGenerator
}

func (idProvider) ImportID(terraformutils.Resource) (string, bool) { return "mapped", true }

func TestImportIDFunc(t *testing.T) {
	r := terraformutils.NewSimpleResource("listed", "a", "aws_sqs_queue", "aws", nil)

	if id, ok := importIDFunc(sourcedProvider{})(r); id != "listed" || !ok {
		t.Errorf("without a mapping: got %q, %v; want the lister's ID", id, ok)
	}
	if id, ok := importIDFunc(idProvider{})(r); id != "mapped" || !ok {
		t.Errorf("with a mapping: got %q, %v; want the provider's ID", id, ok)
	}
}

type sourcedProvider struct {
	terraformutils.ProviderGenerator
	data map[string]interface{}
}

func (p sourcedProvider) GetName() string   { return "datadog" }
func (p sourcedProvider) GetSource() string { return "DataDog/datadog" }
func (p sourcedProvider) GetProviderData(...string) map[string]interface{} {
	return p.data
}

func TestEngineProvider(t *testing.T) {
	p := sourcedProvider{data: map[string]interface{}{
		"provider": map[string]interface{}{"datadog": map[string]interface{}{"api_url": "https://api.datadoghq.eu"}},
	}}

	got := engineProvider(p)

	want := engine.Provider{Name: "datadog", Source: "DataDog/datadog", Config: map[string]interface{}{"api_url": "https://api.datadoghq.eu"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// Unexpected provider data must not panic.
	if got := engineProvider(sourcedProvider{data: map[string]interface{}{"provider": "oops"}}); got.Config != nil {
		t.Errorf("want no config from malformed data, got %v", got.Config)
	}
}

func TestCheckTerraformEngineOptions(t *testing.T) {
	defaults := ImportOptions{State: DefaultState, Output: "hcl", Connect: true}
	if err := checkTerraformEngineOptions(defaults); err != nil {
		t.Errorf("defaults must be accepted, got %v", err)
	}
	withJSON := defaults
	withJSON.Output = outputJSON
	if err := checkTerraformEngineOptions(withJSON); err != nil {
		t.Errorf("--output json must be accepted, got %v", err)
	}

	bad := defaults
	bad.State, bad.Output, bad.Compact = "bucket", "yaml", true
	err := checkTerraformEngineOptions(bad)
	if err == nil {
		t.Fatal("want an error for unsupported options")
	}
	for _, want := range []string{"--state bucket", "--output yaml", "--compact"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestImportRejectsUnknownEngine(t *testing.T) {
	err := Import(&fakeProvider{}, ImportOptions{Engine: "tofu-ish"}, nil)
	if err == nil || !strings.Contains(err.Error(), `unknown --engine "tofu-ish"`) {
		t.Errorf("want an unknown engine error, got %v", err)
	}
}

// The engine labels resources by the name the lister gave, not by undoing
// the legacy sanitizing: "-" is part of names like my-2024-backups, and
// "-2024-" is not an escape.
func TestImportsByDirKeepRawNames(t *testing.T) {
	resources := map[string][]terraformutils.Resource{
		"s3": {
			terraformutils.NewSimpleResource("my-2024-backups", "my-2024-backups", "aws_s3_bucket", "aws", nil),
			terraformutils.NewSimpleResource("logs-2024-a", "logs-2024-a", "aws_s3_bucket", "aws", nil),
			terraformutils.NewSimpleResource("logs-2025-a", "logs-2025-a", "aws_s3_bucket", "aws", nil),
		},
		"ssm": {terraformutils.NewSimpleResource("/infraharvest-e2e/endpoint", "/infraharvest-e2e/endpoint", "aws_ssm_parameter", "aws", nil)},
	}
	options := ImportOptions{PathPattern: "{output}/{provider}/", PathOutput: "out"}

	got, _ := importsByDir("aws", options, resources, listerID)

	names := map[string]bool{}
	for _, imp := range got[filepath.Join("out", "aws")] {
		names[imp.Name] = true
	}
	for _, want := range []string{"my-2024-backups", "logs-2024-a", "logs-2025-a", "/infraharvest-e2e/endpoint"} {
		if !names[want] {
			t.Errorf("name %q lost; got %v", want, names)
		}
	}
}

func TestWriteGitignore(t *testing.T) {
	out := filepath.Join(t.TempDir(), "generated")

	if err := writeGitignore(out); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(out, ".gitignore"))
	if err != nil || !strings.Contains(string(content), "*.tfstate") {
		t.Fatalf("want a .gitignore excluding state, got %q, %v", content, err)
	}

	// A .gitignore the user already has is kept.
	if err := os.WriteFile(filepath.Join(out, ".gitignore"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeGitignore(out); err != nil {
		t.Fatal(err)
	}
	if content, _ := os.ReadFile(filepath.Join(out, ".gitignore")); string(content) != "mine\n" {
		t.Errorf("existing .gitignore overwritten: %q", content)
	}
}

func TestEngineBinaryAndRegistry(t *testing.T) {
	for _, tc := range []struct{ engineName, binary, source string }{
		{engineTerraform, "terraform", "registry.terraform.io/hashicorp/aws"},
		{engineTofu, "tofu", "registry.opentofu.org/hashicorp/aws"},
	} {
		b := engineBinary(tc.engineName)
		if b.Name != tc.binary {
			t.Errorf("--engine=%s runs %s, want %s", tc.engineName, b.Name, tc.binary)
		}
		if got := qualifiedSource(b.Registry, "hashicorp/aws"); got != tc.source {
			t.Errorf("--engine=%s: got %s, want %s", tc.engineName, got, tc.source)
		}
	}
	if got := qualifiedSource("registry.opentofu.org", "example.com/acme/thing"); got != "example.com/acme/thing" {
		t.Errorf("a source with a host must be kept, got %s", got)
	}
}

type scopedProvider struct {
	terraformutils.ProviderGenerator
}

func (scopedProvider) Scope(context.Context) (string, string, error) {
	return "111122223333", "eu-west-2", nil
}

func TestRootPathPattern(t *testing.T) {
	for pattern, want := range map[string]string{
		// Not given: one root per account and region.
		"": "{output}/{provider}/111122223333/eu-west-2/",
		// Given as the legacy default: followed as it is (see
		// defaultPathPattern).
		DefaultPathPattern:              DefaultPathPattern,
		"{output}/{account}/{service}/": "{output}/111122223333/{service}/",
		"{output}/{provider}/":          "{output}/{provider}/",
	} {
		got, err := rootPathPattern(t.Context(), scopedProvider{}, pattern)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", pattern, got, err, want)
		}
	}
}
