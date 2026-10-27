// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
)

const secretsConfig = `resource "aws_db_instance" "app--main" {
  identifier = "main"
  password   = null # sensitive
  username   = null
  note       = "x" # sensitive
  restore {
    token = null # sensitive
  }
  replica {
    secret = null # sensitive
  }
  replica {
    secret = null # sensitive
  }
}

resource "aws_db_instance" "app-main" {
  password = null # sensitive
}
`

func TestFindSecrets(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, t.TempDir(), GeneratedFileName, secretsConfig)

	found := findSecrets(f)

	var got []string
	for _, addr := range []string{"aws_db_instance.app--main", "aws_db_instance.app-main"} {
		for _, s := range found[addr] {
			got = append(got, addr+" "+s.path+" "+strings.Join(s.schemaPath, "/"))
		}
	}
	want := []string{
		"aws_db_instance.app--main password password",
		"aws_db_instance.app--main restore.token restore/token",
		"aws_db_instance.app--main replica[0].secret replica/secret",
		"aws_db_instance.app--main replica[1].secret replica/secret",
		"aws_db_instance.app-main password password",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSecretsToVariables(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, t.TempDir(), GeneratedFileName, secretsConfig)

	secrets := secretsToVariables(f, findSecrets(f), nil)

	var names []string
	for _, s := range secrets {
		names = append(names, s.Variable)
	}
	// app--main and app-main both become app_main.
	want := []string{
		"aws_db_instance_app_main_password",
		"aws_db_instance_app_main_restore_token",
		"aws_db_instance_app_main_replica_0_secret",
		"aws_db_instance_app_main_replica_1_secret",
		"aws_db_instance_app_main_password_2",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("got %q, want %q", names, want)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, filepath.Dir(f.Path), GeneratedFileName)
	for _, line := range []string{
		"password   = var.aws_db_instance_app_main_password # sensitive",
		"secret = var.aws_db_instance_app_main_replica_1_secret # sensitive",
		"password = var.aws_db_instance_app_main_password_2 # sensitive",
		"username   = null\n",
		`note       = "x" # sensitive`,
	} {
		if !strings.Contains(got, line) {
			t.Errorf("missing %q in:\n%s", line, got)
		}
	}
}

func TestAttributeType(t *testing.T) {
	t.Parallel()
	schemas := &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
		"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
			"aws_db_instance": {Block: &tfjson.SchemaBlock{
				Attributes: map[string]*tfjson.SchemaAttribute{"password": {AttributeType: cty.String}},
				NestedBlocks: map[string]*tfjson.SchemaBlockType{"replica": {Block: &tfjson.SchemaBlock{
					Attributes: map[string]*tfjson.SchemaAttribute{"secrets": {AttributeType: cty.Map(cty.String)}},
				}}},
			}},
		}},
	}}
	for _, tc := range []struct {
		resourceType string
		path         []string
		want         cty.Type
	}{
		{"aws_db_instance", []string{"password"}, cty.String},
		{"aws_db_instance", []string{"replica", "secrets"}, cty.Map(cty.String)},
		{"aws_db_instance", []string{"restore", "token"}, cty.NilType},
		{"aws_s3_bucket", []string{"password"}, cty.NilType},
	} {
		if got := attributeType(schemas, tc.resourceType, tc.path); !got.Equals(tc.want) {
			t.Errorf("%s %v: got %#v, want %#v", tc.resourceType, tc.path, got, tc.want)
		}
	}
	if got := attributeType(nil, "aws_db_instance", []string{"password"}); got != cty.NilType {
		t.Errorf("without schemas: got %#v", got)
	}
}

func TestVariablesFile(t *testing.T) {
	t.Parallel()
	secrets := []Secret{
		{Variable: "a_secrets", Address: "aws_x.a", Attribute: "replica.secrets", schemaPath: []string{"replica", "secrets"}},
		{Variable: "a_token", Address: "aws_x.a", Attribute: "token", schemaPath: []string{"token"}},
	}
	schemas := &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
		"registry.terraform.io/hashicorp/x": {ResourceSchemas: map[string]*tfjson.Schema{
			"aws_x": {Block: &tfjson.SchemaBlock{NestedBlocks: map[string]*tfjson.SchemaBlockType{"replica": {Block: &tfjson.SchemaBlock{
				Attributes: map[string]*tfjson.SchemaAttribute{"secrets": {AttributeType: cty.Map(cty.String)}},
			}}}}},
		}},
	}}

	got, err := variablesFile(secrets, schemas)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"variable \"a_secrets\" {\n",
		"  type        = map(string)\n",
		// No type for attributes the schemas don't describe.
		"variable \"a_token\" {\n  description = \"token of aws_x.a. Terraform doesn't write",
		"  sensitive   = true\n}\n",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Count(string(got), "type ") != 1 {
		t.Errorf("want one type constraint:\n%s", got)
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
			if check := result.Gate.check(CheckSecrets); !check.Passed {
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

// valuesAt reads an attribute of an imported object, through every block
// of a nested block type, whatever their order; allUnset says whether all
// of what it found is null or empty.
func TestValuesAt(t *testing.T) {
	t.Parallel()
	object := map[string]any{
		"password": nil,
		"key_raw":  "",
		"key_rsa":  []any{map[string]any{"value": ""}, map[string]any{"value": nil}},
		"replica":  []any{map[string]any{"secret": nil}, map[string]any{"secret": "s"}},
		"restore":  []any{map[string]any{"token": nil}},
	}
	for _, tc := range []struct {
		path []string
		null bool
	}{
		{[]string{"password"}, true},
		{[]string{"key_raw"}, true},
		{[]string{"key_rsa", "value"}, true},
		{[]string{"restore", "token"}, true},
		{[]string{"replica", "secret"}, false},
		{[]string{"missing"}, false},
		{[]string{"restore", "missing"}, false},
	} {
		if got := allUnset(valuesAt(object, tc.path)); got != tc.null {
			t.Errorf("%v: allUnset %v, want %v", tc.path, got, tc.null)
		}
	}
}

func TestAboutSecret(t *testing.T) {
	t.Parallel()
	secrets := []secretAttribute{{address: "aws_ssm_parameter.p", path: "value", schemaPath: []string{"value"}, rng: hcl.Range{Start: hcl.Pos{Line: 4}, End: hcl.Pos{Line: 4}}}}
	for _, c := range []struct {
		d    tfjson.Diagnostic
		want bool
	}{
		{errorAt(4, "Invalid value", ""), true},
		{errorAt(2, "Invalid combination of arguments", `"value": one of insecure_value,value,value_wo must be specified`), true},
		{tfjson.Diagnostic{Severity: tfjson.DiagnosticSeverityError, Summary: "Missing required argument", Detail: `The argument "value" is required, but no definition was found.`}, true},
		// Another attribute's error that happens to say value.
		{errorAt(3, "Invalid value", `expected value of tier to be one of ["Standard" "Advanced"]`), false},
	} {
		if got := aboutSecret(c.d, secrets); got != c.want {
			t.Errorf("%s: %s: got %v, want %v", c.d.Summary, c.d.Detail, got, c.want)
		}
	}
}
