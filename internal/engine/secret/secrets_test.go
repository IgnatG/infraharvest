// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package secret

import (
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
	f := writeGenerated(t, t.TempDir(), secretsConfig)

	found := FindSensitive(f)

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
	f := writeGenerated(t, t.TempDir(), secretsConfig)

	secrets := ToVariables(f, FindSensitive(f), nil, nil)

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
	got := readGenerated(t, filepath.Dir(f.Path))
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

func TestVariablesFile(t *testing.T) {
	t.Parallel()
	secrets := []Variable{
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

	got, err := VariablesFile(secrets, schemas)
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

// errorAt is an error diagnostic at line of the generated configuration.
func errorAt(line int, summary, detail string) tfjson.Diagnostic {
	return tfjson.Diagnostic{
		Severity: tfjson.DiagnosticSeverityError,
		Summary:  summary,
		Detail:   detail,
		Range:    &tfjson.Range{Filename: generatedFile, Start: tfjson.Pos{Line: line}, End: tfjson.Pos{Line: line}},
	}
}

func TestAboutSecret(t *testing.T) {
	t.Parallel()
	secrets := []Sensitive{{address: "aws_ssm_parameter.p", path: "value", schemaPath: []string{"value"}, rng: hcl.Range{Start: hcl.Pos{Line: 4}, End: hcl.Pos{Line: 4}}}}
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
		if got := About(c.d, secrets); got != c.want {
			t.Errorf("%s: %s: got %v, want %v", c.d.Summary, c.d.Detail, got, c.want)
		}
	}
}

func TestPlaceholders(t *testing.T) {
	t.Parallel()
	vars := Placeholders([]Variable{{Variable: "a"}, {Variable: "b", Type: cty.String}})
	if len(vars) != 2 {
		t.Errorf("want a value per secret, got %d", len(vars))
	}
}
