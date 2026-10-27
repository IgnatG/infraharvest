// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package managed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const state = `{
  "version": 4,
  "terraform_version": "1.16.5",
  "resources": [
    {"mode": "managed", "type": "aws_vpc", "name": "main", "instances": [{"attributes": {"id": "vpc-0abc1234", "arn": "arn:aws:ec2:eu-west-2:1:vpc/vpc-0abc1234", "cidr_block": "10.0.0.0/16"}}]},
    {"mode": "data", "type": "aws_caller_identity", "name": "current", "instances": [{"attributes": {"id": "1"}}]},
    {"module": "module.logs", "mode": "managed", "type": "aws_s3_bucket", "name": "this", "instances": [{"index_key": 0, "attributes": {"id": "logs"}}]}
  ]
}`

// fakeStore is a bucket in memory.
type fakeStore map[string]string

func (f fakeStore) List(_ context.Context, _, prefix string) ([]string, error) {
	var keys []string
	for k := range f {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (f fakeStore) Get(_ context.Context, _, key string) ([]byte, error) {
	return []byte(f[key]), nil
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "aws", "eu-west-2")
	if err := os.MkdirAll(filepath.Join(root, ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(root, "terraform.tfstate"):               state,
		filepath.Join(root, ".terraform", "terraform.tfstate"): `{"version": 3}`, // backend settings, not state
		filepath.Join(root, "main.tf"):                         "not state",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var region, profile string
	store := fakeStore{
		"imported/iam/terraform.tfstate": `{"version": 4, "resources": [{"mode": "managed", "type": "aws_iam_role", "instances": [{"attributes": {"id": "app"}}]}]}`,
		"imported/iam/notes.txt":         "not state",
		"other/terraform.tfstate":        `{"version": 4, "resources": [{"mode": "managed", "type": "aws_iam_role", "instances": [{"attributes": {"id": "other"}}]}]}`,
	}

	gcs := fakeStore{
		"roots/aws/global/default.tfstate": `{"version": 4, "resources": [{"mode": "managed", "type": "aws_iam_policy", "instances": [{"attributes": {"id": "arn:aws:iam::1:policy/app"}}]}]}`,
	}
	blobs := fakeStore{
		"azure/westeurope/default.tfstate": `{"version": 4, "resources": [{"mode": "managed", "type": "azurerm_resource_group", "instances": [{"attributes": {"id": "/subscriptions/1/resourceGroups/app"}}]}]}`,
	}
	var serviceURL string
	stores := Stores{
		S3: func(_ context.Context, rgn, prof string) (ObjectStore, error) {
			region, profile = rgn, prof
			return store, nil
		},
		GCS: func(context.Context) (ObjectStore, error) { return gcs, nil },
		AzureBlob: func(_ context.Context, u string) (ObjectStore, error) {
			serviceURL = u
			return containerStore{"tfstate", blobs}, nil
		},
	}

	r, err := Load(context.Background(), []string{dir, "s3://acme-state/imported/?region=eu-west-2&profile=state", "gs://acme-gcs/roots/", "https://acmestate.blob.core.windows.net/tfstate/azure/"}, stores)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		typ  string
		ids  []string
		want bool
	}{
		{"aws_vpc", []string{"vpc-0abc1234"}, true},
		{"aws_vpc", []string{"", "arn:aws:ec2:eu-west-2:1:vpc/vpc-0abc1234"}, true},
		{"aws_s3_bucket", []string{"logs"}, true},
		{"aws_iam_role", []string{"app"}, true},
		{"aws_iam_role", []string{"other"}, false}, // outside the prefix
		{"aws_iam_policy", []string{"arn:aws:iam::1:policy/app"}, true},
		{"aws_caller_identity", []string{"1"}, false},
		{"aws_subnet", []string{"vpc-0abc1234"}, false},
	} {
		if _, got := r.Lookup(tc.typ, tc.ids...); got != tc.want {
			t.Errorf("Lookup(%s, %v) = %v, want %v", tc.typ, tc.ids, got, tc.want)
		}
	}
	if where, _ := r.Lookup("aws_iam_role", "app"); where != "s3://acme-state/imported/iam/terraform.tfstate" {
		t.Errorf("where: %s", where)
	}
	if where, _ := r.Lookup("aws_iam_policy", "arn:aws:iam::1:policy/app"); where != "gs://acme-gcs/roots/aws/global/default.tfstate" {
		t.Errorf("where: %s", where)
	}
	if where, _ := r.Lookup("azurerm_resource_group", "/subscriptions/1/resourceGroups/app"); where != "https://acmestate.blob.core.windows.net/tfstate/azure/westeurope/default.tfstate" {
		t.Errorf("where: %s", where)
	}
	if region != "eu-west-2" || profile != "state" {
		t.Errorf("region, profile: %q, %q", region, profile)
	}
	if serviceURL != "https://acmestate.blob.core.windows.net/" {
		t.Errorf("service URL: %q", serviceURL)
	}
}

// containerStore is a fakeStore that is one Blob Storage container.
type containerStore struct {
	name string
	fakeStore
}

func (c containerStore) List(ctx context.Context, container, prefix string) ([]string, error) {
	if container != c.name {
		return nil, fmt.Errorf("no container %q", container)
	}
	return c.fakeStore.List(ctx, container, prefix)
}

func TestIsAzureBlob(t *testing.T) {
	for source, want := range map[string]bool{
		"https://acmestate.blob.core.windows.net/tfstate/":      true,
		"https://acmestate.blob.core.usgovcloudapi.net/tfstate": true,
		"https://acmestate.blob.core.chinacloudapi.cn/tfstate":  true,
		"https://acmestate.blob.evil.example/tfstate":           false,
		"https://acmestate.blob.core.windows.net.evil.example/": false,
		"https://blob.core.windows.net/tfstate/":                false,
		"https://acmestate.blob.core.windows.net:8443/tfstate/": false,
		"https://acmestate.file.core.windows.net/share/":        false,
		"http://acmestate.blob.core.windows.net/tfstate/":       false,
		"s3://acme-state/blob.json":                             false,
		"terraform.tfstate":                                     false,
	} {
		if got := isAzureBlob(source); got != want {
			t.Errorf("isAzureBlob(%q) = %v, want %v", source, got, want)
		}
	}
	if _, err := Load(context.Background(), []string{"https://acmestate.blob.core.windows.net/"}, Stores{}); err == nil {
		t.Error("want an error for a source without a container")
	}
	// Another host would get the Azure credentials.
	for _, source := range []string{"https://acmestate.blob.evil.example/tfstate/", "http://acmestate.blob.core.windows.net/tfstate/"} {
		if err := CheckSources([]string{source}); err == nil || !strings.Contains(err.Error(), "not a Blob Storage URL") {
			t.Errorf("CheckSources(%s): want an error naming Blob Storage, got %v", source, err)
		}
		if _, err := Load(context.Background(), []string{source}, Stores{}); err == nil || !strings.Contains(err.Error(), "not a Blob Storage URL") {
			t.Errorf("Load(%s): want an error naming Blob Storage, got %v", source, err)
		}
	}
}

func TestLoadRejectsOtherFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.tfstate")
	if err := os.WriteFile(path, []byte(`{"version": 2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), []string{path}, Stores{}); err == nil || !strings.Contains(err.Error(), "not a version 4 state file") {
		t.Errorf("want a version error, got %v", err)
	}
}

// Version 3 state, as Terraform 0.11 and earlier wrote it, is refused with
// a hint to upgrade it, and nothing in it counts as managed.
func TestParseRejectsVersion3(t *testing.T) {
	r := Resources{}
	err := r.Parse([]byte(`{"version": 3, "modules": [{"path": ["root"], "resources": {
	  "aws_vpc.main": {"type": "aws_vpc", "primary": {"id": "vpc-0abc1234", "attributes": {"id": "vpc-0abc1234"}}}
	}}]}`), "terraform.tfstate")
	want := "terraform.tfstate: state format version 3 is not supported; upgrade it with Terraform 0.12 or later"
	if err == nil || err.Error() != want {
		t.Errorf("got %v, want %q", err, want)
	}
	if len(r) != 0 {
		t.Errorf("resources: %v", r)
	}
}

// A source that holds no state fails, naming it, rather than leaving out
// nothing as if no resource were managed: a directory without .tfstate
// files (outside .terraform), a prefix without .tfstate objects, and a
// workspace pattern that matches no workspace.
func TestLoadRejectsSourcesWithoutState(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(dir, "main.tf"):                         "not state",
		filepath.Join(dir, ".terraform", "terraform.tfstate"): `{"version": 4}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bucket := fakeStore{"roots/notes.txt": "not state", "other/terraform.tfstate": `{"version": 4}`}
	stores := Stores{
		S3:           func(context.Context, string, string) (ObjectStore, error) { return bucket, nil },
		GCS:          func(context.Context) (ObjectStore, error) { return bucket, nil },
		AzureBlob:    func(context.Context, string) (ObjectStore, error) { return containerStore{"tfstate", bucket}, nil },
		HCPTerraform: func(context.Context, string) (ObjectStore, error) { return fakeWorkspaces{"app-prod": nil}, nil },
	}
	for _, source := range []string{dir, "s3://acme-state/roots/", "gs://acme-gcs/roots/", "https://acmestate.blob.core.windows.net/tfstate/roots/", "tfc://acme/network-*"} {
		_, err := Load(context.Background(), []string{source}, stores)
		if err == nil || !strings.Contains(err.Error(), "state "+source+": no ") {
			t.Errorf("%s: want an error saying it holds no state, got %v", source, err)
		}
	}
	// A workspace with no state yet is there, and manages nothing.
	if _, err := Load(context.Background(), []string{"tfc://acme/app-*"}, stores); err != nil {
		t.Errorf("a workspace without state: %v", err)
	}
}

// A source whose kind of store the Stores given leave out fails, naming
// that store, instead of panicking on the nil function.
func TestLoadWithoutStore(t *testing.T) {
	for source, want := range map[string]string{
		"s3://acme-state/roots/":                                 "no S3 store",
		"gs://acme-gcs/roots/":                                   "no Cloud Storage store",
		"https://acmestate.blob.core.windows.net/tfstate/roots/": "no Blob Storage store",
		"tfc://acme/network-*":                                   "no HCP Terraform store",
	} {
		_, err := Load(context.Background(), []string{source}, Stores{})
		if err == nil || !strings.Contains(err.Error(), "state "+source+": "+want) {
			t.Errorf("%s: want an error saying %q, got %v", source, want, err)
		}
	}
}

// Azure IDs match whatever their case, the types another name of a type
// manages match it, and ARNs keep their case.
func TestLookupMatchesCaseAndAliases(t *testing.T) {
	r := Resources{}
	err := r.Parse([]byte(`{"version": 4, "resources": [
	  {"mode": "managed", "type": "azurerm_log_analytics_workspace", "instances": [{"attributes": {"id": "/subscriptions/1/resourcegroups/App/providers/microsoft.operationalinsights/workspaces/w"}}]},
	  {"mode": "managed", "type": "aws_alb", "instances": [{"attributes": {"id": "arn:aws:elasticloadbalancing:eu-west-2:1:loadbalancer/app/web/1", "arn": "arn:aws:elasticloadbalancing:eu-west-2:1:loadbalancer/app/web/1"}}]},
	  {"mode": "managed", "type": "azurerm_virtual_machine", "instances": [{"attributes": {"id": "/subscriptions/1/resourceGroups/app/providers/Microsoft.Compute/virtualMachines/vm"}}]},
	  {"mode": "managed", "type": "aws_s3_bucket", "instances": [{"attributes": {"id": "Logs"}}]}
	]}`), "prod.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		typ, id string
		want    bool
	}{
		{"azurerm_log_analytics_workspace", "/subscriptions/1/resourceGroups/app/providers/Microsoft.OperationalInsights/workspaces/w", true},
		{"aws_lb", "arn:aws:elasticloadbalancing:eu-west-2:1:loadbalancer/app/web/1", true},
		{"aws_alb", "arn:aws:elasticloadbalancing:eu-west-2:1:loadbalancer/app/web/1", true},
		{"aws_lb", "arn:aws:elasticloadbalancing:eu-west-2:1:loadbalancer/app/WEB/1", false},
		{"azurerm_linux_virtual_machine", "/subscriptions/1/resourceGroups/APP/providers/Microsoft.Compute/virtualMachines/vm", true},
		{"azurerm_windows_virtual_machine", "/subscriptions/1/resourceGroups/app/providers/Microsoft.Compute/virtualMachines/vm", true},
		{"azurerm_linux_virtual_machine", "/subscriptions/1/resourceGroups/app/providers/Microsoft.Compute/virtualMachines/other", false},
		{"aws_s3_bucket", "logs", false},
	} {
		if _, got := r.Lookup(tc.typ, tc.id); got != tc.want {
			t.Errorf("Lookup(%s, %s) = %v, want %v", tc.typ, tc.id, got, tc.want)
		}
	}
}

// The azurerm backend keeps a workspace's state at <key>env:<workspace>,
// which is read too; in a bucket, such a name isn't state.
func TestLoadReadsAzureWorkspaces(t *testing.T) {
	staging := `{"version": 4, "resources": [{"mode": "managed", "type": "azurerm_resource_group", "instances": [{"attributes": {"id": "/subscriptions/1/resourceGroups/staging"}}]}]}`
	blobs := fakeStore{
		"prod/terraform.tfstate":            `{"version": 4, "resources": []}`,
		"prod/terraform.tfstateenv:staging": staging,
		"prod/terraform.tfstate.backup":     "not state",
	}
	stores := Stores{
		AzureBlob: func(context.Context, string) (ObjectStore, error) { return containerStore{"tfstate", blobs}, nil },
	}
	r, err := Load(context.Background(), []string{"https://acmestate.blob.core.windows.net/tfstate/prod/"}, stores)
	if err != nil {
		t.Fatal(err)
	}
	where, ok := r.Lookup("azurerm_resource_group", "/subscriptions/1/resourceGroups/staging")
	if !ok || where != "https://acmestate.blob.core.windows.net/tfstate/prod/terraform.tfstateenv:staging" {
		t.Errorf("the staging workspace's state: %q, %v", where, ok)
	}
	if isStateObject("s3", "prod/terraform.tfstateenv:staging") {
		t.Error("an S3 object named like an azurerm workspace isn't state")
	}
}

// recordingStore is a fakeStore that records the prefixes it is listed
// with.
type recordingStore struct {
	fakeStore
	prefixes *[]string
}

func (r recordingStore) List(ctx context.Context, bucket, prefix string) ([]string, error) {
	*r.prefixes = append(*r.prefixes, prefix)
	return r.fakeStore.List(ctx, bucket, prefix)
}

// The S3 backend keeps the state of a workspace other than default at
// <workspace_key_prefix>/<workspace>/<key>, at the bucket's root, which is
// listed too: "env:/" unless the source names another prefix, keeping only
// the keys under the source's prefix. A source under the workspaces'
// prefix, or with none, lists them already.
func TestLoadReadsS3Workspaces(t *testing.T) {
	role := func(id string) string {
		return `{"version": 4, "resources": [{"mode": "managed", "type": "aws_iam_role", "instances": [{"attributes": {"id": "` + id + `"}}]}]}`
	}
	bucket := fakeStore{
		"imported/iam/terraform.tfstate":              role("default"),
		"env:/staging/imported/iam/terraform.tfstate": role("staging"),
		"env:/staging/other/terraform.tfstate":        role("other"),
		"env:/staging/imported/iam/notes.txt":         "not state",
		"workspaces/prod/imported/terraform.tfstate":  role("prod"),
	}
	for _, tc := range []struct {
		source       string
		wantPrefixes []string
		found        []string
		notFound     []string
		where        string
	}{
		{
			source:       "s3://acme-state/imported/",
			wantPrefixes: []string{"imported/", "env:/"},
			found:        []string{"default", "staging"},
			notFound:     []string{"other", "prod"},
			where:        "s3://acme-state/env:/staging/imported/iam/terraform.tfstate",
		},
		{
			source:       "s3://acme-state/imported?workspace_key_prefix=workspaces",
			wantPrefixes: []string{"imported", "workspaces/"},
			found:        []string{"default", "prod"},
			notFound:     []string{"staging", "other"},
		},
		{
			source:       "s3://acme-state/env:/staging/",
			wantPrefixes: []string{"env:/staging/"},
			found:        []string{"staging", "other"},
			notFound:     []string{"default"},
		},
		{
			source:       "s3://acme-state",
			wantPrefixes: []string{""},
			found:        []string{"default", "staging", "other", "prod"},
		},
	} {
		var prefixes []string
		stores := Stores{S3: func(context.Context, string, string) (ObjectStore, error) {
			return recordingStore{bucket, &prefixes}, nil
		}}
		r, err := Load(context.Background(), []string{tc.source}, stores)
		if err != nil {
			t.Errorf("%s: %v", tc.source, err)
			continue
		}
		if fmt.Sprint(prefixes) != fmt.Sprint(tc.wantPrefixes) {
			t.Errorf("%s: listed %q, want %q", tc.source, prefixes, tc.wantPrefixes)
		}
		for _, id := range tc.found {
			if _, ok := r.Lookup("aws_iam_role", id); !ok {
				t.Errorf("%s: role %s not found", tc.source, id)
			}
		}
		for _, id := range tc.notFound {
			if where, ok := r.Lookup("aws_iam_role", id); ok {
				t.Errorf("%s: role %s found in %s", tc.source, id, where)
			}
		}
		if tc.where != "" {
			if where, _ := r.Lookup("aws_iam_role", "staging"); where != tc.where {
				t.Errorf("%s: where %q, want %q", tc.source, where, tc.where)
			}
		}
	}
}

// A state larger than the limit fails, saying so, rather than being cut
// off into a JSON error.
func TestReadStateLimit(t *testing.T) {
	limit := stateSizeLimit
	defer func() { stateSizeLimit = limit }()
	stateSizeLimit = 4 << 20

	content, err := readState(strings.NewReader(strings.Repeat("x", 4<<20)))
	if err != nil || len(content) != 4<<20 {
		t.Errorf("at the limit: %d bytes, %v", len(content), err)
	}
	if _, err := readState(strings.NewReader(strings.Repeat("x", 4<<20+1))); err == nil || !strings.Contains(err.Error(), "larger than 4 MiB") {
		t.Errorf("over the limit: %v", err)
	}
	path := filepath.Join(t.TempDir(), "big.tfstate")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", 4<<20+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), []string{path}, Stores{}); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("a local state over the limit: %v", err)
	}
}

// States are read several at a time, and what they manage is added as if
// read in order; the first failure is returned.
func TestParseAll(t *testing.T) {
	stateOf := func(id string) []byte {
		return []byte(`{"version": 4, "resources": [{"mode": "managed", "type": "aws_s3_bucket", "instances": [{"attributes": {"id": "` + id + `"}}]}]}`)
	}
	var keys []string
	content := map[string][]byte{}
	for i := range 3 * fetchParallel {
		key := fmt.Sprintf("roots/%02d.tfstate", i)
		keys = append(keys, key)
		content[key] = stateOf("shared")
	}
	content["roots/05.tfstate"] = nil // no state yet
	get := func(_ context.Context, key string) ([]byte, error) {
		body, ok := content[key]
		if !ok {
			return nil, fmt.Errorf("no such key %s", key)
		}
		return body, nil
	}
	r := Resources{}
	if err := r.parseAll(context.Background(), keys, get, "s3://acme/"); err != nil {
		t.Fatal(err)
	}
	if where, _ := r.Lookup("aws_s3_bucket", "shared"); where != "s3://acme/"+keys[len(keys)-1] {
		t.Errorf("the last state should win, as read in order: %q", where)
	}

	failing := func(ctx context.Context, key string) ([]byte, error) {
		if key == "roots/03.tfstate" {
			return nil, fmt.Errorf("access denied to %s", key)
		}
		return get(ctx, key)
	}
	if err := (Resources{}).parseAll(context.Background(), keys, failing, "s3://acme/"); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Errorf("want the failure, got %v", err)
	}
}

// Each resource has its address in the state: in a module call, with its
// count or for_each key; one the state doesn't name has none.
func TestParseKeepsAddresses(t *testing.T) {
	r := Resources{}
	err := r.Parse([]byte(`{"version": 4, "resources": [
	  {"mode": "managed", "type": "aws_s3_bucket", "name": "logs", "instances": [{"attributes": {"id": "logs"}}]},
	  {"mode": "managed", "module": "module.vpc", "type": "aws_subnet", "name": "private", "instances": [{"index_key": 0, "attributes": {"id": "subnet-0"}}, {"index_key": 1, "attributes": {"id": "subnet-1"}}]},
	  {"mode": "managed", "type": "aws_iam_role", "name": "app", "instances": [{"index_key": "web", "attributes": {"id": "web", "arn": "arn:aws:iam::1:role/web"}}]},
	  {"mode": "managed", "type": "aws_sqs_queue", "instances": [{"attributes": {"id": "jobs"}}]}
	]}`), "prod.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		typ, id, want string
	}{
		{"aws_s3_bucket", "logs", "aws_s3_bucket.logs"},
		{"aws_subnet", "subnet-1", "module.vpc.aws_subnet.private[1]"},
		{"aws_iam_role", "arn:aws:iam::1:role/web", `aws_iam_role.app["web"]`},
		{"aws_sqs_queue", "jobs", ""},
	} {
		res, ok := r.Find(tc.typ, tc.id)
		if !ok || res.Address != tc.want || res.Type != tc.typ || res.Where != "prod.tfstate" {
			t.Errorf("Find(%s, %s) = %+v, %v, want address %q", tc.typ, tc.id, res, ok, tc.want)
		}
	}
}
