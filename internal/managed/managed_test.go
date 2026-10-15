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
