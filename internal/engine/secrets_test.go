// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
)

// fakeSecret makes a 64-character hex secret from seed, like the Lambda
// environment secrets of the sandbox run. Made at run time, so that secret
// scanners don't take the source for a leak.
func fakeSecret(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// assertNotWritten fails if any file under dir holds one of values.
func assertNotWritten(t *testing.T, dir string, values ...string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, v := range values {
			if strings.Contains(string(content), v) {
				t.Errorf("%s holds the secret value %q:\n%s", path, v, content)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The sandbox run wrote two Lambda environment secrets verbatim: the
// provider doesn't mark map values sensitive, so Terraform generated them.
// Moving them changes generated.tf, so Generate says it plans again.
func TestGenerateMovesEnvironmentSecretsToVariables(t *testing.T) {
	var logged bytes.Buffer
	writer, flags := log.Writer(), log.Flags()
	log.SetOutput(&logged)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(writer)
		log.SetFlags(flags)
	})
	dir := t.TempDir()
	// The engine writes the secrets' var file under the temporary
	// directory, which the fake then reads it from.
	secretsTemp := t.TempDir()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, secretsTemp)
	}
	value := fakeSecret("webhook")
	tf := &fakeTerraform{dir: dir, secretsTemp: secretsTemp, generated: `resource "aws_lambda_function" "api" {
  function_name = "api"
  role          = "arn:aws:iam::123456789012:role/api"
  environment {
    variables = {
      LOG_LEVEL      = "debug"
      TABLE_ARN      = "arn:aws:dynamodb:eu-west-2:123456789012:table/orders"
      WEBHOOK_SECRET = "` + value + `"
    }
  }
}
`}

	result, err := Generate(context.Background(), tf, dir, []Import{{Type: "aws_lambda_function", Name: "api", ID: "api"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	want := []secret.Variable{{Variable: "aws_lambda_function_api_webhook_secret", Address: "aws_lambda_function.api", Attribute: "environment.variables.WEBHOOK_SECRET"}}
	var got []secret.Variable
	for _, s := range result.Secrets {
		got = append(got, secret.Variable{Variable: s.Variable, Address: s.Address, Attribute: s.Attribute})
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("secrets: got %+v, want %+v", got, want)
	}
	if wantLog := dir + ": planning generated.tf again: " + string(replanSecrets) + "\n"; logged.String() != wantLog {
		t.Errorf("log: got %q, want %q", logged.String(), wantLog)
	}
	generated := readFile(t, dir, GeneratedFileName)
	for _, line := range []string{"WEBHOOK_SECRET = var.aws_lambda_function_api_webhook_secret", `LOG_LEVEL      = "debug"`, `TABLE_ARN      = "arn:aws:dynamodb:`} {
		if !strings.Contains(generated, line) {
			t.Errorf("missing %q in:\n%s", line, generated)
		}
	}
	variables := readFile(t, dir, VariablesFileName)
	for _, want := range []string{`variable "aws_lambda_function_api_webhook_secret" {`, "type        = string", "sensitive   = true", "secret store", "TF_VAR_aws_lambda_function_api_webhook_secret"} {
		if !strings.Contains(variables, want) {
			t.Errorf("missing %q in variables.tf:\n%s", want, variables)
		}
	}
	if strings.Contains(variables, "default") {
		t.Errorf("a secret variable has a default:\n%s", variables)
	}
	if readme := readFile(t, dir, ReadmeFileName); !strings.Contains(readme, "`aws_lambda_function_api_webhook_secret`") {
		t.Errorf("README doesn't list the variable:\n%s", readme)
	}
	// Infraharvest's own plans had the value, from outside the output, so
	// the plan stays clean; the file is gone afterwards.
	if len(tf.varFiles) == 0 {
		t.Fatal("no plan was given the secret's value")
	}
	for path, content := range tf.varFiles {
		if !strings.Contains(content, value) {
			t.Errorf("var file %s lacks the value: %s", path, content)
		}
		if strings.HasPrefix(path, dir) {
			t.Errorf("var file %s is in the output", path)
		}
		if _, err := os.Stat(path); err == nil {
			t.Errorf("var file %s left behind", path)
		}
	}
	if !result.Gate.Named(gate.CheckSecrets).Passed {
		t.Errorf("G6 failed: %+v", result.Gate.Named(gate.CheckSecrets))
	}
	assertNotWritten(t, dir, value)
}

// The gate fails a root with a secret value left in it, such as one an
// edit put back.
func TestScanSecretsFailsOnPlantedSecret(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_lambda_function" "api" {
  function_name = "api"
  environment {
    variables = {
      API_SECRET = "`+fakeSecret("planted")+`"
    }
  }
}
`)
	writeConfig(t, dir, ImportsFileName, `import {
  to = aws_lambda_function.api
  id = "api"
}
`)

	checks, _, err := withChecks(gate.Policy{Checks: []string{gate.CheckSecrets}}).Run(t.Context(), &fakeTerraform{dir: dir}, dir)
	if err != nil {
		t.Fatal(err)
	}
	check := checks.Named(gate.CheckSecrets)

	if check.Passed || len(check.Details) != 1 || !strings.Contains(check.Details[0], "generated.tf: environment.variables.API_SECRET of resource.aws_lambda_function.api holds a secret value") {
		t.Errorf("want G6 to fail on the secret, got %+v", check)
	}
}

// The gate ran before Generate wrote the README, which shows its details.
func TestRedactResultAndCheckReadme(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	value := fakeSecret("readme")
	// The secret is one Generate found in the configuration: its value
	// is known.
	writeConfig(t, dir, GeneratedFileName, `resource "aws_lambda_function" "a" {
  environment {
    variables = { API_SECRET = "`+value+`" }
  }
}
`)
	secrets, _, err := secret.LiteralsToVariables(filepath.Join(dir, GeneratedFileName), nil)
	if err != nil || len(secrets) != 1 {
		t.Fatalf("got %+v, %v; want the secret in a variable", secrets, err)
	}
	result := &Result{
		Gate:    gate.Gate{{Name: gate.CheckPlan, Details: []string{`generated.tf:3: Invalid value: got "` + value + `"`}}, {Name: gate.CheckSecrets, Passed: true}},
		Modules: []ModuleCall{{Name: "vpc", Declined: "plan error: got " + value}},
	}
	redactResult(result, secrets)
	if err := WriteReadme(dir, result); err != nil {
		t.Fatal(err)
	}
	if err := checkReadme(dir, result, secret.KnownValues(secrets)); err != nil {
		t.Fatal(err)
	}
	assertNotWritten(t, dir, value)
	if !result.Gate.Named(gate.CheckSecrets).Passed {
		t.Errorf("G6 failed on a redacted README: %+v", result.Gate.Named(gate.CheckSecrets))
	}

	// Should a value get through, G6 fails and the README says so.
	leaky := &Result{Gate: gate.Gate{{Name: gate.CheckPlan, Details: []string{"got " + value}}, {Name: gate.CheckSecrets, Passed: true}}}
	if err := WriteReadme(dir, leaky); err != nil {
		t.Fatal(err)
	}
	if err := checkReadme(dir, leaky, secret.KnownValues(secrets)); err != nil {
		t.Fatal(err)
	}
	g6 := leaky.Gate.Named(gate.CheckSecrets)
	if g6.Passed || len(g6.Details) != 1 || !strings.HasPrefix(g6.Details[0], ReadmeFileName+": ") {
		t.Errorf("G6 didn't fail on the README: %+v", g6)
	}
	if readme := readFile(t, dir, ReadmeFileName); !strings.Contains(readme, gate.CheckSecrets+" | **failed**") {
		t.Errorf("README doesn't show G6 failing:\n%s", readme)
	}
}

// Only a SecureString parameter's value is secret: a String or StringList
// parameter's is written as it is, which plans clean after import, and the
// gate doesn't take it for a leak although the provider marks it
// sensitive.
func TestGenerateWritesStringParameterValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		typ, name, value string
		variable         bool
	}{
		{"String", "/app/region", "eu-west-2-primary", false},
		{"StringList", "/app/zones", "eu-west-2a,eu-west-2b", false},
		{"SecureString", "/app/key", "s3cure-value", true},
		// Its name says it is a secret, whatever its type.
		{"String", "/app/db_password", "plain-but-secret", true},
	} {
		t.Run(tc.typ+tc.name, func(t *testing.T) {
			dir := t.TempDir()
			generated := strings.ReplaceAll(strings.ReplaceAll(ssmGenerated, "SecureString", tc.typ), "/app/env", tc.name)
			tf := &fakeTerraform{
				dir:       dir,
				generated: generated,
				plans: []fakePlan{{diags: []tfjson.Diagnostic{
					errorAt(4, "Invalid combination of arguments", `"value": one of insecure_value,value,value_wo must be specified`),
				}}},
				schemas: ssmSchemas,
				shown: &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{{
					Address: "aws_ssm_parameter.app_env",
					Mode:    tfjson.ManagedResourceMode,
					Type:    "aws_ssm_parameter",
					Change: &tfjson.Change{
						Actions:         tfjson.Actions{tfjson.ActionNoop},
						Before:          map[string]any{"name": tc.name, "type": tc.typ, "value": tc.value},
						BeforeSensitive: map[string]any{"value": true},
					},
				}}},
			}

			result, err := Generate(context.Background(), tf, dir, []Import{{Type: "aws_ssm_parameter", Name: "app_env", ID: tc.name}}, Options{})
			if err != nil {
				t.Fatal(err)
			}

			got := readFile(t, dir, GeneratedFileName)
			_, statErr := os.Stat(filepath.Join(dir, VariablesFileName))
			if tc.variable {
				if len(result.Secrets) != 1 || statErr != nil || !strings.Contains(got, "value = var.aws_ssm_parameter_app_env_value") {
					t.Errorf("want the value in a variable, got secrets %+v:\n%s", result.Secrets, got)
				}
				if strings.Contains(got, tc.value) {
					t.Errorf("secret value written:\n%s", got)
				}
				return
			}
			if len(result.Secrets) != 0 || statErr == nil {
				t.Errorf("want no secret variable, got %+v", result.Secrets)
			}
			if !strings.Contains(got, `value = "`+tc.value+`"`) || strings.Contains(got, "# sensitive") {
				t.Errorf("want the value written as it is:\n%s", got)
			}
			if check := result.Gate.Named(gate.CheckSecrets); !check.Passed {
				t.Errorf("G6 failed: %+v", check)
			}
		})
	}
}

// importedChange is the change of a plan that imports the object before
// at address, unchanged.
func importedChange(address string, before map[string]any) *tfjson.ResourceChange {
	return &tfjson.ResourceChange{
		Address: address,
		Mode:    tfjson.ManagedResourceMode,
		Type:    strings.SplitN(address, ".", 2)[0],
		Change:  &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionNoop}, Before: before},
	}
}

// A secret argument the imported object holds no value for, as a
// database's master password, which AWS never returns, is left out rather
// than read from a variable: unset, it plans as imported, where a variable
// would make the plan set it. One the object holds a value for keeps its
// variable, as does one the schema requires.
func TestGenerateLeavesOutUnsetSecrets(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tf := &fakeTerraform{
		dir: dir,
		generated: `resource "aws_redshift_cluster" "dw" {
  cluster_identifier = "dw"
  master_password    = null # sensitive
}

resource "aws_secretsmanager_secret_version" "app" {
  secret_id     = "app"
  secret_string = null # sensitive
}

resource "aws_lightsail_database" "db" {
  master_password = null # sensitive
  name            = "db"
}
`,
		schemas: &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
			"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
				"aws_redshift_cluster": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
					"master_password": {AttributeType: cty.String, Optional: true, Sensitive: true},
				}}},
				"aws_secretsmanager_secret_version": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
					"secret_string": {AttributeType: cty.String, Optional: true, Sensitive: true},
				}}},
				"aws_lightsail_database": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
					"master_password": {AttributeType: cty.String, Required: true, Sensitive: true},
				}}},
			}},
		}},
		shown: &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
			importedChange("aws_redshift_cluster.dw", map[string]any{"cluster_identifier": "dw", "master_password": nil}),
			importedChange("aws_secretsmanager_secret_version.app", map[string]any{"secret_id": "app", "secret_string": "from the state"}),
			importedChange("aws_lightsail_database.db", map[string]any{"name": "db", "master_password": nil}),
		}},
	}

	result, err := Generate(context.Background(), tf, dir, []Import{
		{Type: "aws_redshift_cluster", Name: "dw", ID: "dw"},
		{Type: "aws_secretsmanager_secret_version", Name: "app", ID: "app"},
		{Type: "aws_lightsail_database", Name: "db", ID: "db"},
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	var variables []string
	for _, s := range result.Secrets {
		variables = append(variables, s.Variable)
	}
	// In the order of the resources, which the configuration sorts by type.
	if want := []string{"aws_lightsail_database_db_master_password", "aws_secretsmanager_secret_version_app_secret_string"}; !reflect.DeepEqual(variables, want) {
		t.Errorf("variables: got %v, want %v", variables, want)
	}
	got := readFile(t, dir, GeneratedFileName)
	start := strings.Index(got, `resource "aws_redshift_cluster"`)
	if start < 0 {
		t.Fatalf("the cluster is gone:\n%s", got)
	}
	cluster, _, _ := strings.Cut(got[start:], "\n}")
	if strings.Contains(cluster, "master_password") {
		t.Errorf("want the cluster's unset master_password left out:\n%s", got)
	}
	if !strings.Contains(got, "= var.aws_lightsail_database_db_master_password") {
		t.Errorf("want the required master_password read from its variable:\n%s", got)
	}
}
