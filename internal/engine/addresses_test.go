// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The addresses come from the import blocks, module calls and index keys
// included, by type and import ID; a root without imports.tf has none.
func TestImportedAddresses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got, err := ImportedAddresses(dir); err != nil || got != nil {
		t.Fatalf("without imports.tf: %v, %v", got, err)
	}
	imports := `import {
  to = module.logs.aws_s3_bucket.this[0]
  id = "logs"
}

import {
  to = aws_s3_bucket_versioning.logs
  id = "logs"
}

import {
  to = aws_sqs_queue.jobs
  id = "https://sqs.eu-west-2.amazonaws.com/111122223333/jobs"
}
`
	if err := os.WriteFile(filepath.Join(dir, ImportsFileName), []byte(imports), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ImportedAddresses(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []Managed{
		{Type: "aws_s3_bucket", ID: "logs", Address: "module.logs.aws_s3_bucket.this[0]"},
		{Type: "aws_s3_bucket_versioning", ID: "logs", Address: "aws_s3_bucket_versioning.logs"},
		{Type: "aws_sqs_queue", ID: "https://sqs.eu-west-2.amazonaws.com/111122223333/jobs", Address: "aws_sqs_queue.jobs"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A result without addresses, as an older release recorded it: resources
// moved into a module call are at that call, the others at their labels.
func TestInferredAddresses(t *testing.T) {
	t.Parallel()
	result := &Result{
		Imported: []Import{{Type: "aws_s3_bucket", Name: "logs", ID: "logs"}, {Type: "aws_sqs_queue", Name: "jobs", ID: "q"}, {Type: "aws_s3_bucket", Name: "state", ID: "state"}},
		Modules: []ModuleCall{
			{Name: "logs", Resources: []string{"aws_s3_bucket.logs"}},
			{Name: "state", Resources: []string{"aws_s3_bucket.state"}, Declined: "the plan changed"},
		},
	}
	want := []Managed{
		{Type: "aws_s3_bucket", ID: "logs", Address: "module.logs", Unverified: true},
		{Type: "aws_s3_bucket", ID: "state", Address: "aws_s3_bucket.state", Unverified: true},
		{Type: "aws_sqs_queue", ID: "q", Address: "aws_sqs_queue.jobs", Unverified: true},
	}
	if got := InferredAddresses(result); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := InferredAddresses(nil); got != nil {
		t.Errorf("without a result: %+v", got)
	}
}

func TestReaddressed(t *testing.T) {
	t.Parallel()
	previous := []Managed{
		{Type: "aws_sqs_queue", ID: "a", Address: "aws_sqs_queue.main"},
		{Type: "aws_sqs_queue", ID: "b", Address: "aws_sqs_queue.orders"},
		{Type: "aws_sqs_queue", ID: "gone", Address: "aws_sqs_queue.gone"},
		{Type: "aws_s3_bucket", ID: "logs", Address: "module.logs", Unverified: true},
		{Type: "aws_s3_bucket", ID: "state", Address: "module.state", Unverified: true},
	}
	now := []Managed{
		{Type: "aws_sqs_queue", ID: "a", Address: "aws_sqs_queue.jobs"},
		{Type: "aws_sqs_queue", ID: "b", Address: "aws_sqs_queue.orders"},
		{Type: "aws_sqs_queue", ID: "new", Address: "aws_sqs_queue.new"},
		{Type: "aws_s3_bucket", ID: "logs", Address: "module.logs.aws_s3_bucket.this[0]"},
		{Type: "aws_s3_bucket", ID: "state", Address: "aws_s3_bucket.state"},
	}
	want := []string{"aws_sqs_queue.main is now aws_sqs_queue.jobs", "module.state is now aws_s3_bucket.state"}
	if got := Readdressed(previous, now); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := Readdressed(previous, previous); len(got) != 0 {
		t.Errorf("the same addresses: %q", got)
	}
}

// What an incremental import adds joins the addresses the root recorded;
// a root recorded without them, by an older release, stays without.
func TestResultWithAddresses(t *testing.T) {
	t.Parallel()
	added := &Result{
		Imported:  []Import{{Type: "aws_subnet", Name: "a", ID: "subnet-1"}},
		Addresses: []Managed{{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.a"}},
	}
	previous := &Result{
		Imported:  []Import{{Type: "aws_vpc", Name: "main", ID: "vpc-1"}},
		Addresses: []Managed{{Type: "aws_vpc", ID: "vpc-1", Address: "aws_vpc.main"}},
	}
	want := []Managed{{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.a"}, {Type: "aws_vpc", ID: "vpc-1", Address: "aws_vpc.main"}}
	if got := previous.With(added).Addresses; !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	older := &Result{Imported: previous.Imported}
	if got := older.With(added).Addresses; got != nil {
		t.Errorf("added to a root recorded without addresses: %+v", got)
	}
	empty := &Result{}
	if got := empty.With(added).Addresses; !reflect.DeepEqual(got, added.Addresses) {
		t.Errorf("added to a root that imports nothing: %+v", got)
	}
}
