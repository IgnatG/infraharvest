// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package catalogue

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"go.yaml.in/yaml/v3"
)

func TestBuiltinLoads(t *testing.T) {
	providers, err := Providers()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(providers, []string{"aws", "azurerm", "google"}) {
		t.Errorf("providers: got %v", providers)
	}
	aws := MustBuiltin("aws")
	if len(aws.Types()) < 100 {
		t.Errorf("aws: %d types, want the ARN, Cloud Control, network and identity types", len(aws.Types()))
	}
	if e, ok := aws.Entry("aws_vpc"); !ok || e.Service != "vpc" || e.DataSource == nil {
		t.Errorf("aws_vpc: got %+v, want the vpc service's entry with a data source", e)
	}
	if got := aws.Omit()[AllTypes]; !reflect.DeepEqual(got, []string{"region", "tags_all"}) {
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
		"inventory types apart":   "provider: google\ntypes:\n  google_x:\n    inventory: { type: compute.googleapis.com/Disk, name: \"//compute.googleapis.com/projects/*/zones/*/disks/*\" }\n  google_y:\n    inventory: { type: compute.googleapis.com/disk }\n",
		"repeated argument":       "provider: aws\ntypes:\n  aws_x:\n    omit: [a, a]\n",
		"cloud control service":   "provider: aws\ntypes:\n  aws_x:\n    cloud_control: { type: \"AWS::X::Y\", identifier: Id }\n",
		"unknown field":           "provider: aws\ntypes:\n  aws_x:\n    imports: x\n",
		"bad filter":              "provider: aws\ntypes:\n  aws_x:\n    import_id: \"{id|upper:x}\"\n",
	} {
		if _, err := load(t, map[string]string{"x.yaml": file}); err == nil {
			t.Errorf("%s: want an error for\n%s", name, file)
		}
	}
	if _, err := load(t, map[string]string{"a.yaml": "provider: aws\ntypes:\n  aws_x: {}\n", "b.yaml": "provider: aws\ntypes:\n  aws_x: {}\n"}); err == nil || !strings.Contains(err.Error(), "aws_x is in") {
		t.Errorf("a type in two files: got %v", err)
	}
}

// Overlay entries replace the fields they set, and add types.
func TestLoadOverlay(t *testing.T) {
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
	return loadFS(os.DirFS(dir), ".", true)
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

func TestFromInventory(t *testing.T) {
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
