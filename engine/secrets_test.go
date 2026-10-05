// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
)

const secretsConfig = `resource "aws_db_instance" "tfer--main" {
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

resource "aws_db_instance" "tfer-main" {
  password = null # sensitive
}
`

func TestFindSecrets(t *testing.T) {
	f := writeConfig(t, t.TempDir(), GeneratedFileName, secretsConfig)

	found := findSecrets(f)

	var got []string
	for _, addr := range []string{"aws_db_instance.tfer--main", "aws_db_instance.tfer-main"} {
		for _, s := range found[addr] {
			got = append(got, addr+" "+s.path+" "+strings.Join(s.schemaPath, "/"))
		}
	}
	want := []string{
		"aws_db_instance.tfer--main password password",
		"aws_db_instance.tfer--main restore.token restore/token",
		"aws_db_instance.tfer--main replica[0].secret replica/secret",
		"aws_db_instance.tfer--main replica[1].secret replica/secret",
		"aws_db_instance.tfer-main password password",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSecretsToVariables(t *testing.T) {
	f := writeConfig(t, t.TempDir(), GeneratedFileName, secretsConfig)

	secrets := secretsToVariables(f, findSecrets(f), nil)

	var names []string
	for _, s := range secrets {
		names = append(names, s.Variable)
	}
	// tfer--main and tfer-main both become tfer_main.
	want := []string{
		"aws_db_instance_tfer_main_password",
		"aws_db_instance_tfer_main_restore_token",
		"aws_db_instance_tfer_main_replica_0_secret",
		"aws_db_instance_tfer_main_replica_1_secret",
		"aws_db_instance_tfer_main_password_2",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("got %q, want %q", names, want)
	}
	if err := f.save(); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, filepath.Dir(f.path), GeneratedFileName)
	for _, line := range []string{
		"password   = var.aws_db_instance_tfer_main_password # sensitive",
		"secret = var.aws_db_instance_tfer_main_replica_1_secret # sensitive",
		"password = var.aws_db_instance_tfer_main_password_2 # sensitive",
		"username   = null\n",
		`note       = "x" # sensitive`,
	} {
		if !strings.Contains(got, line) {
			t.Errorf("missing %q in:\n%s", line, got)
		}
	}
}

func TestAttributeType(t *testing.T) {
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
