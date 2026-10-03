// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
)

func TestImportsFile(t *testing.T) {
	got, err := ImportsFile([]Import{
		{Type: "aws_sqs_queue", Name: "tfer--orders", ID: "https://sqs.eu-west-1.amazonaws.com/123/orders"},
		{Type: "aws_s3_bucket", Name: "tfer--logs", ID: `odd"id`},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := `import {
  to = aws_s3_bucket.tfer--logs
  id = "odd\"id"
}

import {
  to = aws_sqs_queue.tfer--orders
  id = "https://sqs.eu-west-1.amazonaws.com/123/orders"
}
`
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestImportsFileRejectsInvalidAddress(t *testing.T) {
	if _, err := ImportsFile([]Import{{Type: "aws_sqs_queue", Name: "has space", ID: "x"}}); err == nil {
		t.Error("want an error for an invalid resource name")
	}
}

func TestProvidersFile(t *testing.T) {
	got, err := ProvidersFile(Provider{
		Name:    "azurerm",
		Source:  "hashicorp/azurerm",
		Version: "~> 4.0",
		Config: map[string]interface{}{
			"subscription_id":            "sub",
			"features":                   map[string]interface{}{},
			"skip_provider_registration": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := `provider "azurerm" {
  features {
  }
  skip_provider_registration = true
  subscription_id            = "sub"
}
`
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestVersionsFile(t *testing.T) {
	got := VersionsFile(">= 1.16, < 2.0", Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.67"})

	want := `terraform {
  required_version = ">= 1.16, < 2.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.67"
    }
  }
}
`
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestProvidersFileRejectsUnsupportedValue(t *testing.T) {
	_, err := ProvidersFile(Provider{Name: "aws", Source: "hashicorp/aws", Config: map[string]interface{}{"region": struct{}{}}})
	if err == nil || !strings.Contains(err.Error(), "region") {
		t.Errorf("want an error naming the argument, got %v", err)
	}
}

// fakePlan is what one fake plan reports: diagnostics, or err for a failure
// without any.
type fakePlan struct {
	diags   []tfjson.Diagnostic
	err     error
	summary changeSummary // reported when there are no diags
}

// fakeTerraform records calls. Like Terraform, a plan with
// -generate-config-out writes generated.tf even when it then fails.
type fakeTerraform struct {
	dir         string
	generated   string                   // generated.tf content; a comment if empty
	plans       []fakePlan               // returned by successive plans; then clean
	validations []*tfjson.ValidateOutput // returned by successive validations; then valid
	schemas     *tfjson.ProviderSchemas
	shown       *tfjson.Plan   // returned by ShowPlanFile once showns is used up; empty if nil
	showns      []*tfjson.Plan // returned by successive ShowPlanFile calls
	initErr     error
	calls       []string
}

func (f *fakeTerraform) Init(context.Context, ...tfexec.InitOption) error {
	f.calls = append(f.calls, "init")
	return f.initErr
}

func (f *fakeTerraform) PlanJSON(_ context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	f.calls = append(f.calls, "plan")
	for _, opt := range opts {
		if _, ok := opt.(*tfexec.GenerateConfigOutOption); ok {
			content := f.generated
			if content == "" {
				content = "# generated\n"
			}
			if err := os.WriteFile(filepath.Join(f.dir, GeneratedFileName), []byte(content), 0o644); err != nil {
				return false, err
			}
		}
	}
	var p fakePlan
	if len(f.plans) > 0 {
		p, f.plans = f.plans[0], f.plans[1:]
	}
	for _, d := range p.diags {
		line, err := json.Marshal(map[string]interface{}{"type": "diagnostic", "diagnostic": d})
		if err != nil {
			return false, err
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			return false, err
		}
	}
	if p.err == nil && len(p.diags) == 0 {
		line, err := json.Marshal(map[string]interface{}{"type": "change_summary", "changes": p.summary})
		if err != nil {
			return false, err
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			return false, err
		}
	}
	if p.err == nil && len(p.diags) > 0 {
		p.err = errors.New("exit status 1")
	}
	return p.err == nil, p.err
}

func (f *fakeTerraform) Validate(context.Context) (*tfjson.ValidateOutput, error) {
	f.calls = append(f.calls, "validate")
	if len(f.validations) == 0 {
		return &tfjson.ValidateOutput{Valid: true}, nil
	}
	out := f.validations[0]
	f.validations = f.validations[1:]
	return out, nil
}

func (f *fakeTerraform) ProvidersSchema(context.Context) (*tfjson.ProviderSchemas, error) {
	f.calls = append(f.calls, "schema")
	return f.schemas, nil
}

func (f *fakeTerraform) ShowPlanFile(context.Context, string, ...tfexec.ShowOption) (*tfjson.Plan, error) {
	f.calls = append(f.calls, "show")
	if len(f.showns) > 0 {
		p := f.showns[0]
		f.showns = f.showns[1:]
		return p, nil
	}
	if f.shown == nil {
		return &tfjson.Plan{}, nil
	}
	return f.shown, nil
}

func (f *fakeTerraform) FormatCheck(context.Context, ...tfexec.FormatOption) (bool, []string, error) {
	f.calls = append(f.calls, "fmt")
	return true, nil, nil
}

func (f *fakeTerraform) SetStdout(io.Writer) {}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestGenerate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "aws", "sqs")
	tf := &fakeTerraform{dir: dir}

	config := map[string][]byte{VersionsFileName: []byte("# versions\n"), ProvidersFileName: []byte("# providers\n")}
	result, err := Generate(context.Background(), tf, dir, []Import{{Type: "aws_sqs_queue", Name: "Orders Queue", ID: "a"}}, Options{Config: config})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Join(tf.calls, ",") != "init,plan,plan,show,fmt,validate,plan,show" {
		t.Errorf("calls: got %v, want [init plan plan show fmt validate plan show]", tf.calls)
	}
	for _, name := range []string{VersionsFileName, ProvidersFileName, ImportsFileName, GeneratedFileName, ReadmeFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s not written: %v", name, err)
		}
	}
	for _, name := range []string{VariablesFileName, RejectedFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s written without secrets or rejections", name)
		}
	}
	if len(result.Secrets) != 0 || len(result.Rejected) != 0 {
		t.Errorf("want nothing left to do, got %+v", result)
	}
	if got := readFile(t, dir, ImportsFileName); !strings.Contains(got, "to = aws_sqs_queue.orders_queue") {
		t.Errorf("want the resource labelled orders_queue:\n%s", got)
	}
	readme := readFile(t, dir, ReadmeFileName)
	for _, want := range []string{"| `aws_sqs_queue` | 1 |", "1. Run `terraform init` and `terraform plan`", "3. Delete `imports.tf`"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md misses %q:\n%s", want, readme)
		}
	}
}

// Output directories such as generated/aws/sqs don't exist before the
// first import into them.
func TestNewTerraformCreatesDirs(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "generated", "aws", "sqs")
	cache := filepath.Join(base, "cache", "plugins")

	if _, err := NewTerraform(dir, os.Args[0], cache); err != nil {
		t.Fatal(err)
	}

	for _, d := range []string{dir, cache} {
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			t.Errorf("%s not created: %v", d, err)
		}
	}
}

func TestGenerateRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, GeneratedFileName), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	tf := &fakeTerraform{dir: dir}

	_, err := Generate(context.Background(), tf, dir, []Import{{Type: "aws_sqs_queue", Name: "a", ID: "a"}}, Options{})

	if err == nil || len(tf.calls) != 0 {
		t.Errorf("want an error before running Terraform, got err=%v calls=%v", err, tf.calls)
	}
}

func TestGenerateReportsTerraformFailures(t *testing.T) {
	dir := t.TempDir()
	tf := &fakeTerraform{dir: dir, plans: []fakePlan{{err: errors.New("Failed to load plugin schemas")}}}

	_, err := Generate(context.Background(), tf, dir, []Import{{Type: "aws_sqs_queue", Name: "a", ID: "a"}}, Options{})

	if err == nil || !strings.Contains(err.Error(), "terraform plan: Failed to load plugin schemas") {
		t.Errorf("want the plan error, got %v", err)
	}
}

// Errors no resource explains, such as invalid provider configuration, fail
// the whole directory.
func TestGenerateFailsOnDirectoryErrors(t *testing.T) {
	dir := t.TempDir()
	tf := &fakeTerraform{dir: dir, plans: []fakePlan{{diags: []tfjson.Diagnostic{
		{Severity: tfjson.DiagnosticSeverityError, Summary: "Invalid provider configuration", Range: &tfjson.Range{Filename: ProvidersFileName, Start: tfjson.Pos{Line: 1}}},
	}}}}

	_, err := Generate(context.Background(), tf, dir, []Import{{Type: "aws_sqs_queue", Name: "a", ID: "a"}}, Options{})

	if err == nil || !strings.Contains(err.Error(), "providers.tf:1: Invalid provider configuration") {
		t.Errorf("want the directory error, got %v", err)
	}
}

// importErrorAt is an error on line of imports.tf.
func importErrorAt(line int, summary string) tfjson.Diagnostic {
	return tfjson.Diagnostic{
		Severity: tfjson.DiagnosticSeverityError,
		Summary:  summary,
		Range:    &tfjson.Range{Filename: ImportsFileName, Start: tfjson.Pos{Line: line}, End: tfjson.Pos{Line: line}},
	}
}

func TestGenerateLeavesOutUnimportableResources(t *testing.T) {
	dir := t.TempDir()
	tf := &fakeTerraform{
		dir: dir,
		generated: `resource "aws_sqs_queue" "b" {
  name = "b"
}
`,
		// imports.tf: aws_sqs_queue.a on lines 1-4, aws_sqs_queue.b on 6-9.
		plans: []fakePlan{{diags: []tfjson.Diagnostic{
			importErrorAt(2, "Cannot import non-existent remote object"),
			importErrorAt(2, "Configuration for import target does not exist"),
		}}},
	}
	imports := []Import{{Type: "aws_sqs_queue", Name: "a", ID: "gone"}, {Type: "aws_sqs_queue", Name: "b", ID: "b"}}

	result, err := Generate(context.Background(), tf, dir, imports, Options{})
	if err != nil {
		t.Fatal(err)
	}

	want := []Rejection{{Address: "aws_sqs_queue.a", ID: "gone", Errors: []string{"Cannot import non-existent remote object", "Configuration for import target does not exist"}}}
	if !reflect.DeepEqual(result.Rejected, want) {
		t.Errorf("rejected: got %+v, want %+v", result.Rejected, want)
	}
	if strings.Join(tf.calls, ",") != "init,plan,validate,plan,plan,show,fmt,validate,plan,show" {
		t.Errorf("calls: got %v, want [init plan validate plan plan show fmt validate plan show]", tf.calls)
	}
	if got := readFile(t, dir, ImportsFileName); strings.Contains(got, "aws_sqs_queue.a") || !strings.Contains(got, "aws_sqs_queue.b") {
		t.Errorf("imports.tf should only import aws_sqs_queue.b:\n%s", got)
	}
	wantRejected := `# aws_sqs_queue.a was left out:
#   Cannot import non-existent remote object
#   Configuration for import target does not exist

import {
  to = aws_sqs_queue.a
  id = "gone"
}
`
	if got := readFile(t, dir, RejectedFileName); got != wantRejected {
		t.Errorf("rejected.hcl: got:\n%s\nwant:\n%s", got, wantRejected)
	}
}

const invalidGenerated = `resource "aws_route53_record" "a" {
  name                             = "example.internal"
  multivalue_answer_routing_policy = false
}
`

func TestGenerateRepairsAndReplans(t *testing.T) {
	dir := t.TempDir()
	rejected := errorAt(3, "Invalid combination of arguments", `"multivalue_answer_routing_policy": all of `+"`multivalue_answer_routing_policy,set_identifier`"+` must be specified`)
	tf := &fakeTerraform{
		dir:         dir,
		generated:   invalidGenerated,
		plans:       []fakePlan{{diags: []tfjson.Diagnostic{rejected}}},
		validations: []*tfjson.ValidateOutput{{Diagnostics: []tfjson.Diagnostic{rejected}}},
	}

	result, err := Generate(context.Background(), tf, dir, []Import{{Type: "aws_route53_record", Name: "a", ID: "Z1_example.internal_A"}}, Options{})
	if err != nil {
		t.Fatalf("want the repaired configuration to plan, got %v", err)
	}

	if strings.Join(tf.calls, ",") != "init,plan,validate,validate,plan,plan,show,fmt,validate,plan,show" {
		t.Errorf("calls: got %v, want [init plan validate validate plan plan show fmt validate plan show]", tf.calls)
	}
	if got := readFile(t, dir, GeneratedFileName); strings.Contains(got, "multivalue") || !strings.Contains(got, `name = "example.internal"`) {
		t.Errorf("zero value not removed or file not formatted:\n%s", got)
	}
	if len(result.Rejected) != 0 {
		t.Errorf("want nothing left out, got %+v", result.Rejected)
	}
}

func TestGenerateLeavesOutWhatRepairCannotFix(t *testing.T) {
	dir := t.TempDir()
	tf := &fakeTerraform{dir: dir, generated: invalidGenerated, plans: []fakePlan{
		{diags: []tfjson.Diagnostic{errorAt(2, "Invalid record name", "")}},
	}}

	result, err := Generate(context.Background(), tf, dir, []Import{{Type: "aws_route53_record", Name: "a", ID: "x"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Join(tf.calls, ",") != "init,plan,validate,plan,plan,show,fmt,validate,plan,show" {
		t.Errorf("calls: got %v, want [init plan validate plan plan show fmt validate plan show]", tf.calls)
	}
	if len(result.Rejected) != 1 || result.Rejected[0].Address != "aws_route53_record.a" {
		t.Errorf("want aws_route53_record.a left out, got %+v", result.Rejected)
	}
	if got := readFile(t, dir, GeneratedFileName); strings.Contains(got, "aws_route53_record") {
		t.Errorf("generated.tf still has the resource:\n%s", got)
	}
	if got := readFile(t, dir, RejectedFileName); !strings.Contains(got, `resource "aws_route53_record" "a"`) || !strings.Contains(got, "#   Invalid record name") {
		t.Errorf("rejected.hcl misses the resource or its error:\n%s", got)
	}
}

func TestGenerateGivesUpAfterMaxRounds(t *testing.T) {
	dir := t.TempDir()
	stuck := fakePlan{diags: []tfjson.Diagnostic{importErrorAt(2, "Cannot import")}}
	tf := &fakeTerraform{dir: dir, plans: []fakePlan{stuck, stuck, stuck, stuck, stuck}}
	// Each plan reports another import that fails.
	imports := []Import{{Type: "aws_sqs_queue", Name: "a", ID: "a"}, {Type: "aws_sqs_queue", Name: "b", ID: "b"}, {Type: "aws_sqs_queue", Name: "c", ID: "c"}, {Type: "aws_sqs_queue", Name: "d", ID: "d"}}

	_, err := Generate(context.Background(), tf, dir, imports, Options{})

	if err == nil || !strings.Contains(err.Error(), "Cannot import") {
		t.Errorf("want the remaining error, got %v", err)
	}
}

func TestGenerateRepairsWhatValidationRejects(t *testing.T) {
	dir := t.TempDir()
	rejected := errorAt(3, "expected rotation_period_in_days to be in the range (90 - 2560), got 0", "")
	tf := &fakeTerraform{
		dir: dir,
		generated: `resource "aws_kms_key" "a" {
  description             = "app"
  rotation_period_in_days = 0
}
`,
		plans:       []fakePlan{{diags: []tfjson.Diagnostic{rejected}}},
		validations: []*tfjson.ValidateOutput{{Diagnostics: []tfjson.Diagnostic{rejected}}},
	}

	_, err := Generate(context.Background(), tf, dir, []Import{{Type: "aws_kms_key", Name: "a", ID: "k"}}, Options{})
	if err != nil {
		t.Fatalf("want the repaired configuration to plan, got %v", err)
	}

	if strings.Join(tf.calls, ",") != "init,plan,validate,validate,plan,plan,show,fmt,validate,plan,show" {
		t.Errorf("calls: got %v, want [init plan validate validate plan plan show fmt validate plan show]", tf.calls)
	}
	if got := readFile(t, dir, GeneratedFileName); strings.Contains(got, "rotation_period_in_days") {
		t.Errorf("rejected zero value not removed:\n%s", got)
	}
}

const ssmGenerated = `resource "aws_ssm_parameter" "app_env" {
  name     = "/app/env"
  type     = "SecureString"
  value    = null # sensitive
  value_wo = null # sensitive
}
`

var ssmSchemas = &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
	"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
		"aws_ssm_parameter": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
			"value":    {AttributeType: cty.String, Optional: true, Sensitive: true},
			"value_wo": {AttributeType: cty.String, Optional: true, Sensitive: true, WriteOnly: true},
		}}},
	}},
}}

func TestGenerateMovesSecretsToVariables(t *testing.T) {
	dir := t.TempDir()
	tf := &fakeTerraform{
		dir:       dir,
		generated: ssmGenerated,
		plans: []fakePlan{{diags: []tfjson.Diagnostic{
			errorAt(4, "Invalid combination of arguments", `"value": one of insecure_value,value,value_wo must be specified`),
		}}},
		schemas: ssmSchemas,
	}

	result, err := Generate(context.Background(), tf, dir, []Import{{Type: "aws_ssm_parameter", Name: "app_env", ID: "/app/env"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	// The secret error needs no repair. Validation checks the variable;
	// post-processing plans with a placeholder value for it.
	if strings.Join(tf.calls, ",") != "init,plan,schema,validate,plan,show,fmt,validate,plan,show" {
		t.Errorf("calls: got %v, want [init plan schema validate plan show fmt validate plan show]", tf.calls)
	}
	wantSecrets := []Secret{{Variable: "aws_ssm_parameter_app_env_value", Address: "aws_ssm_parameter.app_env", Attribute: "value", schemaPath: []string{"value"}, ty: cty.String}}
	if !reflect.DeepEqual(result.Secrets, wantSecrets) {
		t.Errorf("secrets: got %+v, want %+v", result.Secrets, wantSecrets)
	}
	got := readFile(t, dir, GeneratedFileName)
	if !strings.Contains(got, "value    = var.aws_ssm_parameter_app_env_value # sensitive") {
		t.Errorf("generated.tf doesn't read the variable:\n%s", got)
	}
	// Write-only arguments are never stored: unset is right for an import.
	if !strings.Contains(got, "value_wo = null") {
		t.Errorf("write-only value_wo should stay null:\n%s", got)
	}
	wantVariables := `variable "aws_ssm_parameter_app_env_value" {
  description = "value of aws_ssm_parameter.app_env. Terraform doesn't write secret values into the configuration it generates: set it before planning, for example in a .tfvars file kept out of version control."
  type        = string
  sensitive   = true
}
`
	if got := readFile(t, dir, VariablesFileName); got != wantVariables {
		t.Errorf("variables.tf: got:\n%s\nwant:\n%s", got, wantVariables)
	}
}

func TestGenerateLeavesOutSecretResourcesThatDontValidate(t *testing.T) {
	dir := t.TempDir()
	tf := &fakeTerraform{
		dir:       dir,
		generated: ssmGenerated,
		plans: []fakePlan{{diags: []tfjson.Diagnostic{
			errorAt(4, "Invalid combination of arguments", `"value": one of insecure_value,value,value_wo must be specified`),
		}}},
		validations: []*tfjson.ValidateOutput{{Diagnostics: []tfjson.Diagnostic{errorAt(3, "expected type to be one of [String StringList SecureString]", "")}}},
		schemas:     ssmSchemas,
	}

	result, err := Generate(context.Background(), tf, dir, []Import{{Type: "aws_ssm_parameter", Name: "app_env", ID: "/app/env"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Secrets) != 0 || len(result.Rejected) != 1 {
		t.Errorf("want the resource left out with its secret, got %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, VariablesFileName)); err == nil {
		t.Error("variables.tf kept without secrets")
	}
}

func TestBinaryFind(t *testing.T) {
	versions := map[string]string{}
	versionOf := func(_ context.Context, path string) (*version.Version, error) {
		v, ok := versions[path]
		if !ok {
			return nil, errors.New("not an engine binary")
		}
		return version.NewVersion(v)
	}
	installed := ""
	terraform := TerraformBinary
	terraform.install = func(_ context.Context, dir string) (string, error) {
		if _, err := os.Stat(dir); err != nil {
			return "", err // find must create the cache directory first
		}
		installed = filepath.Join(dir, terraform.fileName())
		return installed, nil
	}
	t.Setenv("PATH", t.TempDir()) // no engine on PATH

	t.Run("explicit path too old", func(t *testing.T) {
		versions["/old/terraform"] = "1.4.6"
		if _, err := terraform.find(context.Background(), "/old/terraform", t.TempDir(), versionOf); err == nil {
			t.Error("want an error for Terraform older than 1.5")
		}
	})

	t.Run("cached binary is reused", func(t *testing.T) {
		cache := t.TempDir()
		cached := filepath.Join(cache, terraform.fileName())
		if err := os.WriteFile(cached, nil, 0o755); err != nil {
			t.Fatal(err)
		}
		versions[cached] = "1.16.5"
		installed = ""

		got, err := terraform.find(context.Background(), "", cache, versionOf)

		if err != nil || got != cached || installed != "" {
			t.Errorf("got %q, %v (installed %q); want the cached binary without installing", got, err, installed)
		}
	})

	t.Run("installs when nothing qualifies", func(t *testing.T) {
		cache := filepath.Join(t.TempDir(), "not-yet-created")

		got, err := terraform.find(context.Background(), "", cache, versionOf)

		if err != nil || got != filepath.Join(cache, terraform.fileName()) {
			t.Errorf("got %q, %v; want a fresh install into the cache", got, err)
		}
	})

	t.Run("OpenTofu is not downloaded", func(t *testing.T) {
		_, err := TofuBinary.find(context.Background(), "", t.TempDir(), versionOf)

		if err == nil || !strings.Contains(err.Error(), "tofu 1.6.0 or newer not found") {
			t.Errorf("want an error asking to install OpenTofu, got %v", err)
		}
	})

	t.Run("OpenTofu older than 1.6", func(t *testing.T) {
		versions["/old/tofu"] = "1.5.7"
		if _, err := TofuBinary.find(context.Background(), "/old/tofu", t.TempDir(), versionOf); err == nil {
			t.Error("want an error for OpenTofu older than 1.6")
		}
	})
}
