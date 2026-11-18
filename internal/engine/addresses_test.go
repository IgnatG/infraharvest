// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
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
	if err := os.WriteFile(filepath.Join(dir, rootfile.ImportsFileName), []byte(imports), 0o644); err != nil {
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
		Imported: []rootfile.Import{{Type: "aws_s3_bucket", Name: "logs", ID: "logs"}, {Type: "aws_sqs_queue", Name: "jobs", ID: "q"}, {Type: "aws_s3_bucket", Name: "state", ID: "state"}},
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
	if got := Readdressed(previous, now, nil); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := Readdressed(previous, previous, nil); len(got) != 0 {
		t.Errorf("the same addresses: %q", got)
	}
	// A moved block follows a resource to its new address.
	moved := []rootfile.Move{{From: "aws_sqs_queue.main", To: "aws_sqs_queue.jobs"}}
	if got, want := Readdressed(previous, now, moved), []string{"module.state is now aws_s3_bucket.state"}; !reflect.DeepEqual(got, want) {
		t.Errorf("with moved blocks: got %q, want %q", got, want)
	}
}

// A resource now of another type with the same import ID is retyped,
// unless that type is another name of its own (see Retype); one that
// shares its ID with a resource of another type in both isn't.
func TestRetyped(t *testing.T) {
	t.Parallel()
	previous := []Managed{
		{Type: "aws_alb", ID: "arn:lb", Address: "aws_alb.web"},
		{Type: "aws_s3_bucket", ID: "data", Address: "aws_s3_bucket.data"},
		{Type: "aws_s3_bucket", ID: "logs", Address: "aws_s3_bucket.logs"},
		{Type: "aws_s3_bucket_versioning", ID: "logs", Address: "aws_s3_bucket_versioning.logs"},
		{Type: "aws_sqs_queue", ID: "gone", Address: "aws_sqs_queue.gone"},
	}
	now := []Managed{
		{Type: "aws_lb", ID: "arn:lb", Address: "aws_lb.web"},
		{Type: "aws_s3_bucket_policy", ID: "data", Address: "aws_s3_bucket_policy.data"},
		{Type: "aws_s3_bucket", ID: "logs", Address: "aws_s3_bucket.logs"},
		{Type: "aws_s3_bucket_versioning", ID: "logs", Address: "aws_s3_bucket_versioning.logs"},
	}
	want := []string{"aws_s3_bucket.data is now aws_s3_bucket_policy.data, of another type, not one infraharvest knows as another name of aws_s3_bucket"}
	if got := Retyped(previous, now); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// What an incremental import adds joins the addresses the root recorded;
// a root recorded without them, by an older release, stays without.
func TestResultWithAddresses(t *testing.T) {
	t.Parallel()
	added := &Result{
		Imported:  []rootfile.Import{{Type: "aws_subnet", Name: "a", ID: "subnet-1"}},
		Addresses: []Managed{{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.a"}},
	}
	previous := &Result{
		Imported:  []rootfile.Import{{Type: "aws_vpc", Name: "main", ID: "vpc-1"}},
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

// The type of a resource is found past its module calls, whose instance
// keys may hold dots, brackets and quotes, and a data source's after
// data; a module call alone has none.
func TestAddressType(t *testing.T) {
	t.Parallel()
	for address, want := range map[string]string{
		"aws_s3_bucket.logs":                                 "aws_s3_bucket",
		"aws_s3_bucket.logs[0]":                              "aws_s3_bucket",
		`aws_sqs_queue.this["orders.dlq"]`:                   "aws_sqs_queue",
		"data.aws_iam_policy_document.p":                     "aws_iam_policy_document",
		"module.logs.aws_s3_bucket.this[0]":                  "aws_s3_bucket",
		"module.logs.data.aws_iam_policy_document.p":         "aws_iam_policy_document",
		`module.logs["a.b"].aws_s3_bucket.this`:              "aws_s3_bucket",
		`module.x["a.b"].module.y[1].aws_sqs_queue.q["c.d"]`: "aws_sqs_queue",
		`module.x["say \"a.b\" ]"].aws_iam_role.this`:        "aws_iam_role",
		`module.app["a]\".b"].module.inner.aws_s3_bucket.x`:  "aws_s3_bucket",
		`module.app["a[b"].aws_iam_role.this`:                "aws_iam_role",
		"module.app":                                         "",
		`module.app["a.b"]`:                                  "",
		"module.app.module.inner":                            "",
		"":                                                   "",
	} {
		if got := AddressType(address); got != want {
			t.Errorf("AddressType(%q) = %q, want %q", address, got, want)
		}
	}
}

// A finding is about the resource at its address, or the only one at it
// but for instance keys, or the only one in the module call it names.
func TestResourceAt(t *testing.T) {
	t.Parallel()
	managed := []Managed{
		{Type: "aws_s3_bucket", ID: "acme-logs", Address: "aws_s3_bucket.logs"},
		{Type: "aws_s3_bucket", ID: "acme-data", Address: "module.data.aws_s3_bucket.this[0]"},
		{Type: "aws_iam_role", ID: "a", Address: `module.roles["a"].aws_iam_role.this`},
		{Type: "aws_iam_role", ID: "b", Address: `module.roles["b"].aws_iam_role.this`},
	}
	for address, want := range map[string]string{
		"aws_s3_bucket.logs":                  "acme-logs",
		"module.data.aws_s3_bucket.this[0]":   "acme-data",
		"module.data.aws_s3_bucket.this":      "acme-data",
		"module.data":                         "acme-data",
		`module.roles["a"].aws_iam_role.this`: "a",
		"module.roles.aws_iam_role.this":      "",
		"module.roles":                        "",
		"aws_s3_bucket.other":                 "",
		"":                                    "",
	} {
		m, ok := NewManagedIndex(managed).At(address)
		if ok != (want != "") || m.ID != want {
			t.Errorf("%q: got %+v, %v, want %q", address, m, ok, want)
		}
	}
}

// resourceAtByScan is what ManagedIndex.At answered as it read every
// resource for each address, before ManagedIndex.
func resourceAtByScan(managed []Managed, address string) (Managed, bool) {
	if address == "" {
		return Managed{}, false
	}
	for _, m := range managed {
		if m.Address == address {
			return m, true
		}
	}
	bare := strings.Join(hclfile.AddressNames(address), ".")
	one := func(match func(string) bool) (Managed, bool) {
		var found []Managed
		for _, m := range managed {
			if match(strings.Join(hclfile.AddressNames(m.Address), ".")) {
				found = append(found, m)
			}
		}
		if len(found) == 1 {
			return found[0], true
		}
		return Managed{}, false
	}
	if m, ok := one(func(a string) bool { return a == bare }); ok {
		return m, true
	}
	if hclfile.NamesModuleCall(address) {
		return one(func(a string) bool { return strings.HasPrefix(a, bare+".") })
	}
	return Managed{}, false
}

// ManagedIndex answers as reading every resource for each address did,
// with resources at the same address, at the same address but for
// instance keys, and in module calls of one or several resources.
func TestManagedIndexAnswersAsAScan(t *testing.T) {
	t.Parallel()
	managed := []Managed{
		{Type: "aws_s3_bucket", ID: "logs", Address: "aws_s3_bucket.logs"},
		{Type: "aws_s3_bucket", ID: "logs-again", Address: "aws_s3_bucket.logs"},
		{Type: "aws_s3_bucket", ID: "q0", Address: "aws_sqs_queue.q[0]"},
		{Type: "aws_s3_bucket", ID: "q1", Address: "aws_sqs_queue.q[1]"},
		{Type: "aws_s3_bucket", ID: "data", Address: "module.data.aws_s3_bucket.this[0]"},
		{Type: "aws_iam_role", ID: "a", Address: `module.roles["a"].aws_iam_role.this`},
		{Type: "aws_iam_role", ID: "b", Address: `module.roles["b"].aws_iam_role.this`},
		{Type: "aws_vpc", ID: "vpc", Address: "module.net.aws_vpc.this"},
		{Type: "aws_subnet", ID: "s", Address: "module.net.aws_subnet.private[0]"},
		{Type: "aws_kms_key", ID: "k", Address: "module.outer.module.inner.aws_kms_key.this"},
	}
	index := NewManagedIndex(managed)
	for _, address := range []string{
		"", "aws_s3_bucket.logs", "aws_sqs_queue.q", "aws_sqs_queue.q[1]", "aws_sqs_queue.q[2]",
		"module.data", "module.data.aws_s3_bucket.this", "module.roles", `module.roles["a"]`,
		"module.roles.aws_iam_role.this", "module.net", "module.net.aws_vpc.this",
		"module.outer", "module.outer.module.inner", "aws_s3_bucket.other", "data.aws_caller_identity.this",
	} {
		want, wantOK := resourceAtByScan(managed, address)
		got, ok := index.At(address)
		if got != want || ok != wantOK {
			t.Errorf("%q: got %+v, %v, want %+v, %v", address, got, ok, want, wantOK)
		}
	}
}
