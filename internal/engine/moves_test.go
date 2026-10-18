// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// generateAgain generates the queues of imports into a new directory, as
// a root an earlier import generated with previous, and returns the
// directory and the result.
func generateAgain(t *testing.T, imports []Import, previous *Previous) (string, *Result) {
	t.Helper()
	dir := t.TempDir()
	tf := &fakeTerraform{dir: dir}
	result, err := Generate(context.Background(), tf, dir, imports, Options{TidyLabels: true, Previous: previous})
	if err != nil {
		t.Fatal(err)
	}
	return dir, result
}

// addressesOf returns where result imports each resource, by import ID.
func addressesOf(result *Result) map[string]string {
	at := map[string]string{}
	for _, m := range result.Addresses {
		at[m.ID] = m.Address
	}
	return at
}

// A resource keeps its label when others are added: the only queue of a
// root, main, stays main when a second one appears, and nothing moves.
func TestGenerateKeepsLabelsWhenResourcesAreAdded(t *testing.T) {
	t.Parallel()
	previous := &Previous{Addresses: []Managed{{Type: "aws_sqs_queue", ID: "a", Address: "aws_sqs_queue.main"}}}
	imports := []Import{{Type: "aws_sqs_queue", Name: "Orders Queue", ID: "a"}, {Type: "aws_sqs_queue", Name: "Jobs Queue", ID: "b"}}

	dir, result := generateAgain(t, imports, previous)

	if got, want := addressesOf(result), map[string]string{"a": "aws_sqs_queue.main", "b": "aws_sqs_queue.jobs"}; !reflect.DeepEqual(got, want) {
		t.Errorf("addresses: got %v, want %v", got, want)
	}
	if len(result.Moves) != 0 || len(result.Unmoved) != 0 {
		t.Errorf("moves %+v, unmoved %q, want none", result.Moves, result.Unmoved)
	}
	if exists(filepath.Join(dir, MovesFileName)) {
		t.Errorf("%s written without moves", MovesFileName)
	}
	if readme := readFile(t, dir, ReadmeFileName); strings.Contains(readme, "## Moves") || strings.Contains(readme, MovesFileName) {
		t.Errorf("README.md speaks of moves:\n%s", readme)
	}
}

// A resource renamed in the cloud gets a label from its new name, and a
// moved block from where it was; the others keep theirs.
func TestGenerateMovesWhatWasRenamed(t *testing.T) {
	t.Parallel()
	previous := &Previous{Addresses: []Managed{
		{Type: "aws_sqs_queue", ID: "a", Address: "aws_sqs_queue.orders"},
		{Type: "aws_sqs_queue", ID: "b", Address: "aws_sqs_queue.jobs"},
	}}
	imports := []Import{{Type: "aws_sqs_queue", Name: "Billing Queue", ID: "a"}, {Type: "aws_sqs_queue", Name: "Jobs Queue", ID: "b"}}

	dir, result := generateAgain(t, imports, previous)

	if got, want := addressesOf(result), map[string]string{"a": "aws_sqs_queue.billing", "b": "aws_sqs_queue.jobs"}; !reflect.DeepEqual(got, want) {
		t.Errorf("addresses: got %v, want %v", got, want)
	}
	if want := []Move{{From: "aws_sqs_queue.orders", To: "aws_sqs_queue.billing"}}; !reflect.DeepEqual(result.Moves, want) {
		t.Errorf("moves: got %+v, want %+v", result.Moves, want)
	}
	want := `moved {
  from = aws_sqs_queue.orders
  to   = aws_sqs_queue.billing
}
`
	if got := readFile(t, dir, MovesFileName); got != want {
		t.Errorf("%s:\n%s\nwant:\n%s", MovesFileName, got, want)
	}
	readme := readFile(t, dir, ReadmeFileName)
	for _, want := range []string{"## Moves", "| `aws_sqs_queue.orders` | `aws_sqs_queue.billing` |", "or as moved for those `moves.tf` moves", "Delete `imports.tf` and `moves.tf`"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md misses %q:\n%s", want, readme)
		}
	}
}

// Two resources whose names swapped don't take each other's addresses:
// a moved block onto an address another resource still has would be
// rejected, and a swap followed as a chain.
func TestGenerateAvoidsSwappingAddresses(t *testing.T) {
	t.Parallel()
	previous := &Previous{Addresses: []Managed{
		{Type: "aws_sqs_queue", ID: "a", Address: "aws_sqs_queue.orders"},
		{Type: "aws_sqs_queue", ID: "b", Address: "aws_sqs_queue.billing"},
	}}
	imports := []Import{{Type: "aws_sqs_queue", Name: "Billing Queue", ID: "a"}, {Type: "aws_sqs_queue", Name: "Orders Queue", ID: "b"}}

	_, result := generateAgain(t, imports, previous)

	if got, want := addressesOf(result), map[string]string{"a": "aws_sqs_queue.billing_2", "b": "aws_sqs_queue.orders_2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("addresses: got %v, want %v", got, want)
	}
	want := []Move{
		{From: "aws_sqs_queue.orders", To: "aws_sqs_queue.billing_2"},
		{From: "aws_sqs_queue.billing", To: "aws_sqs_queue.orders_2"},
	}
	if !reflect.DeepEqual(result.Moves, want) || len(result.Unmoved) != 0 {
		t.Errorf("moves: got %+v, %q, want %+v", result.Moves, result.Unmoved, want)
	}
}

// The moved blocks of a moves.tf still there are kept, and a resource
// that moves again moves on from where they took it; no resource takes
// an address they move from.
func TestGenerateChainsTheMovesOfMovesTF(t *testing.T) {
	t.Parallel()
	previous := &Previous{
		Addresses: []Managed{
			{Type: "aws_sqs_queue", ID: "a", Address: "aws_sqs_queue.billing"},
			{Type: "aws_sqs_queue", ID: "b", Address: "aws_sqs_queue.jobs"},
		},
		Moved: []Move{{From: "aws_sqs_queue.orders", To: "aws_sqs_queue.billing"}},
	}
	imports := []Import{
		{Type: "aws_sqs_queue", Name: "Invoices Queue", ID: "a"},
		{Type: "aws_sqs_queue", Name: "Jobs Queue", ID: "b"},
		{Type: "aws_sqs_queue", Name: "Orders Queue", ID: "c"},
	}

	dir, result := generateAgain(t, imports, previous)

	if got, want := addressesOf(result), map[string]string{"a": "aws_sqs_queue.invoices", "b": "aws_sqs_queue.jobs", "c": "aws_sqs_queue.orders_2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("addresses: got %v, want %v", got, want)
	}
	want := `moved {
  from = aws_sqs_queue.orders
  to   = aws_sqs_queue.billing
}

moved {
  from = aws_sqs_queue.billing
  to   = aws_sqs_queue.invoices
}
`
	if got := readFile(t, dir, MovesFileName); got != want {
		t.Errorf("%s:\n%s\nwant:\n%s", MovesFileName, got, want)
	}
}

// Moved blocks that move a resource in a cycle fail the root's moves: it
// is to be applied, and moves.tf deleted, first.
func TestGenerateFailsTheMovesOfACycle(t *testing.T) {
	t.Parallel()
	previous := &Previous{
		Addresses: []Managed{{Type: "aws_sqs_queue", ID: "a", Address: "aws_sqs_queue.jobs"}},
		Moved:     []Move{{From: "aws_sqs_queue.x", To: "aws_sqs_queue.y"}, {From: "aws_sqs_queue.y", To: "aws_sqs_queue.x"}},
	}

	dir, result := generateAgain(t, []Import{{Type: "aws_sqs_queue", Name: "Jobs Queue", ID: "a"}}, previous)

	if len(result.Moves) != 0 || exists(filepath.Join(dir, MovesFileName)) {
		t.Errorf("moves %+v written for a cycle", result.Moves)
	}
	if len(result.Unmoved) != 1 || !strings.Contains(result.Unmoved[0], "come back") || !strings.Contains(result.Unmoved[0], "delete moves.tf") {
		t.Errorf("unmoved %q, want the cycle", result.Unmoved)
	}
}

// Resources move into and out of module calls, keys and all, but not
// into or out of a module of another package, which moved blocks can't
// reach, nor onto an address another resource had, such as one deleted in
// the cloud since, which its state still has.
func TestMoves(t *testing.T) {
	t.Parallel()
	previous := &Previous{Addresses: []Managed{
		{Type: "aws_s3_bucket", ID: "logs", Address: "aws_s3_bucket.logs"},
		{Type: "aws_s3_bucket", ID: "state", Address: "module.old.aws_s3_bucket.this"},
		{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.a"},
		{Type: "aws_vpc", ID: "vpc-1", Address: "aws_vpc.main"},
		{Type: "aws_route_table", ID: "rtb-1", Address: "module.vpc.aws_route_table.public[0]"},
		{Type: "aws_sqs_queue", ID: "q-1", Address: "aws_sqs_queue.a"},
		{Type: "aws_sqs_queue", ID: "q-2", Address: "aws_sqs_queue.b"},
		{Type: "aws_sqs_queue", ID: "q-3", Address: "module.jobs", Unverified: true},
	}}
	now := []Managed{
		{Type: "aws_s3_bucket", ID: "logs", Address: "module.logs.aws_s3_bucket.this"},
		{Type: "aws_s3_bucket", ID: "state", Address: "aws_s3_bucket.state"},
		{Type: "aws_subnet", ID: "subnet-1", Address: `module.subnets.aws_subnet.this["a"]`},
		{Type: "aws_vpc", ID: "vpc-1", Address: "module.vpc.aws_vpc.this[0]"},
		{Type: "aws_route_table", ID: "rtb-1", Address: "aws_route_table.public"},
		{Type: "aws_sqs_queue", ID: "q-1", Address: "aws_sqs_queue.b"},
		{Type: "aws_sqs_queue", ID: "q-3", Address: "aws_sqs_queue.jobs"},
	}
	sources := map[string]string{"logs": "../../modules/bucket", "subnets": "./modules/subnets", "vpc": "terraform-aws-modules/vpc/aws"}

	moved, unmoved := moves(previous, now, sources)

	want := []Move{
		{From: "module.old.aws_s3_bucket.this", To: "aws_s3_bucket.state"},
		{From: "aws_s3_bucket.logs", To: "module.logs.aws_s3_bucket.this"},
		{From: "aws_subnet.a", To: `module.subnets.aws_subnet.this["a"]`},
	}
	if !reflect.DeepEqual(moved, want) {
		t.Errorf("moves: got %+v, want %+v", moved, want)
	}
	wantUnmoved := []string{
		"aws_vpc.main is now module.vpc.aws_vpc.this[0], and a moved block can't move it into module.vpc (terraform-aws-modules/vpc/aws)",
		"module.vpc.aws_route_table.public[0] is now aws_route_table.public, and a moved block can't move it out of module.vpc (terraform-aws-modules/vpc/aws)",
		"aws_sqs_queue.a is now aws_sqs_queue.b, where another of its resources was",
	}
	if !reflect.DeepEqual(unmoved, wantUnmoved) {
		t.Errorf("unmoved: got %q, want %q", unmoved, wantUnmoved)
	}
	if moved, unmoved := moves(nil, now, sources); moved != nil || unmoved != nil {
		t.Errorf("without a previous root: %+v, %q", moved, unmoved)
	}
}

func TestConflictingMoves(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		moved    []Move
		declared string
		want     string
	}{
		"a chain":           {[]Move{{"a.x", "a.y"}, {"a.y", "a.z"}}, "a.z", ""},
		"one moved twice":   {[]Move{{"a.x", "a.y"}, {"a.x", "a.z"}}, "", "a.x moves both to a.y and to a.z"},
		"two onto one":      {[]Move{{"a.x", "a.z"}, {"a.y", "a.z"}}, "", "more than one resource moves to a.z"},
		"from what is here": {[]Move{{"a.x", "a.y"}}, "a.x", "a.x moves to a.y, but a resource is at a.x still"},
		"a cycle":           {[]Move{{"a.x", "a.y"}, {"a.y", "a.x"}}, "", "the moves from a.x come back to a.x"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := conflictingMoves(tt.moved, map[string]bool{tt.declared: tt.declared != ""}); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// moves.tf has the moved blocks sorted by where they move to, keys and
// all, and reads back the same.
func TestWriteAndReadMoves(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got, err := ReadMoves(dir); err != nil || got != nil {
		t.Fatalf("without moves.tf: %+v, %v", got, err)
	}
	moved := []Move{
		{From: "aws_subnet.a", To: `module.subnets.aws_subnet.this["a"]`},
		{From: "aws_s3_bucket.logs", To: "module.logs.aws_s3_bucket.this[0]"},
	}
	if err := writeMoves(dir, moved); err != nil {
		t.Fatal(err)
	}
	want := `moved {
  from = aws_s3_bucket.logs
  to   = module.logs.aws_s3_bucket.this[0]
}

moved {
  from = aws_subnet.a
  to   = module.subnets.aws_subnet.this["a"]
}
`
	if got := readFile(t, dir, MovesFileName); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	got, err := ReadMoves(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Move{moved[1], moved[0]}; !reflect.DeepEqual(got, want) {
		t.Errorf("read back %+v, want %+v", got, want)
	}
	if err := writeMoves(dir, nil); err != nil || exists(filepath.Join(dir, MovesFileName)) {
		t.Errorf("without moves: %v, want moves.tf removed", err)
	}
}

func TestKeepsLabel(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		old, base string
		tidy      bool
		want      bool
	}{
		{"orders", "orders", true, true},
		{"orders_2", "orders", true, true},
		{"web", "shop_prod_web", true, true},
		{"web_3", "shop_prod_web", true, true},
		{"main", "orders", true, true},
		{"main", "orders", false, false},
		{"orders", "billing", true, false},
		{"orders_x", "orders", true, false},
	} {
		if got := keepsLabel(tt.old, tt.base, tt.tidy); got != tt.want {
			t.Errorf("keepsLabel(%q, %q, %v) = %v, want %v", tt.old, tt.base, tt.tidy, got, tt.want)
		}
	}
}

// A resource keeps the label an earlier import gave it before it moved
// into a module call, and no resource takes an address another had.
func TestRelabelledKeepsLabelsAndReservedAddresses(t *testing.T) {
	t.Parallel()
	previous := &Previous{
		Addresses: []Managed{
			{Type: "aws_s3_bucket", ID: "logs", Address: "module.logs.aws_s3_bucket.this"},
			{Type: "aws_s3_bucket", ID: "old", Address: "aws_s3_bucket.state"},
		},
		Labels: map[External]string{{Type: "aws_s3_bucket", ID: "logs"}: "logs"},
	}
	imports := []Import{
		{Type: "aws_s3_bucket", Name: "logs", ID: "logs"},
		{Type: "aws_s3_bucket", Name: "state", ID: "new"},
		{Type: "aws_s3_bucket", Name: "data", ID: "data"},
	}
	got := relabelled(imports, nil, true, previous)
	want := []Import{
		{Type: "aws_s3_bucket", Name: "data", ID: "data"},
		{Type: "aws_s3_bucket", Name: "logs", ID: "logs"},
		{Type: "aws_s3_bucket", Name: "state_2", ID: "new"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := relabelled(imports, nil, true, nil); !reflect.DeepEqual(got, labelled(imports, nil, true)) {
		t.Errorf("without a previous root: %+v", got)
	}
}
