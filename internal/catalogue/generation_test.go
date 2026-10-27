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
		"secret_when":        "secret_when: { argument: value, when: type, in: [SecureString] }",
		"blocks_as_list":     "blocks_as_list: { block: subnet_mapping, attribute: subnet_id, argument: subnets }",
		"principal":          "principal: { argument: principal_id, directory: Entra ID, note: n }",
		"module_references":  "module_references: { attribute: project_id, arguments: [project_id] }",
		"container":          "container: { location: true }",
		"derived_identity":   "derived_identity: \"{family}:{revision}\"",
		"unstable_id_suffix": "unstable_id_suffix: \"?api-version=\"",
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
		"secret_when without in":      "provider: aws\ntypes:\n  aws_x:\n    secret_when: { argument: value, when: type }\n",
		"secret_when argument":        "provider: aws\ntypes:\n  aws_x:\n    secret_when: { argument: Value, when: type, in: [x] }\n",
		"every type's secret_when":    "provider: aws\ntypes:\n  \"*\":\n    secret_when: { argument: value, when: type, in: [x] }\n",
		"blocks_as_list argument":     "provider: aws\ntypes:\n  aws_x:\n    blocks_as_list: { block: subnet_mapping, attribute: subnet_id }\n",
		"every type's blocks":         "provider: aws\ntypes:\n  \"*\":\n    blocks_as_list: { block: a, attribute: b, argument: c }\n",
		"principal note":              "provider: azurerm\ntypes:\n  azurerm_x:\n    principal: { argument: principal_id, directory: Entra ID }\n",
		"principal kinds":             "provider: azurerm\ntypes:\n  azurerm_x:\n    principal: { argument: principal_id, kind_argument: principal_type, directory: Entra ID, note: n }\n",
		"module_references arguments": "provider: google\ntypes:\n  google_x:\n    module_references: { attribute: project_id, arguments: [] }\n",
		"derived_identity filter":     "provider: aws\ntypes:\n  aws_x:\n    derived_identity: \"{family|trim_prefix:x}\"\n",
		"derived_identity fields":     "provider: aws\ntypes:\n  aws_x:\n    derived_identity: \"family\"\n",
		"every type's container":      "provider: aws\ntypes:\n  \"*\":\n    container: {}\n",
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
		"secret_when without in":      "provider: aws\ntypes:\n  aws_x:\n    secret_when: { argument: value, when: type }\n",
		"derived_identity filter":     "provider: aws\ntypes:\n  aws_x:\n    derived_identity: \"{family|trim_prefix:x}\"\n",
		"principal kinds":             "provider: azurerm\ntypes:\n  azurerm_x:\n    principal: { argument: principal_id, kind_argument: principal_type, directory: Entra ID, note: n }\n",
		"blocks_as_list argument":     "provider: aws\ntypes:\n  aws_x:\n    blocks_as_list: { block: subnet_mapping, attribute: subnet_id }\n",
		"module_references arguments": "provider: google\ntypes:\n  google_x:\n    module_references: { attribute: project_id, arguments: [] }\n",
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

// A load balancer's subnet_mapping blocks, under either of its type's
// names.
func TestBlocksAsList(t *testing.T) {
	t.Parallel()
	want := BlocksAsList{Block: "subnet_mapping", Attribute: "subnet_id", Argument: "subnets"}
	for _, typ := range []string{"aws_lb", "aws_alb"} {
		if got, ok := ForType(typ).BlocksAsList(typ); !ok || got != want {
			t.Errorf("%s: got %+v, %v, want %+v", typ, got, ok, want)
		}
	}
	if _, ok := ForType("aws_subnet").BlocksAsList("aws_subnet"); ok {
		t.Error("aws_subnet: want none")
	}
}

// An azurerm role assignment's principal is an Entra ID object, of the
// kind its principal_type says.
func TestPrincipal(t *testing.T) {
	t.Parallel()
	got, ok := ForType("azurerm_role_assignment").Principal("azurerm_role_assignment")
	if !ok || got.Argument != "principal_id" || got.KindArgument != "principal_type" || got.Kinds["ServicePrincipal"] != "service principal" || got.Directory != "Entra ID" {
		t.Fatalf("got %+v, %v", got, ok)
	}
	got.Kinds["User"] = "nobody"
	if again, _ := ForType("azurerm_role_assignment").Principal("azurerm_role_assignment"); again.Kinds["User"] != "user" {
		t.Error("changing what one caller got changed the built-in catalogue")
	}
}

// Module calls name a Google project by its project ID.
func TestModuleReferences(t *testing.T) {
	t.Parallel()
	got, ok := ForType("google_project").ModuleReferences("google_project")
	if !ok || got.Attribute != "project_id" || !reflect.DeepEqual(got.Arguments, []string{"project", "project_id", "projects"}) {
		t.Errorf("google_project: got %+v, %v", got, ok)
	}
}

// The types whose resources hold others, and those whose location what is
// in them shares.
func TestContainer(t *testing.T) {
	t.Parallel()
	for typ, location := range map[string]bool{"azurerm_resource_group": true, "google_project": false, "google_folder": false} {
		if got, ok := ForType(typ).Container(typ); !ok || got.Location != location {
			t.Errorf("%s: got %+v, %v, want a container with location %v", typ, got, ok, location)
		}
	}
	if _, ok := ForType("azurerm_virtual_network").Container("azurerm_virtual_network"); ok {
		t.Error("azurerm_virtual_network: want none")
	}
}

// An ECS task definition is family:revision.
func TestDerivedIdentity(t *testing.T) {
	t.Parallel()
	got, ok := ForType("aws_ecs_task_definition").DerivedIdentity("aws_ecs_task_definition")
	want := []IdentityPart{{Attribute: "family"}, {Text: ":"}, {Attribute: "revision"}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, %v, want %+v", got, ok, want)
	}
}

// An azapi resource's ID is the same resource whatever its API version;
// the other providers' IDs have no part that changes.
func TestUnstableIDSuffix(t *testing.T) {
	t.Parallel()
	for typ, want := range map[string]string{"azapi_resource": "?api-version=", "azapi_update_resource": "?api-version=", "azurerm_resource_group": "", "aws_s3_bucket": ""} {
		if got := ForType(typ).UnstableIDSuffix(typ); got != want {
			t.Errorf("%s: got %q, want %q", typ, got, want)
		}
	}
}
