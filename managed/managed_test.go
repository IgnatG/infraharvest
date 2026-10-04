// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package managed

import (
	"context"
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
	var region string
	store := fakeStore{
		"imported/iam/terraform.tfstate": `{"version": 4, "resources": [{"mode": "managed", "type": "aws_iam_role", "instances": [{"attributes": {"id": "app"}}]}]}`,
		"imported/iam/notes.txt":         "not state",
		"other/terraform.tfstate":        `{"version": 4, "resources": [{"mode": "managed", "type": "aws_iam_role", "instances": [{"attributes": {"id": "other"}}]}]}`,
	}

	r, err := Load(context.Background(), []string{dir, "s3://acme-state/imported/?region=eu-west-2"}, func(_ context.Context, rgn string) (ObjectStore, error) {
		region = rgn
		return store, nil
	})
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
	if region != "eu-west-2" {
		t.Errorf("region: %q", region)
	}
}

func TestLoadRejectsOtherFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.tfstate")
	if err := os.WriteFile(path, []byte(`{"version": 3, "modules": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), []string{path}, nil); err == nil || !strings.Contains(err.Error(), "version 4") {
		t.Errorf("want a version error, got %v", err)
	}
}
