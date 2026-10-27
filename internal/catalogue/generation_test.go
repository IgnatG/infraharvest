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
		"lookups":            "lookups: [{ arguments: [kms_key_id], match: x, data_source: { type: aws_kms_alias, label: ebs, attribute: target_key_arn } }]",
		"named_by":           "named_by: [{ attribute: dns_name, match: ['(?P<name>.+)'], id: \"{name}\" }]",
		"root_lookup":        "root_lookup: { attribute: arn, id: \"things/{name}\" }",
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
		"lookups match and names":     "provider: aws\ntypes:\n  aws_x:\n    lookups: [{ arguments: [a], match: x, names: { x: y }, data_source: { type: aws_y, label: l, attribute: id } }]\n",
		"lookups neither":             "provider: aws\ntypes:\n  aws_x:\n    lookups: [{ arguments: [a], data_source: { type: aws_y, label: l, attribute: id } }]\n",
		"lookups arguments":           "provider: aws\ntypes:\n  aws_x:\n    lookups: [{ arguments: [], match: x, data_source: { type: aws_y, label: l, attribute: id } }]\n",
		"lookups attribute":           "provider: aws\ntypes:\n  aws_x:\n    lookups: [{ arguments: [a], match: x, data_source: { type: aws_y, label: l } }]\n",
		"lookups {name} of a match":   "provider: aws\ntypes:\n  aws_x:\n    lookups: [{ arguments: [a], match: x, data_source: { type: aws_y, label: \"{name}\", attribute: id } }]\n",
		"lookups other field":         "provider: aws\ntypes:\n  aws_x:\n    lookups: [{ arguments: [a], names: { x: y }, data_source: { type: aws_y, label: \"{id}\", attribute: id } }]\n",
		"lookups pattern":             "provider: aws\ntypes:\n  aws_x:\n    lookups: [{ arguments: [a], match: \"(\", data_source: { type: aws_y, label: l, attribute: id } }]\n",
		"named_by group":              "provider: aws\ntypes:\n  aws_x:\n    named_by: [{ attribute: dns_name, match: ['(?P<other>.+)'], id: \"{name}\" }]\n",
		"named_by id":                 "provider: aws\ntypes:\n  aws_x:\n    named_by: [{ attribute: dns_name, match: ['(?P<name>.+)'], id: \"{name|trim_prefix:x}\" }]\n",
		"named_by match":              "provider: aws\ntypes:\n  aws_x:\n    named_by: [{ attribute: dns_name, match: [], id: \"{name}\" }]\n",
		"root_lookup fields":          "provider: google\ntypes:\n  google_x:\n    root_lookup: { attribute: self_link, id: \"networks\" }\n",
		"every type's root_lookup":    "provider: google\ntypes:\n  \"*\":\n    root_lookup: { attribute: self_link, id: \"{name}\" }\n",
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
		"lookups match and names":     "provider: aws\ntypes:\n  aws_x:\n    lookups: [{ arguments: [a], match: x, names: { x: y }, data_source: { type: aws_y, label: l, attribute: id } }]\n",
		"lookups attribute":           "provider: aws\ntypes:\n  aws_x:\n    lookups: [{ arguments: [a], match: x, data_source: { type: aws_y, label: l } }]\n",
		"named_by group":              "provider: aws\ntypes:\n  aws_x:\n    named_by: [{ attribute: dns_name, match: ['.+'], id: \"{name}\" }]\n",
		"root_lookup fields":          "provider: google\ntypes:\n  google_x:\n    root_lookup: { attribute: self_link, id: \"networks\" }\n",
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

// The data sources of what AWS and Azure define: a CloudFront policy AWS
// manages, by its name, the AWS-managed key of EBS encryption, and, for
// every azurerm type, the tenant and the subscription the provider signs
// in to; other values stay.
func TestLookup(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		typ, argument, value string
		want                 DataLookup
	}{
		{"aws_cloudfront_distribution", "cache_policy_id", "4135ea2d-6df8-44a3-9df3-4b5a84be39ad", DataLookup{Type: "aws_cloudfront_cache_policy", Label: "CachingDisabled", Attribute: "id", Arguments: [][2]string{{"name", "Managed-CachingDisabled"}}}},
		{"aws_ebs_volume", "kms_key_id", "arn:aws:kms:eu-west-2:123456789012:key/1234abcd-12ab-34cd-56ef-1234567890ab", DataLookup{Type: "aws_kms_alias", Label: "ebs", Attribute: "target_key_arn", Arguments: [][2]string{{"name", "alias/aws/ebs"}}}},
		{"azurerm_key_vault", "tenant_id", "11111111-2222-3333-4444-555555555555", DataLookup{Type: "azurerm_client_config", Label: "current", Attribute: "tenant_id"}},
		{"azurerm_role_definition", "assignable_scopes", "/subscriptions/11111111-2222-3333-4444-555555555555", DataLookup{Type: "azurerm_subscription", Label: "current", Attribute: "id"}},
	} {
		if got, ok := ForType(c.typ).Lookup(c.typ, c.argument, c.value); !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s.%s = %s: got %+v, %v, want %+v", c.typ, c.argument, c.value, got, ok, c.want)
		}
	}
	for _, c := range [][3]string{
		{"aws_cloudfront_distribution", "cache_policy_id", "00000000-0000-0000-0000-000000000000"},
		{"aws_rds_cluster", "kms_key_id", "arn:aws:kms:eu-west-2:123456789012:key/1234abcd-12ab-34cd-56ef-1234567890ab"},
		{"aws_instance", "tenant_id", "11111111-2222-3333-4444-555555555555"},
		{"azurerm_role_assignment", "scope", "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/app"},
	} {
		if got, ok := ForType(c[0]).Lookup(c[0], c[1], c[2]); ok {
			t.Errorf("%s.%s = %s: got %+v, want none", c[0], c[1], c[2], got)
		}
	}
}

// A load balancer's DNS name names it by its name and region, a Classic
// one's by its name alone, a bucket's regional domain name the bucket,
// and a self link, on either host, the Google resource whose ID it ends
// in.
func TestNamedBy(t *testing.T) {
	t.Parallel()
	const alb = "arn:aws:elasticloadbalancing:eu-west-2:123456789012:loadbalancer/app/web/50dc6c495c0c9188"
	for _, c := range []struct {
		typ, value, attribute string
		names, others         []string
	}{
		{"aws_lb", "web-1234567890.eu-west-2.elb.amazonaws.com", "dns_name", []string{alb}, []string{
			"arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/web/50dc6c495c0c9188",
			"arn:aws:elasticloadbalancing:eu-west-2:123456789012:loadbalancer/app/api/50dc6c495c0c9188",
		}},
		{"aws_alb", "internal-web-0123456789abcdef.elb.eu-west-2.amazonaws.com", "dns_name", []string{alb}, nil},
		{"aws_elb", "web-1234567890.us-east-1.elb.amazonaws.com", "dns_name", []string{"web"}, []string{"api"}},
		{"aws_s3_bucket", "assets.s3.eu-west-2.amazonaws.com", "bucket_regional_domain_name", []string{"assets"}, []string{"logs"}},
		{"google_compute_subnetwork", "https://compute.googleapis.com/compute/v1/projects/p/regions/r/subnetworks/s", "self_link", []string{"projects/p/regions/r/subnetworks/s"}, []string{"projects/p/regions/r/subnetworks/t"}},
		{"google_compute_network", "https://www.googleapis.com/compute/beta/projects/p/global/networks/n", "self_link", []string{"projects/p/global/networks/n"}, nil},
	} {
		namings := ForType(c.typ).NamedBy(c.typ, c.value)
		if len(namings) != 1 || namings[0].Attribute != c.attribute {
			t.Errorf("%s by %s: got %+v, want one naming of %s", c.typ, c.value, namings, c.attribute)
			continue
		}
		for _, id := range c.names {
			if !namings[0].Names(id) {
				t.Errorf("%s by %s: doesn't name %s", c.typ, c.value, id)
			}
		}
		for _, id := range c.others {
			if namings[0].Names(id) {
				t.Errorf("%s by %s: names %s", c.typ, c.value, id)
			}
		}
	}
	for typ, value := range map[string]string{"aws_s3_bucket": "web-1234567890.eu-west-2.elb.amazonaws.com", "aws_lb": "assets.s3.eu-west-2.amazonaws.com", "aws_sqs_queue": "web-1234567890.eu-west-2.elb.amazonaws.com"} {
		if namings := ForType(typ).NamedBy(typ, value); len(namings) != 0 {
			t.Errorf("%s by %s: got %+v, want none", typ, value, namings)
		}
	}
}

// A Google network another root manages is read by its project and name,
// labelled by its name; its other attributes, and other types, aren't.
func TestRootLookup(t *testing.T) {
	t.Parallel()
	got, ok := ForType("google_compute_network").RootLookup("google_compute_network", "self_link", "projects/p/global/networks/vpc")
	want := DataLookup{Type: "google_compute_network", Label: "vpc", Attribute: "self_link", Arguments: [][2]string{{"name", "vpc"}, {"project", "p"}}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, %v, want %+v", got, ok, want)
	}
	for _, c := range [][3]string{
		{"google_compute_network", "id", "projects/p/global/networks/vpc"},
		{"google_compute_network", "self_link", "projects/p/regions/r/networks/vpc"},
		{"google_compute_subnetwork", "self_link", "projects/p/regions/r/subnetworks/s"},
	} {
		if got, ok := ForType(c[0]).RootLookup(c[0], c[1], c[2]); ok {
			t.Errorf("%v: got %+v, want none", c, got)
		}
	}
}
