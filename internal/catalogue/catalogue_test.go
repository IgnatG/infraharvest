// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package catalogue

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/hashicorp/go-version"
	"go.yaml.in/yaml/v3"
)

func TestBuiltinLoads(t *testing.T) {
	t.Parallel()
	providers, err := Providers()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(providers, []string{"aws", "azapi", "azurerm", "google"}) {
		t.Errorf("providers: got %v", providers)
	}
	aws := MustBuiltin("aws")
	if len(aws.Types()) < 100 {
		t.Errorf("aws: %d types, want the ARN, Cloud Control, network and identity types", len(aws.Types()))
	}
	if e, ok := aws.Entry("aws_vpc"); !ok || e.Service != "vpc" || e.DataSource == nil {
		t.Errorf("aws_vpc: got %+v, want the vpc service's entry with a data source", e)
	}
	if got := aws.Omit()[AllTypes]; !reflect.DeepEqual(got, []string{"region", "tags_all", "*.tags_all"}) {
		t.Errorf("omit for every type: got %v", got)
	}
	if source := aws.Source("aws_vpc"); source != "data/aws/vpc.yaml" {
		t.Errorf("source: got %q", source)
	}
	empty, err := Builtin("nope")
	if err != nil || len(empty.Types()) != 0 {
		t.Errorf("a provider without a catalogue: got %v, %v", empty.Types(), err)
	}
}

// Every built-in file follows schema.json, which editors validate with.
func TestBuiltinFilesFollowTheSchema(t *testing.T) {
	t.Parallel()
	schema := loadSchema(t)
	err := fs.WalkDir(builtin, "data", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := fs.ReadFile(builtin, name)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(string(content), "# yaml-language-server: $schema=../../schema.json\n") {
			t.Errorf("%s: start with the schema comment, for editors", name)
		}
		if err := schema.Validate(yamlToJSON(t, content)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The schema rejects what Parse and check reject.
func TestSchemaRejects(t *testing.T) {
	t.Parallel()
	schema := loadSchema(t)
	for name, file := range map[string]string{
		"unknown field":    "provider: aws\ntypes:\n  aws_vpc:\n    imports: x\n",
		"no provider":      "types: {}\n",
		"template":         "provider: aws\ntypes:\n  aws_vpc:\n    import_id: vpc\n",
		"skip reason":      "provider: aws\ntypes:\n  aws_vpc:\n    skip:\n      - ids: [vpc-1]\n",
		"cloud control":    "provider: aws\ntypes:\n  aws_vpc:\n    cloud_control: { type: VPC, identifier: VpcId }\n",
		"data source type": "provider: aws\ntypes:\n  aws_vpc:\n    data_source: { type: Vpc, argument: id }\n",
		"inventory":        "provider: google\ntypes:\n  google_x:\n    inventory: { name: x }\n",
		"attribute prefix": "provider: aws\ntypes:\n  aws_vpc:\n    skip:\n      - reason: r\n        attribute_prefixes: { name: [] }\n",
		"attribute suffix": "provider: aws\ntypes:\n  aws_vpc:\n    skip:\n      - reason: r\n        attribute_suffixes: { name: [] }\n",
		"omitted value":    "provider: aws\ntypes:\n  aws_vpc:\n    omit: [\"=x\"]\n",
	} {
		if err := schema.Validate(yamlToJSON(t, []byte(file))); err == nil {
			t.Errorf("%s: the schema accepts\n%s", name, file)
		}
	}
}

func loadSchema(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	content, err := os.ReadFile("schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(content, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// yamlToJSON decodes YAML into the values encoding/json would decode the
// same document into, which is what the schema validates.
func yamlToJSON(t *testing.T, content []byte) any {
	t.Helper()
	var value any
	if err := yaml.Unmarshal(content, &value); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestTemplates(t *testing.T) {
	t.Parallel()
	attributes := map[string]string{"subnet_id": "subnet-1", "route_table_id": "rtb-1", "arn": "arn:aws:ecs:us-east-1:1:cluster/apps"}
	for source, want := range map[string]string{
		"{subnet_id}/{route_table_id}":   "subnet-1/rtb-1",
		"{id}":                           "listed",
		"{arn|after_last:/}":             "apps",
		"{arn|before_last:/}":            "arn:aws:ecs:us-east-1:1:cluster",
		"{arn|trim_prefix:arn:aws:ecs:}": "us-east-1:1:cluster/apps",
		"{id|trim_suffix:ed}":            "list",
		"{id|after_last:#}":              "listed",
		"{id|before_last:#}":             "listed",
		"prefix-{id}":                    "prefix-listed",
	} {
		tmpl, err := ParseTemplate(source)
		if err != nil {
			t.Errorf("%s: %v", source, err)
			continue
		}
		if got, ok := tmpl.Render("listed", attributes); !ok || got != want {
			t.Errorf("%s: got %q, %v; want %q", source, got, ok, want)
		}
	}
	tmpl, _ := ParseTemplate("{gateway_id}/{route_table_id}")
	if got, ok := tmpl.Render("listed", attributes); ok {
		t.Errorf("a field without a value: got %q, want no ID", got)
	}
	for _, source := range []string{"", "{}", "{id", "id}", "{id|upper}", "{id|after_last}", "{a b}", "x{id}}"} {
		if _, err := ParseTemplate(source); err == nil {
			t.Errorf("%q: want an error", source)
		}
	}
}

func TestImportID(t *testing.T) {
	t.Parallel()
	c := parseCatalogue(t, `
provider: aws
types:
  aws_route_table_association:
    import_id:
      - "{subnet_id}/{route_table_id}"
      - "{gateway_id}/{route_table_id}"
  aws_main_route_table_association:
    not_importable: the VPC records it
  aws_vpc:
    data_source: { type: aws_vpc, argument: id }
`)
	tests := []struct {
		typ, id    string
		attributes map[string]string
		want       string
		ok         bool
	}{
		{"aws_route_table_association", "rtbassoc-1", map[string]string{"subnet_id": "subnet-1", "route_table_id": "rtb-1"}, "subnet-1/rtb-1", true},
		{"aws_route_table_association", "rtbassoc-2", map[string]string{"gateway_id": "igw-1", "route_table_id": "rtb-1"}, "igw-1/rtb-1", true},
		{"aws_route_table_association", "rtbassoc-3", map[string]string{"route_table_id": "rtb-1"}, "", false},
		{"aws_main_route_table_association", "rtbassoc-4", nil, "", false},
		{"aws_vpc", "vpc-1", nil, "vpc-1", true},
		{"aws_sqs_queue", "https://sqs/1/q", nil, "https://sqs/1/q", true},
	}
	for _, tt := range tests {
		got, ok, reason := c.ImportID(tt.typ, tt.id, tt.attributes)
		if got != tt.want || ok != tt.ok || ok != (reason == "") {
			t.Errorf("%s %s: got %q, %v, %q; want %q, %v", tt.typ, tt.id, got, ok, reason, tt.want, tt.ok)
		}
	}
}

func TestSkipped(t *testing.T) {
	t.Parallel()
	c := parseCatalogue(t, `
provider: aws
types:
  aws_iam_role:
    skip:
      - reason: service-linked
        id_prefixes: [AWSServiceRoleFor]
  aws_default_network_acl:
    skip:
      - reason: default
        all: true
  aws_instance:
    skip:
      - reason: EKS node
        id_prefixes: [i-]
        attributes: { "tags.eks:cluster-name": prod }
`)
	for _, tt := range []struct {
		typ, id    string
		attributes map[string]string
		want       string
	}{
		{"aws_iam_role", "AWSServiceRoleForECS", nil, "service-linked"},
		{"aws_iam_role", "app", nil, ""},
		{"aws_default_network_acl", "acl-1", nil, "default"},
		{"aws_instance", "i-1", map[string]string{"tags.eks:cluster-name": "prod"}, "EKS node"},
		{"aws_instance", "i-2", map[string]string{"tags.eks:cluster-name": "dev"}, ""},
		{"aws_vpc", "vpc-1", nil, ""},
	} {
		if got := c.Skipped(tt.typ, tt.id, tt.attributes); got != tt.want {
			t.Errorf("%s %s: got %q, want %q", tt.typ, tt.id, got, tt.want)
		}
	}
}

func TestCheckRejects(t *testing.T) {
	t.Parallel()
	for name, file := range map[string]string{
		"another provider's type": "provider: aws\ntypes:\n  google_x: {}\n",
		"exclusive import fields": "provider: aws\ntypes:\n  aws_x:\n    import_id: \"{id}\"\n    not_importable: no\n",
		"template without fields": "provider: aws\ntypes:\n  aws_x:\n    import_id: x\n",
		"skip without a reason":   "provider: aws\ntypes:\n  aws_x:\n    skip:\n      - ids: [a]\n",
		"skip matching all":       "provider: aws\ntypes:\n  aws_x:\n    skip:\n      - reason: r\n",
		"all with conditions":     "provider: aws\ntypes:\n  aws_x:\n    skip:\n      - reason: r\n        all: true\n        ids: [a]\n",
		"unknown child":           "provider: aws\ntypes:\n  aws_x:\n    children: [aws_y]\n",
		"own child":               "provider: aws\ntypes:\n  aws_x:\n    children: [aws_x]\n",
		"every type's inventory":  "provider: google\ntypes:\n  \"*\":\n    inventory: { type: compute.googleapis.com/Network }\n",
		"inventory type":          "provider: google\ntypes:\n  google_x:\n    inventory: { type: Network }\n",
		"inventory name":          "provider: google\ntypes:\n  google_x:\n    inventory: { type: compute.googleapis.com/Network, name: \"//x/[\" }\n",
		"empty attribute prefix":  "provider: aws\ntypes:\n  aws_x:\n    skip:\n      - reason: r\n        attribute_prefixes: { name: [\"\"] }\n",
		"empty attribute suffix":  "provider: aws\ntypes:\n  aws_x:\n    skip:\n      - reason: r\n        attribute_suffixes: { name: [\"\"] }\n",
		"inventory types apart":   "provider: google\ntypes:\n  google_x:\n    inventory: { type: compute.googleapis.com/Disk, name: \"//compute.googleapis.com/projects/*/zones/*/disks/*\" }\n  google_y:\n    inventory: { type: compute.googleapis.com/disk }\n",
		"repeated argument":       "provider: aws\ntypes:\n  aws_x:\n    omit: [a, a]\n",
		"default without a value": "provider: google\ntypes:\n  google_x:\n    defaults: [storage_class]\n",
		"default's path":          "provider: google\ntypes:\n  google_x:\n    defaults: [\"a..b=1\"]\n",
		"repeated default":        "provider: google\ntypes:\n  google_x:\n    defaults: [\"a=1\", \"a=2\"]\n",
		"every type's defaults":   "provider: google\ntypes:\n  \"*\":\n    defaults: [\"a=1\"]\n",
		"omitted value's name":    "provider: aws\ntypes:\n  aws_x:\n    omit: [\"=x\"]\n",
		"omitted condition":       "provider: aws\ntypes:\n  aws_x:\n    omit: [\"a when =x\"]\n",
		"omitted path":            "provider: aws\ntypes:\n  aws_x:\n    omit: [\"a..b\"]\n",
		"lifecycle reason":        "provider: aws\ntypes:\n  aws_x:\n    lifecycle: { create_before_destroy: true }\n",
		"every type's lifecycle":  "provider: aws\ntypes:\n  \"*\":\n    lifecycle: { create_before_destroy: true, reason: r }\n",
		"cloud control service":   "provider: aws\ntypes:\n  aws_x:\n    cloud_control: { type: \"AWS::X::Y\", identifier: Id }\n",
		"unknown field":           "provider: aws\ntypes:\n  aws_x:\n    imports: x\n",
		"bad filter":              "provider: aws\ntypes:\n  aws_x:\n    import_id: \"{id|upper:x}\"\n",
		"placeholder without set": "provider: aws\ntypes:\n  aws_x:\n    placeholder: { one_of: [a], ignore: [a], reason: r }\n",
		"placeholder reason":      "provider: aws\ntypes:\n  aws_x:\n    placeholder: { one_of: [a], set: { a: x }, ignore: [a] }\n",
		"placeholder argument":    "provider: aws\ntypes:\n  aws_x:\n    placeholder: { one_of: [a], set: { Foo-Bar: x }, ignore: [a], reason: r }\n",
		"placeholder empty value": "provider: aws\ntypes:\n  aws_x:\n    placeholder: { one_of: [a], set: { a: \"\" }, ignore: [a], reason: r }\n",
		"placeholder reference":   "provider: aws\ntypes:\n  aws_x:\n    placeholder: { one_of: [a], set: { a: \"x/{Name}.zip\" }, ignore: [a], reason: r }\n",
		"data source provider":    "provider: aws\ntypes:\n  aws_x:\n    data_source: { type: google_x, argument: id }\n",
		"remove outside overlays": "provider: aws\ntypes:\n  aws_x:\n    remove: [placeholder]\n",
	} {
		if _, err := load(t, map[string]string{"x.yaml": file}); err == nil {
			t.Errorf("%s: want an error for\n%s", name, file)
		}
	}
	if _, err := load(t, map[string]string{"a.yaml": "provider: aws\ntypes:\n  aws_x: {}\n", "b.yaml": "provider: aws\ntypes:\n  aws_x: {}\n"}); err == nil || !strings.Contains(err.Error(), "aws_x is in") {
		t.Errorf("a type in two files: got %v", err)
	}
	// Defaults name nested arguments by their path.
	if c, err := load(t, map[string]string{"x.yaml": "provider: google\ntypes:\n  google_x:\n    defaults: [\"storage_class=STANDARD\", \"soft_delete_policy.retention_duration_seconds=604800\"]\n"}); err != nil {
		t.Errorf("defaults: %v", err)
	} else if got := c["google"].Defaults()["google_x"]; len(got) != 2 {
		t.Errorf("defaults: got %v", got)
	}
	// name=value omits an argument only when it is set to value.
	if _, err := load(t, map[string]string{"x.yaml": "provider: aws\ntypes:\n  aws_x:\n    omit: [a, \"b=$c\", \"d=true\"]\n"}); err != nil {
		t.Errorf("omit with values: %v", err)
	}
	// Nested arguments, conditions, and conditions on what the lister
	// recorded.
	if _, err := load(t, map[string]string{"x.yaml": "provider: aws\ntypes:\n  aws_x:\n    omit: [\"a.b=1 when c and !d.e and f=g\", \"h when @i=default\"]\n"}); err != nil {
		t.Errorf("omit with paths and conditions: %v", err)
	}
}

// Overlay entries replace the fields they set, and add types.
func TestLoadOverlay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "aws.yaml", `
provider: aws
types:
  aws_route_table_association:
    import_id: "{id}"
  aws_iam_role:
    skip: []
  aws_widget:
    not_importable: no import yet
`)
	write(t, dir, "google.yaml", "provider: google\ntypes:\n  google_widget: {}\n")
	c, err := Load("aws", dir)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok, _ := c.ImportID("aws_route_table_association", "rtbassoc-1", map[string]string{"subnet_id": "subnet-1", "route_table_id": "rtb-1"}); id != "rtbassoc-1" || !ok {
		t.Errorf("overridden template: got %q, %v", id, ok)
	}
	if reason := c.Skipped("aws_iam_role", "AWSServiceRoleForECS", nil); reason != "" {
		t.Errorf("cleared skip rules: got %q", reason)
	}
	role, _ := c.Entry("aws_iam_role")
	if role.DataSource == nil || len(role.Omit) == 0 {
		t.Errorf("fields the overlay doesn't set: got %+v", role)
	}
	if _, ok, _ := c.ImportID("aws_widget", "w", nil); ok {
		t.Error("added type: want not importable")
	}
	if c.Source("aws_widget") != filepath.Join(dir, "aws.yaml") {
		t.Errorf("source: %s", c.Source("aws_widget"))
	}
	if _, ok := c.Entry("google_widget"); ok {
		t.Error("another provider's file applies")
	}
	if builtin := MustBuiltin("aws"); builtin.Skipped("aws_iam_role", "AWSServiceRoleForECS", nil) == "" {
		t.Error("the overlay changed the built-in catalogue")
	}

	write(t, dir, "bad.yaml", "provider: aws\ntypes:\n  aws_vpc:\n    import_id: x\n")
	if _, err := Load("aws", dir); err == nil {
		t.Error("an invalid overlay: want an error")
	}
	if _, err := Load("aws", filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing directory: want an error")
	}

	// A provider no catalogue has is most likely mistyped; a directory
	// without catalogue files, most likely the wrong one.
	other := t.TempDir()
	write(t, other, "aws.yml", "provider: aws\ntypes:\n  aws_widget:\n    not_importable: no import yet\n")
	c, err = Load("aws", other)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Entry("aws_widget"); !ok {
		t.Error("a .yml file: want its entries")
	}
	write(t, other, "typo.yaml", "provider: awss\ntypes: {}\n")
	if _, err := Load("aws", other); err == nil || !strings.Contains(err.Error(), `typo.yaml: unknown provider "awss"`) {
		t.Errorf("an unknown provider: want an error naming the file, got %v", err)
	}
	empty := t.TempDir()
	write(t, empty, "README.md", "notes")
	if _, err := Load("aws", empty); err == nil || !strings.Contains(err.Error(), "no .yaml or .yml files") {
		t.Errorf("a directory without catalogue files: want an error, got %v", err)
	}
}

// An overlay's remove clears built-in fields it can't clear by setting
// them, and is checked.
func TestLoadOverlayRemoves(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "aws.yaml", `
provider: aws
types:
  aws_lambda_function:
    remove: [placeholder]
  aws_iam_role:
    remove: [data_source, arn]
`)
	c, err := Load("aws", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Placeholders()["aws_lambda_function"]; ok {
		t.Error("removed placeholder: still there")
	}
	role, _ := c.Entry("aws_iam_role")
	if role.DataSource != nil || role.ARN != nil || len(role.Remove) != 0 {
		t.Errorf("removed data_source and arn: got %+v", role)
	}
	if builtin, _ := MustBuiltin("aws").Entry("aws_iam_role"); builtin.DataSource == nil {
		t.Error("the overlay changed the built-in catalogue")
	}

	for name, overlay := range map[string]string{
		"unknown field": "provider: aws\ntypes:\n  aws_iam_role:\n    remove: [omit]\n",
		"twice":         "provider: aws\ntypes:\n  aws_iam_role:\n    remove: [arn, arn]\n",
		"set too":       "provider: aws\ntypes:\n  aws_iam_role:\n    remove: [arn]\n    arn: { service: iam, resource_type: role }\n",
		"new type":      "provider: aws\ntypes:\n  aws_widget:\n    remove: [arn]\n",
	} {
		bad := t.TempDir()
		write(t, bad, "aws.yaml", overlay)
		if _, err := Load("aws", bad); err == nil || !strings.Contains(err.Error(), "remove") {
			t.Errorf("%s: want an error about remove, got %v", name, err)
		}
	}
}

func parseCatalogue(t *testing.T, file string) *Catalogue {
	t.Helper()
	catalogues, err := load(t, map[string]string{"x.yaml": file})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range catalogues {
		return c
	}
	t.Fatal("no catalogue")
	return nil
}

func load(t *testing.T, files map[string]string) (map[string]*Catalogue, error) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		write(t, dir, name, content)
	}
	return loadFS(os.DirFS(dir), ".", true, nil)
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Rules by attribute prefix, case-insensitive rules, and rules for every
// type, after the type's own.
func TestSkippedByPrefixCaseAndEveryType(t *testing.T) {
	t.Parallel()
	c := parseCatalogue(t, `
provider: azurerm
types:
  "*":
    skip:
      - reason: Network Watcher
        ignore_case: true
        attributes: { resource_group_name: NetworkWatcherRG }
      - reason: managed group
        attribute_prefixes: { resource_group_managed_by: [/subscriptions/] }
  azurerm_monitor_smart_detector_alert_rule:
    skip:
      - reason: Failure Anomalies
        ignore_case: true
        attribute_prefixes: { name: ["Failure Anomalies - "] }
  azurerm_network_watcher:
    skip:
      - reason: by name
        ids: [/X/NetworkWatcher_eastus]
`)
	for _, tt := range []struct {
		typ, id    string
		attributes map[string]string
		want       string
	}{
		{"azurerm_monitor_smart_detector_alert_rule", "/r", map[string]string{"name": "failure anomalies - app"}, "Failure Anomalies"},
		{"azurerm_monitor_smart_detector_alert_rule", "/r", map[string]string{"name": "slow requests"}, ""},
		{"azurerm_monitor_smart_detector_alert_rule", "/r", nil, ""},
		{"azurerm_virtual_network", "/v", map[string]string{"resource_group_name": "networkwatcherrg"}, "Network Watcher"},
		{"azurerm_virtual_network", "/v", map[string]string{"resource_group_name": "NetworkWatcherRG-2"}, ""},
		{"azurerm_lb", "/lb", map[string]string{"resource_group_managed_by": "/subscriptions/s/resourceGroups/rg/providers/Microsoft.ContainerService/managedClusters/aks"}, "managed group"},
		{"azurerm_lb", "/lb", map[string]string{"resource_group_managed_by": "/SUBSCRIPTIONS/s"}, ""},
		// The type's own rule comes first.
		{"azurerm_network_watcher", "/X/NetworkWatcher_eastus", map[string]string{"resource_group_name": "NetworkWatcherRG"}, "by name"},
		{"azurerm_network_watcher", "/x/networkwatcher_eastus", nil, ""},
	} {
		if got := c.Skipped(tt.typ, tt.id, tt.attributes); got != tt.want {
			t.Errorf("%s %s %v: got %q, want %q", tt.typ, tt.id, tt.attributes, got, tt.want)
		}
	}
}

// Rules by attribute suffix, whatever the case with ignore_case, with the
// other conditions they set.
func TestSkippedBySuffix(t *testing.T) {
	t.Parallel()
	c := parseCatalogue(t, `
provider: google
types:
  google_project_iam_member:
    skip:
      - reason: service agent role
        ignore_case: true
        attribute_suffixes: { role: [serviceagent] }
      - reason: default editor
        attributes: { role: roles/editor }
        attribute_suffixes: { member: ["-compute@developer.gserviceaccount.com", "@appspot.gserviceaccount.com"] }
`)
	for _, tt := range []struct {
		attributes map[string]string
		want       string
	}{
		{map[string]string{"role": "roles/pubsub.serviceAgent"}, "service agent role"},
		{map[string]string{"role": "roles/compute.instanceGroupManagerServiceAgent"}, "service agent role"},
		{map[string]string{"role": "roles/serviceAgentViewer"}, ""},
		{map[string]string{"role": "roles/editor", "member": "serviceAccount:123-compute@developer.gserviceaccount.com"}, "default editor"},
		{map[string]string{"role": "roles/editor", "member": "serviceAccount:acme@appspot.gserviceaccount.com"}, "default editor"},
		{map[string]string{"role": "roles/viewer", "member": "serviceAccount:123-compute@developer.gserviceaccount.com"}, ""},
		{map[string]string{"role": "roles/editor", "member": "serviceAccount:ci@acme.iam.gserviceaccount.com"}, ""},
		{map[string]string{"role": "roles/editor"}, ""},
	} {
		if got := c.Skipped("google_project_iam_member", "id", tt.attributes); got != tt.want {
			t.Errorf("%v: got %q, want %q", tt.attributes, got, tt.want)
		}
	}
}

func TestFromInventory(t *testing.T) {
	t.Parallel()
	c := parseCatalogue(t, `
provider: google
types:
  google_compute_network:
    inventory: { type: compute.googleapis.com/Network }
  google_compute_disk:
    inventory: { type: compute.googleapis.com/Disk, name: "//compute.googleapis.com/projects/*/zones/*/disks/*" }
  google_compute_region_disk:
    inventory: { type: compute.googleapis.com/Disk, name: "//compute.googleapis.com/projects/*/regions/*/disks/*" }
  google_storage_bucket: {}
`)
	if got := c.InventoryTypes(); !reflect.DeepEqual(got, []string{"compute.googleapis.com/Disk", "compute.googleapis.com/Network"}) {
		t.Errorf("inventory types: %v", got)
	}
	for _, tt := range []struct {
		assetType, name string
		want            []string
	}{
		{"compute.googleapis.com/Network", "//compute.googleapis.com/projects/p/global/networks/n", []string{"google_compute_network"}},
		{"compute.googleapis.com/network", "//compute.googleapis.com/projects/p/global/networks/n", []string{"google_compute_network"}},
		{"compute.googleapis.com/Disk", "//compute.googleapis.com/projects/p/zones/us-central1-a/disks/d", []string{"google_compute_disk"}},
		{"compute.googleapis.com/Disk", "//compute.googleapis.com/projects/P/Regions/us-central1/disks/d", []string{"google_compute_region_disk"}},
		// * stands for one segment.
		{"compute.googleapis.com/Disk", "//compute.googleapis.com/projects/p/zones/a/b/disks/d", nil},
		{"storage.googleapis.com/Bucket", "//storage.googleapis.com/b", nil},
	} {
		if got := c.FromInventory(tt.assetType, tt.name); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s %s: got %v, want %v", tt.assetType, tt.name, got, tt.want)
		}
	}
}

// A resource named as each built-in inventory entry's pattern says maps to
// that entry's type alone.
func TestBuiltinInventoryPatternsTellTypesApart(t *testing.T) {
	t.Parallel()
	providers, err := Providers()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range providers {
		c := MustBuiltin(provider)
		for _, typ := range c.Types() {
			e, _ := c.Entry(typ)
			if e.Inventory == nil {
				continue
			}
			name := strings.ReplaceAll(e.Inventory.Name, "*", "x")
			if got := c.FromInventory(e.Inventory.Type, name); !reflect.DeepEqual(got, []string{typ}) {
				t.Errorf("%s: %s %q maps to %v", c.Source(typ), e.Inventory.Type, name, got)
			}
		}
	}
}

// provider_versions adds what holds only for the provider versions a
// constraint accepts to what holds for every version; without a version,
// it adds nothing.
func TestProviderVersions(t *testing.T) {
	t.Parallel()
	c := parseCatalogue(t, `
provider: azurerm
types:
  azurerm_storage_account:
    omit: [a]
    state_only: [s]
    provider_versions:
      "< 5.0":
        omit: [allow_nested_items_to_be_public=true]
        defaults: [min_tls_version=TLS1_0]
      ">= 5.0":
        omit: [allow_nested_items_to_be_public=false]
        defaults: [min_tls_version=TLS1_2]
        state_only: [t]
  azurerm_log_analytics_workspace:
    provider_versions:
      ">= 5.0, < 6.0":
        omit: [internet_ingestion_enabled]
`)
	v4 := version.Must(version.NewVersion("4.81.0"))
	v5 := version.Must(version.NewVersion("5.10.0"))
	for _, tt := range []struct {
		name      string
		got, want []string
	}{
		{"omit without a version", c.Omit()["azurerm_storage_account"], []string{"a"}},
		{"omit for 4.81.0", c.OmitFor(v4)["azurerm_storage_account"], []string{"a", "allow_nested_items_to_be_public=true"}},
		{"omit for 5.10.0", c.OmitFor(v5)["azurerm_storage_account"], []string{"a", "allow_nested_items_to_be_public=false"}},
		{"defaults without a version", c.Defaults()["azurerm_storage_account"], nil},
		{"defaults for 4.81.0", c.DefaultsFor(v4)["azurerm_storage_account"], []string{"min_tls_version=TLS1_0"}},
		{"defaults for 5.10.0", c.DefaultsFor(v5)["azurerm_storage_account"], []string{"min_tls_version=TLS1_2"}},
		{"state_only without a version", c.StateOnly()["azurerm_storage_account"], []string{"s"}},
		{"state_only for 4.81.0", c.StateOnlyFor(v4)["azurerm_storage_account"], []string{"s"}},
		{"state_only for 5.10.0", c.StateOnlyFor(v5)["azurerm_storage_account"], []string{"s", "t"}},
		{"only versioned, without a version", c.Omit()["azurerm_log_analytics_workspace"], nil},
		{"only versioned, for 4.81.0", c.OmitFor(v4)["azurerm_log_analytics_workspace"], nil},
		{"only versioned, for 5.10.0", c.OmitFor(v5)["azurerm_log_analytics_workspace"], []string{"internet_ingestion_enabled"}},
	} {
		if !reflect.DeepEqual(tt.got, tt.want) {
			t.Errorf("%s: got %v, want %v", tt.name, tt.got, tt.want)
		}
	}

	dir := t.TempDir()
	write(t, dir, "azurerm.yaml", "provider: azurerm\ntypes:\n  azurerm_storage_account:\n    provider_versions:\n      \">= 6.0\":\n        omit: [b]\n")
	overlaid, err := Load("azurerm", dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := overlaid.OmitFor(v5)["azurerm_storage_account"]; slices.Contains(got, "allow_nested_items_to_be_public=false") {
		t.Errorf("an overlay's provider_versions replace the built-in ones; got %v", got)
	}
}

// The checks reject version constraints that don't parse, constraints
// that add nothing, and what every version has already.
func TestCheckRejectsProviderVersions(t *testing.T) {
	t.Parallel()
	for name, file := range map[string]string{
		"constraint":            "provider: aws\ntypes:\n  aws_x:\n    provider_versions:\n      five:\n        omit: [a]\n",
		"nothing added":         "provider: aws\ntypes:\n  aws_x:\n    provider_versions:\n      \">= 5.0\": {}\n",
		"every version's":       "provider: aws\ntypes:\n  aws_x:\n    omit: [a]\n    provider_versions:\n      \">= 5.0\":\n        omit: [a]\n",
		"argument name":         "provider: aws\ntypes:\n  aws_x:\n    provider_versions:\n      \">= 5.0\":\n        state_only: [A]\n",
		"omitted value name":    "provider: aws\ntypes:\n  aws_x:\n    provider_versions:\n      \">= 5.0\":\n        omit: [\"=x\"]\n",
		"unknown field":         "provider: aws\ntypes:\n  aws_x:\n    provider_versions:\n      \">= 5.0\":\n        skip: []\n",
		"defaults path":         "provider: aws\ntypes:\n  aws_x:\n    provider_versions:\n      \">= 5.0\":\n        defaults: [a]\n",
		"every type's defaults": "provider: aws\ntypes:\n  \"*\":\n    provider_versions:\n      \">= 5.0\":\n        defaults: [a=1]\n",
	} {
		if _, err := load(t, map[string]string{"x.yaml": file}); err == nil {
			t.Errorf("%s: want an error for\n%s", name, file)
		}
	}
	schema := loadSchema(t)
	for name, file := range map[string]string{
		"constraint":    "provider: aws\ntypes:\n  aws_x:\n    provider_versions:\n      five:\n        omit: [a]\n",
		"nothing added": "provider: aws\ntypes:\n  aws_x:\n    provider_versions:\n      \">= 5.0\": {}\n",
	} {
		if err := schema.Validate(yamlToJSON(t, []byte(file))); err == nil {
			t.Errorf("%s: the schema accepts\n%s", name, file)
		}
	}
	ok := "provider: aws\ntypes:\n  \"*\":\n    provider_versions:\n      \"~> 6.0\":\n        omit: [a]\n  aws_x:\n    provider_versions:\n      \">= 5.0, < 6.0\":\n        omit: [\"b=c\"]\n        state_only: [d]\n"
	if _, err := load(t, map[string]string{"x.yaml": ok}); err != nil {
		t.Errorf("valid provider_versions: %v", err)
	}
	if err := schema.Validate(yamlToJSON(t, []byte(ok))); err != nil {
		t.Errorf("the schema rejects valid provider_versions: %v", err)
	}
}

// keep_empty lists, per type, the arguments whose empty value is a
// setting; overlays replace the list.
func TestKeepEmpty(t *testing.T) {
	t.Parallel()
	c := parseCatalogue(t, "provider: aws\ntypes:\n  aws_default_route_table:\n    keep_empty: [route]\n  aws_vpc: {}\n")
	if got := c.KeepEmpty(); !reflect.DeepEqual(got, map[string][]string{"aws_default_route_table": {"route"}}) {
		t.Errorf("keep_empty: got %v", got)
	}
	if _, err := load(t, map[string]string{"x.yaml": "provider: aws\ntypes:\n  aws_x:\n    keep_empty: [Route]\n"}); err == nil {
		t.Error("keep_empty of a name that isn't an argument's: want an error")
	}
}

// An omit entry may name an argument of nested blocks, of any type for
// *.name.
func TestOmitInNestedBlocks(t *testing.T) {
	t.Parallel()
	ok := "provider: aws\ntypes:\n  aws_x:\n    omit: [root_block_device.tags_all, \"*.tags_all\", \"a.b.c=1\"]\n"
	if _, err := load(t, map[string]string{"x.yaml": ok}); err != nil {
		t.Errorf("nested omit entries: %v", err)
	}
	schema := loadSchema(t)
	if err := schema.Validate(yamlToJSON(t, []byte(ok))); err != nil {
		t.Errorf("the schema rejects nested omit entries: %v", err)
	}
	for _, entry := range []string{".a", "*", "a.*", "a..b", "A.b"} {
		file := "provider: aws\ntypes:\n  aws_x:\n    omit: [\"" + entry + "\"]\n"
		if _, err := load(t, map[string]string{"x.yaml": file}); err == nil {
			t.Errorf("omit %q: want an error", entry)
		}
		if err := schema.Validate(yamlToJSON(t, []byte(file))); err == nil {
			t.Errorf("omit %q: the schema accepts it", entry)
		}
	}
}

// The types the default selection leaves out whole are those with a skip
// rule for all of them.
func TestSkippedWhole(t *testing.T) {
	t.Parallel()
	got := MustBuiltin("aws").SkippedWhole()
	if !slices.Contains(got, "aws_iam_access_key") || slices.Contains(got, "aws_iam_role") {
		t.Errorf("got %v", got)
	}
	if !slices.IsSorted(got) {
		t.Errorf("not sorted: %v", got)
	}
}

// azapi's entries join the azurerm catalogue's, whose rules for every
// type stay its own; the azurerm catalogue itself is left as it was.
func TestWithAddsTheTypesOfAnotherProvider(t *testing.T) {
	t.Parallel()
	azurerm, azapi := MustBuiltin("azurerm"), MustBuiltin("azapi")
	merged := azurerm.With(azapi)
	if got := merged.Defaults()["azapi_resource"]; !slices.Contains(got, "schema_validation_enabled=true") {
		t.Errorf("azapi_resource defaults: got %v", got)
	}
	if merged.Source("azapi_resource") != "data/azapi/azapi.yaml" {
		t.Errorf("source: got %q", merged.Source("azapi_resource"))
	}
	if !reflect.DeepEqual(merged.Omit()[AllTypes], azurerm.Omit()[AllTypes]) || len(merged.Types()) != len(azurerm.Types())+len(azapi.Types()) {
		t.Errorf("merged: %d types, every type's omit %v", len(merged.Types()), merged.Omit()[AllTypes])
	}
	attributes := map[string]string{"resource_group_name": "DefaultResourceGroup-EUS"}
	if merged.Skipped("azapi_resource", "/subscriptions/s/resourceGroups/DefaultResourceGroup-EUS/providers/Microsoft.AzureFleet/fleets/f", attributes) == "" {
		t.Error("azurerm's rules for every type don't leave out an azapi_resource")
	}
	if _, ok := azurerm.Entry("azapi_resource"); ok {
		t.Error("With changed the azurerm catalogue")
	}
	if azurerm.With(nil) != azurerm {
		t.Error("With(nil) isn't the catalogue itself")
	}
}
