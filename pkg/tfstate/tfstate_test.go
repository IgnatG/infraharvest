// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfstate

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// fixture returns the content of a state in testdata.
func fixture(t testing.TB, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return content
}

// TestParseGolden checks what a Document keeps of states with modules,
// count, for_each, data sources, resource identities and
// terraform_remote_state against testdata/documents.golden.json.
func TestParseGolden(t *testing.T) {
	t.Parallel()
	docs := map[string]Document{}
	for _, name := range []string{"modules.tfstate", "remote-states.tfstate", "lenient.tfstate"} {
		doc, err := Parse(fixture(t, name), "testdata/"+name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		docs[name] = doc
	}
	got, err := json.MarshalIndent(docs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "documents.golden.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("the documents differ from %s (make golden PKG=./pkg/tfstate to rewrite it, and review the diff):\n%s", path, got)
	}
}

// A Document holds no attribute value, no output value and no credential
// of a remote state, whatever the state has.
func TestParseKeepsNoValues(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"modules.tfstate", "remote-states.tfstate"} {
		doc, err := Parse(fixture(t, name), name)
		if err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"hunter2", "AKIAEXAMPLE", "cidr_block", "10.0.0.0", "AIDAEXAMPLE", "consul.example.com"} {
			if strings.Contains(string(out), secret) {
				t.Errorf("%s: the document holds %q: %s", name, secret, out)
			}
		}
	}
}

// What a Document says of the fixtures that the golden file's reader
// should see at a glance.
func TestParseModules(t *testing.T) {
	t.Parallel()
	doc, err := Parse(fixture(t, "modules.tfstate"), "w")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Source != "w" || doc.Serial != 42 || doc.Lineage != "5b3c9a40-0f1e-4a0d-9d3e-2f6e8b1c7a11" || doc.TerraformVersion != "1.16.5" {
		t.Errorf("header: %+v", doc)
	}
	var addresses []string
	for _, i := range doc.Instances {
		addresses = append(addresses, i.Address)
	}
	want := []string{
		"aws_vpc.main", "module.vpc.aws_subnet.private[0]", "module.vpc.aws_subnet.private[1]",
		`module.app["blue"].aws_s3_bucket.logs["eu"]`, `module.app["blue"].aws_s3_bucket.logs["us"]`,
		"data.aws_caller_identity.current", "module.vpc.data.aws_availability_zones.all", "data.terraform_remote_state.network",
	}
	if !reflect.DeepEqual(addresses, want) {
		t.Errorf("addresses:\n got %q\nwant %q", addresses, want)
	}
	subnet := doc.Instances[1]
	if subnet.ID != "subnet-0" || subnet.ARN != "arn:aws:ec2:eu-west-2:123456789012:subnet/subnet-0" || subnet.Mode != "managed" ||
		!reflect.DeepEqual(subnet.Dependencies, []string{"aws_vpc.main", "data.aws_caller_identity.current"}) {
		t.Errorf("a subnet: %+v", subnet)
	}
	// An instance with an ARN alone is found by it.
	if us := doc.Instances[4]; us.ID != us.ARN || us.ID != "arn:aws:s3:::logs-us" {
		t.Errorf("a bucket with an ARN alone: %+v", us)
	}
	if eu := doc.Instances[3]; eu.IdentityID != "account_id=123456789012,bucket=logs-eu,region=eu-west-2" {
		t.Errorf("a bucket's identity: %q", eu.IdentityID)
	}
	if doc.Instances[0].Provider != `provider["registry.terraform.io/hashicorp/aws"]` {
		t.Errorf("the provider: %q", doc.Instances[0].Provider)
	}
	wantOutputs := []Output{{"vpc_id", false}, {"db_password", true}, {"subnets", false}}
	if !reflect.DeepEqual(doc.Outputs, wantOutputs) {
		t.Errorf("outputs: %+v", doc.Outputs)
	}
	if len(doc.RemoteStates) != 1 || doc.RemoteStates[0] != (RemoteState{Address: "data.terraform_remote_state.network", Backend: "s3", Bucket: "acme-state", Key: "network/terraform.tfstate", Workspace: "prod"}) {
		t.Errorf("remote states: %+v", doc.RemoteStates)
	}
}

// Each backend's remote state is read into the fields that say where the
// other state is.
func TestParseRemoteStates(t *testing.T) {
	t.Parallel()
	doc, err := Parse(fixture(t, "remote-states.tfstate"), "w")
	if err != nil {
		t.Fatal(err)
	}
	want := []RemoteState{
		{Address: "data.terraform_remote_state.gcs", Backend: "gcs", Bucket: "acme-gcs-state", Key: "network"},
		{Address: "data.terraform_remote_state.azure", Backend: "azurerm", Account: "acmestate", Bucket: "tfstate", Key: "network.tfstate"},
		{Address: "data.terraform_remote_state.hcp", Backend: "remote", Host: "tfe.example.com", Organization: "acme", Workspace: "network-prod"},
		{Address: "data.terraform_remote_state.local", Backend: "local", Key: "../network/terraform.tfstate"},
		{Address: `data.terraform_remote_state.other["a"]`, Backend: "consul"},
	}
	if !reflect.DeepEqual(doc.RemoteStates, want) {
		t.Errorf("remote states:\n got %+v\nwant %+v", doc.RemoteStates, want)
	}
}

// A value of the wrong type in a field infraharvest never read leaves the
// field empty; it doesn't fail a state that always read.
func TestParseIsLenientAboutNewFields(t *testing.T) {
	t.Parallel()
	doc, err := Parse(fixture(t, "lenient.tfstate"), "w")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Serial != 0 || doc.Lineage != "" || doc.TerraformVersion != "" {
		t.Errorf("header: %+v", doc)
	}
	wantOutputs := []Output{{"a", false}, {"b", false}, {"c", false}, {"d", true}}
	if !reflect.DeepEqual(doc.Outputs, wantOutputs) {
		t.Errorf("outputs: %+v", doc.Outputs)
	}
	if len(doc.Instances) != 2 || doc.Instances[0].Provider != "" || doc.Instances[0].Dependencies != nil ||
		!reflect.DeepEqual(doc.Instances[1].Dependencies, []string{"a", "b"}) {
		t.Errorf("instances: %+v", doc.Instances)
	}
}

func TestParseRefusesOtherVersions(t *testing.T) {
	t.Parallel()
	for content, want := range map[string]string{
		`{"version": 3}`: "version 3 is not supported",
		`{"version": 2}`: "not a version 4 state file",
		`{}`:             "not a version 4 state file",
		`not state`:      "invalid character",
		``:               "EOF",
	} {
		if _, err := Parse([]byte(content), "w"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want an error with %q, got %v", content, want, err)
		}
	}
}

// fakeStore is a bucket in memory, which streams.
type fakeStore struct {
	bucket string
	keys   map[string]string
	lists  *[]string
}

func (f fakeStore) List(_ context.Context, bucket, prefix string) ([]string, error) {
	if f.bucket != "" && bucket != f.bucket {
		return nil, fmt.Errorf("no bucket %q", bucket)
	}
	if f.lists != nil {
		*f.lists = append(*f.lists, prefix)
	}
	var keys []string
	for k := range f.keys {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (f fakeStore) Get(_ context.Context, _, key string) ([]byte, error) {
	content, ok := f.keys[key]
	if !ok {
		return nil, nil
	}
	return []byte(content), nil
}

// Every kind of location is read into the same Documents, found where the
// location says.
func TestReadEveryBackend(t *testing.T) {
	t.Parallel()
	modules, remotes := string(fixture(t, "modules.tfstate")), string(fixture(t, "remote-states.tfstate"))
	s3 := fakeStore{bucket: "acme", keys: map[string]string{"net/terraform.tfstate": modules, "net/notes.txt": "x"}}
	gcs := fakeStore{bucket: "acme", keys: map[string]string{"net/default.tfstate": modules}}
	blob := fakeStore{bucket: "tfstate", keys: map[string]string{"net/terraform.tfstate": modules}}
	hcp := fakeStore{bucket: "acme", keys: map[string]string{"net-prod": modules, "net-dev": remotes, "app": remotes}}
	stores := Stores{
		S3:           func(context.Context, string, string) (ObjectStore, error) { return s3, nil },
		GCS:          func(context.Context) (ObjectStore, error) { return gcs, nil },
		AzureBlob:    func(context.Context, string) (ObjectStore, error) { return blob, nil },
		HCPTerraform: func(context.Context, string) (ObjectStore, error) { return hcp, nil },
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "root", ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(dir, "root", "terraform.tfstate"):               modules,
		filepath.Join(dir, "root", ".terraform", "terraform.tfstate"): `{"version": 3}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for location, sources := range map[string][]string{
		"s3://acme/net/?region=eu-west-2":                      {"s3://acme/net/terraform.tfstate"},
		"gs://acme/net/":                                       {"gs://acme/net/default.tfstate"},
		"https://acmestate.blob.core.windows.net/tfstate/net/": {"https://acmestate.blob.core.windows.net/tfstate/net/terraform.tfstate"},
		"tfc://acme/net-prod":                                  {"tfc://acme/net-prod"},
		"tfc://acme/net-*":                                     {"tfc://acme/net-dev", "tfc://acme/net-prod"},
		filepath.Join(dir, "root"):                             {filepath.Join(dir, "root", "terraform.tfstate")},
		filepath.Join(dir, "root", "terraform.tfstate"):        {filepath.Join(dir, "root", "terraform.tfstate")},
	} {
		docs, err := Read(t.Context(), location, stores)
		if err != nil {
			t.Errorf("%s: %v", location, err)
			continue
		}
		var got []string
		for _, doc := range docs {
			got = append(got, doc.Source)
		}
		if !sameSet(got, sources) {
			t.Errorf("%s: states at %q, want %q", location, got, sources)
		}
		for _, doc := range docs {
			if doc.Serial == 0 || len(doc.Instances) == 0 {
				t.Errorf("%s: %s read as %+v", location, doc.Source, doc)
			}
		}
	}
	// A workspace a store returns no state for has none.
	if docs, err := Read(t.Context(), "tfc://acme/net-new", stores); err != nil || len(docs) != 0 {
		t.Errorf("a workspace without state: %v, %v", docs, err)
	}
	// Terraform Enterprise hosts are named in the location.
	if docs, err := Read(t.Context(), "tfc://acme/app?host=tfe.example.com", stores); err != nil || len(docs) != 1 || docs[0].Source != "tfc://tfe.example.com/acme/app" {
		t.Errorf("a Terraform Enterprise workspace: %+v, %v", docs, err)
	}
	if _, err := ReadAll(t.Context(), []string{"gs://acme/net/", "s3://acme/missing/"}, stores); err == nil || !strings.Contains(err.Error(), "state s3://acme/missing/") {
		t.Errorf("a failing location names itself: %v", err)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		if !seen[s] {
			return false
		}
	}
	return true
}

// A location whose store isn't given reads no state, and says so.
func TestReadWithoutStore(t *testing.T) {
	t.Parallel()
	for _, location := range []string{"s3://acme/net/", "gs://acme/net/", "https://acmestate.blob.core.windows.net/tfstate/", "tfc://acme/net"} {
		if _, err := Read(t.Context(), location, Stores{}); err == nil || !strings.Contains(err.Error(), "store to read the state with") {
			t.Errorf("%s: %v", location, err)
		}
	}
}

func TestReadRejectsOtherLocations(t *testing.T) {
	t.Parallel()
	empty := t.TempDir()
	for location, want := range map[string]string{
		"https://example.com/state":                 "not a Blob Storage URL",
		"http://acmestate.blob.core.windows.net/x/": "not a Blob Storage URL",
		"https://acmestate.blob.core.windows.net/":  "name a container",
		"s3:///prefix":                          "name a bucket",
		"tfc://acme":                            "name an organization and a workspace",
		empty:                                   "no .tfstate file in the directory",
		filepath.Join(empty, "missing.tfstate"): "no such file",
	} {
		_, err := Read(t.Context(), location, Stores{})
		// A missing file is told apart by its error, not its text, which differs on Windows.
		missing := want == "no such file" && errors.Is(err, fs.ErrNotExist)
		if err == nil || (!missing && !strings.Contains(err.Error(), want)) {
			t.Errorf("Read(%s): want an error with %q, got %v", location, want, err)
		}
		if err := CheckLocations([]string{location}); err == nil && want != "no .tfstate file in the directory" {
			t.Errorf("CheckLocations(%s): want an error", location)
		}
	}
}

func TestIsAzureBlob(t *testing.T) {
	t.Parallel()
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
}

// An S3 object named like an azurerm workspace isn't state; in Blob
// Storage it is.
func TestIsStateObject(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		scheme, key string
		want        bool
	}{
		{"s3", "prod/terraform.tfstate", true},
		{"gs", "prod/default.tfstate", true},
		{"https", "prod/terraform.tfstateenv:staging", true},
		{"s3", "prod/terraform.tfstateenv:staging", false},
		{"s3", "prod/terraform.tfstate.backup", false},
		{"https", "prod/notes.txt", false},
	} {
		if got := isStateObject(tc.scheme, tc.key); got != tc.want {
			t.Errorf("isStateObject(%q, %q) = %v, want %v", tc.scheme, tc.key, got, tc.want)
		}
	}
}

func TestAzureBlobSuffix(t *testing.T) {
	t.Parallel()
	for env, want := range map[string]string{
		"": "blob.core.windows.net", "public": "blob.core.windows.net", "AzurePublicCloud": "blob.core.windows.net",
		"usgovernment": "blob.core.usgovcloudapi.net", "AzureUSGovernmentCloud": "blob.core.usgovcloudapi.net",
		"china": "blob.core.chinacloudapi.cn", "AzureChinaCloud": "blob.core.chinacloudapi.cn",
	} {
		if got, err := AzureBlobSuffix(env); err != nil || got != want {
			t.Errorf("AzureBlobSuffix(%q) = %q, %v, want %q", env, got, err, want)
		}
	}
	if _, err := AzureBlobSuffix("germany"); err == nil {
		t.Error("want an error for an environment without Blob Storage here")
	}
}

// A state larger than the limit fails, saying so, rather than being cut
// off into a JSON error.
func TestReadStateLimit(t *testing.T) {
	limit := stateSizeLimit
	defer func() { stateSizeLimit = limit }()
	stateSizeLimit = 4 << 20

	content, err := ReadState(strings.NewReader(strings.Repeat("x", 4<<20)))
	if err != nil || len(content) != 4<<20 {
		t.Errorf("at the limit: %d bytes, %v", len(content), err)
	}
	if _, err := ReadState(strings.NewReader(strings.Repeat("x", 4<<20+1))); err == nil || !strings.Contains(err.Error(), "larger than 4 MiB") {
		t.Errorf("over the limit: %v", err)
	}
	path := filepath.Join(t.TempDir(), "big.tfstate")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", 4<<20+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(t.Context(), path, Stores{}); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("a local state over the limit: %v", err)
	}
}

// ParseFrom fails on a state larger than the limit as ReadState does.
func TestParseFromLimit(t *testing.T) {
	limit := stateSizeLimit
	defer func() { stateSizeLimit = limit }()
	stateSizeLimit = 1 << 10
	big := `{"version": 4, "outputs": {"a": "` + strings.Repeat("x", 2<<10) + `"}, "resources": []}`
	if doc, err := ParseFrom(strings.NewReader(big), "w"); err == nil || !strings.Contains(err.Error(), "larger than") || doc.Source != "" {
		t.Errorf("a state over the limit: %v, %+v", err, doc)
	}
	exact := `{"version": 4, "resources": []}`
	stateSizeLimit = int64(len(exact))
	if _, err := ParseFrom(strings.NewReader(exact), "w"); err != nil {
		t.Errorf("a state of the limit: %v", err)
	}
}

// States are read several at a time, and come in the order of their keys;
// the first failure is returned.
func TestParseAll(t *testing.T) {
	t.Parallel()
	stateOf := func(id string) []byte {
		return []byte(`{"version": 4, "resources": [{"mode": "managed", "type": "aws_s3_bucket", "instances": [{"attributes": {"id": "` + id + `"}}]}]}`)
	}
	var keys []string
	content := map[string][]byte{}
	for i := range 3 * fetchParallel {
		key := fmt.Sprintf("roots/%02d.tfstate", i)
		keys = append(keys, key)
		content[key] = stateOf(key)
	}
	content["roots/05.tfstate"] = nil // no state yet
	get := func(_ context.Context, key string) ([]byte, error) {
		body, ok := content[key]
		if !ok {
			return nil, fmt.Errorf("no such key %s", key)
		}
		return body, nil
	}
	docs, err := parseAll(t.Context(), keys, opener(getStore(get), "acme"), "s3://acme/")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != len(keys)-1 {
		t.Fatalf("%d states, want %d", len(docs), len(keys)-1)
	}
	for i, doc := range docs {
		want := keys[i]
		if i >= 5 {
			want = keys[i+1]
		}
		if doc.Source != "s3://acme/"+want || doc.Instances[0].ID != want {
			t.Errorf("state %d is %s with %q, want %s", i, doc.Source, doc.Instances[0].ID, want)
		}
	}

	failing := func(ctx context.Context, key string) ([]byte, error) {
		if key == "roots/03.tfstate" {
			return nil, fmt.Errorf("access denied to %s", key)
		}
		return get(ctx, key)
	}
	if _, err := parseAll(t.Context(), keys, opener(getStore(failing), "acme"), "s3://acme/"); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Errorf("want the failure, got %v", err)
	}
}

// getStore is an ObjectStore of one bucket whose objects get returns.
type getStore func(ctx context.Context, key string) ([]byte, error)

func (g getStore) List(context.Context, string, string) ([]string, error) { return nil, nil }

func (g getStore) Get(ctx context.Context, _, key string) ([]byte, error) { return g(ctx, key) }

func TestWithoutURL(t *testing.T) {
	t.Parallel()
	inner := fmt.Errorf("connection refused")
	if got := WithoutURL(&url.Error{Op: "Get", URL: "https://host/state?sig=secret", Err: inner}); !errors.Is(got, inner) {
		t.Errorf("got %v", got)
	}
	plain := fmt.Errorf("plain")
	if got := WithoutURL(plain); !errors.Is(got, plain) {
		t.Errorf("got %v", got)
	}
}

// An instance's tags are read from the tags, tags_all and labels attributes,
// leniently, and no other attribute value is kept.
func TestParseTags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		attributes string
		want       map[string]string
	}{
		{"aws tags merged with tags_all, which wins", `{"id": "i-1", "tags": {"Name": "web", "Env": "dev"}, "tags_all": {"Env": "prod", "Owner": "ops"}}`,
			map[string]string{"Name": "web", "Env": "prod", "Owner": "ops"}},
		{"aws tags_all alone", `{"id": "i-1", "tags_all": {"Owner": "ops"}}`, map[string]string{"Owner": "ops"}},
		{"azure tags", `{"id": "/subscriptions/s/rg/r", "tags": {"cost-centre": "42"}}`, map[string]string{"cost-centre": "42"}},
		{"gcp labels", `{"id": "projects/p/buckets/b", "labels": {"team": "data", "env": "stg"}}`, map[string]string{"team": "data", "env": "stg"}},
		{"a non-string value is skipped", `{"id": "i-1", "tags": {"Name": "web", "count": 3, "on": true, "nested": {"a": "b"}, "list": ["x"], "nil": null}}`,
			map[string]string{"Name": "web"}},
		{"a tags attribute that is not an object", `{"id": "i-1", "tags": "oops", "tags_all": ["x"], "labels": 7}`, nil},
		{"null tags", `{"id": "i-1", "tags": null, "tags_all": null}`, nil},
		{"empty tags", `{"id": "i-1", "tags": {}}`, nil},
		{"no tags attribute", `{"id": "i-1", "cidr_block": "10.0.0.0/16"}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state := `{"version": 4, "resources": [{"mode": "managed", "type": "t", "name": "n", "instances": [{"attributes": ` + tt.attributes + `}]}]}`
			doc, err := Parse([]byte(state), "w")
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Instances) != 1 {
				t.Fatalf("instances: %+v", doc.Instances)
			}
			if got := doc.Instances[0].Tags; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("tags: got %#v, want %#v", got, tt.want)
			}
			if doc.Instances[0].ID == "" {
				t.Error("the ID is lost")
			}
		})
	}
}
