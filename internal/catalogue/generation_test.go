// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package catalogue

import (
	"reflect"
	"strings"
	"testing"
)

// ForType finds a type's built-in catalogue by its provider's prefix.
func TestForType(t *testing.T) {
	t.Parallel()
	for typ, want := range map[string]string{"aws_s3_bucket": "aws", "azapi_resource": "azapi", "google_project": "google", "nope_widget": "nope"} {
		c := ForType(typ)
		if c.Provider() != want {
			t.Errorf("%s: got the %s catalogue, want %s's", typ, c.Provider(), want)
		}
	}
	if types := ForType("nope_widget").Types(); len(types) != 0 {
		t.Errorf("a provider without a catalogue: got %v", types)
	}
}

// The engine reads these fields of the built-in catalogue alone, so an
// overlay that sets one is an error rather than a change that does
// nothing.
func TestOverlaysCantSetBuiltinOnlyFields(t *testing.T) {
	t.Parallel()
	for field, entry := range map[string]string{
		"secret_when": "secret_when: { argument: value, when: type, in: [SecureString] }",
	} {
		for _, typ := range []string{"aws_ssm_parameter", "aws_widget"} {
			dir := t.TempDir()
			write(t, dir, "aws.yaml", "provider: aws\ntypes:\n  "+typ+":\n    "+entry+"\n")
			if _, err := Load("aws", dir); err == nil || !strings.Contains(err.Error(), field+": only the built-in catalogue sets it") {
				t.Errorf("%s in an overlay of %s: got %v", field, typ, err)
			}
		}
	}
}

// check rejects what the engine couldn't read.
func TestCheckRejectsGeneration(t *testing.T) {
	t.Parallel()
	for name, file := range map[string]string{
		"secret_when without in":   "provider: aws\ntypes:\n  aws_x:\n    secret_when: { argument: value, when: type }\n",
		"secret_when argument":     "provider: aws\ntypes:\n  aws_x:\n    secret_when: { argument: Value, when: type, in: [x] }\n",
		"every type's secret_when": "provider: aws\ntypes:\n  \"*\":\n    secret_when: { argument: value, when: type, in: [x] }\n",
	} {
		if _, err := load(t, map[string]string{"x.yaml": file}); err == nil {
			t.Errorf("%s: want an error for\n%s", name, file)
		}
	}
}

// The schema rejects what check rejects.
func TestSchemaRejectsGeneration(t *testing.T) {
	t.Parallel()
	schema := loadSchema(t)
	for name, file := range map[string]string{
		"secret_when without in": "provider: aws\ntypes:\n  aws_x:\n    secret_when: { argument: value, when: type }\n",
	} {
		if err := schema.Validate(yamlToJSON(t, []byte(file))); err == nil {
			t.Errorf("%s: the schema accepts\n%s", name, file)
		}
	}
}

func TestSecretWhen(t *testing.T) {
	t.Parallel()
	got, ok := MustBuiltin("aws").SecretWhen("aws_ssm_parameter")
	if want := (SecretWhen{Argument: "value", When: "type", In: []string{"SecureString"}}); !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("aws_ssm_parameter: got %+v, %v, want %+v", got, ok, want)
	}
	got.In[0] = "String"
	if again, _ := MustBuiltin("aws").SecretWhen("aws_ssm_parameter"); again.In[0] != "SecureString" {
		t.Error("changing what one caller got changed the built-in catalogue")
	}
	if _, ok := MustBuiltin("aws").SecretWhen("aws_s3_bucket"); ok {
		t.Error("aws_s3_bucket: want none")
	}
}
