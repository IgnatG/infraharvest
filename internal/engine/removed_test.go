// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRemovedFrom(t *testing.T) {
	t.Parallel()
	for address, want := range map[string]string{
		"aws_subnet.a":                         "aws_subnet.a",
		"aws_subnet.a[0]":                      "aws_subnet.a",
		`aws_subnet.a["x"]`:                    "aws_subnet.a",
		"module.vpc.aws_subnet.a[0]":           "module.vpc",
		`module.vpc["x"].aws_subnet.a`:         "module.vpc",
		"module.vpc.module.inner.aws_subnet.a": "module.vpc",
		"module.vpc":                           "module.vpc",
	} {
		if got := RemovedFrom(address); got != want {
			t.Errorf("RemovedFrom(%q) = %q, want %q", address, got, want)
		}
	}
}

// moves.tf has the moved blocks, then a removed block, with destroy =
// false, for each resource or module call it hands over, sorted, and
// reads them back.
func TestWriteAndReadRemoved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	moved := []Move{{From: "aws_sqs_queue.a", To: "aws_sqs_queue.b"}}
	removed := []Removal{{From: "module.vpc"}, {From: "aws_subnet.a"}, {From: "aws_subnet.a"}}
	if err := writeMoves(dir, moved, removed); err != nil {
		t.Fatal(err)
	}
	want := `moved {
  from = aws_sqs_queue.a
  to   = aws_sqs_queue.b
}

removed {
  from = aws_subnet.a

  lifecycle {
    destroy = false
  }
}

removed {
  from = module.vpc

  lifecycle {
    destroy = false
  }
}
`
	if got := readFile(t, dir, MovesFileName); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	got, err := ReadRemoved(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Removal{{From: "aws_subnet.a"}, {From: "module.vpc"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("read back %+v, want %+v", got, want)
	}
	if moves, err := ReadMoves(dir); err != nil || !reflect.DeepEqual(moves, moved) {
		t.Errorf("moved blocks read back %+v, %v, want %+v", moves, err, moved)
	}

	// With removed blocks alone.
	if err := writeMoves(dir, nil, []Removal{{From: "aws_subnet.a"}}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dir, MovesFileName); !strings.HasPrefix(got, "removed {\n") {
		t.Errorf("got:\n%s", got)
	}
}

// A removed block of moves.tf is kept while the root doesn't have what
// it removes again.
func TestRemovals(t *testing.T) {
	t.Parallel()
	previous := &Previous{Removed: []Removal{{From: "aws_subnet.b"}, {From: "aws_subnet.a"}, {From: "module.vpc"}}}
	now := []Managed{{Type: "aws_subnet", ID: "b", Address: "aws_subnet.b[0]"}, {Type: "aws_vpc", ID: "v", Address: "aws_vpc.main"}}
	if got, want := removals(previous, now), []Removal{{From: "aws_subnet.a"}, {From: "module.vpc"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := removals(nil, now); got != nil {
		t.Errorf("without a previous root: %+v", got)
	}
}

// A root generated again that hands a resource over to another root gets
// a removed block for it, and no resource takes its address; a root that
// imports resources another hands over says where each comes from.
func TestGenerateHandsOverWithRemovedBlocks(t *testing.T) {
	t.Parallel()
	previous := &Previous{
		Addresses: []Managed{
			{Type: "aws_sqs_queue", ID: "a", Address: "aws_sqs_queue.orders"},
			{Type: "aws_sqs_queue", ID: "b", Address: "aws_sqs_queue.jobs"},
		},
		Removed: []Removal{{From: "aws_sqs_queue.jobs", Resources: []Departure{{Type: "aws_sqs_queue", ID: "b", Address: "aws_sqs_queue.jobs", Root: "aws/prod"}}}},
	}
	// A new queue named after the one that goes doesn't take its address.
	imports := []Import{{Type: "aws_sqs_queue", Name: "Orders Queue", ID: "a"}, {Type: "aws_sqs_queue", Name: "Jobs Queue", ID: "c"}}

	dir, result := generateAgain(t, imports, previous)

	if got := addressesOf(result); got["a"] != "aws_sqs_queue.orders" || got["c"] == "aws_sqs_queue.jobs" {
		t.Errorf("addresses: %v, want orders kept and jobs left to the removed block", got)
	}
	if !reflect.DeepEqual(result.Removed, previous.Removed) || len(result.Moves) != 0 {
		t.Errorf("removed %+v, moves %+v, want the removed block alone", result.Removed, result.Moves)
	}
	if got := readFile(t, dir, MovesFileName); !strings.Contains(got, "removed {\n  from = aws_sqs_queue.jobs\n\n  lifecycle {\n    destroy = false\n  }\n}\n") {
		t.Errorf("%s:\n%s", MovesFileName, got)
	}
	readme := readFile(t, dir, ReadmeFileName)
	for _, want := range []string{"## Moves", "| `aws_sqs_queue.jobs` | `aws/prod` |", "Apply this root first", "or as forgotten, not destroyed, for those `moves.tf` removes", "before applying the roots those it removes go to", "Delete `imports.tf` and `moves.tf`"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md misses %q:\n%s", want, readme)
		}
	}
	if !result.Gate.check(CheckMoves).Passed {
		t.Errorf("moves check: %+v", result.Gate.check(CheckMoves))
	}
}

func TestGenerateRecordsArrivals(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tf := &fakeTerraform{dir: dir}
	imports := []Import{{Type: "aws_sqs_queue", Name: "Jobs Queue", ID: "b"}, {Type: "aws_sqs_queue", Name: "Orders Queue", ID: "a"}}
	arrivals := []Arrival{{Type: "aws_sqs_queue", ID: "b", Root: "aws/all", From: "aws_sqs_queue.jobs"}, {Type: "aws_sqs_queue", ID: "gone", Root: "aws/all", From: "aws_sqs_queue.gone"}}
	result, err := Generate(t.Context(), tf, dir, imports, Options{TidyLabels: true, Arrivals: arrivals})
	if err != nil {
		t.Fatal(err)
	}
	want := []Arrival{{Type: "aws_sqs_queue", ID: "b", Root: "aws/all", From: "aws_sqs_queue.jobs", To: addressesOf(result)["b"]}}
	if !reflect.DeepEqual(result.Arrived, want) {
		t.Errorf("arrived %+v, want %+v", result.Arrived, want)
	}
	readme := readFile(t, dir, ReadmeFileName)
	for _, want := range []string{"## Moves", "| `" + want[0].To + "` | `aws/all` | `aws_sqs_queue.jobs` |", "Apply the root each comes from first", "after applying the roots the resources it imports from other roots come from"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md misses %q:\n%s", want, readme)
		}
	}
	if exists(filepath.Join(dir, MovesFileName)) {
		t.Errorf("%s written for a root that only imports", MovesFileName)
	}
}

// A root all of whose resources go to other roots keeps its versions.tf
// and providers.tf, gets moves.tf with their removed blocks, which need
// Terraform 1.7 and OpenTofu 1.10, and a README that says to apply it
// first.
func TestVacate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	opts := Options{
		Config: map[string][]byte{
			VersionsFileName:  VersionsFile(">= 1.16, < 2.0", Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.14"}),
			ProvidersFileName: []byte("provider \"aws\" {\n  region = \"eu-west-1\"\n}\n"),
		},
		Previous: &Previous{
			Addresses: []Managed{{Type: "aws_subnet", ID: "s", Address: "aws_subnet.a[0]"}},
			Removed:   []Removal{{From: "aws_subnet.a", Resources: []Departure{{Type: "aws_subnet", ID: "s", Address: "aws_subnet.a[0]", Root: "aws/prod"}}}},
		},
	}
	result, err := Vacate(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Removed, opts.Previous.Removed) || len(result.Imported) != 0 {
		t.Errorf("result: %+v", result)
	}
	if !result.Gate.Passed() || result.Gate.check(CheckMoves).Name != CheckMoves {
		t.Errorf("gate: %+v", result.Gate)
	}
	if got := readFile(t, dir, VersionsFileName); !strings.Contains(got, `required_version = ">= 1.10, < 2.0"`) {
		t.Errorf("%s:\n%s", VersionsFileName, got)
	}
	if got := readFile(t, dir, ProvidersFileName); got != string(opts.Config[ProvidersFileName]) {
		t.Errorf("%s:\n%s", ProvidersFileName, got)
	}
	if got := readFile(t, dir, MovesFileName); !strings.Contains(got, "from = aws_subnet.a\n") {
		t.Errorf("%s:\n%s", MovesFileName, got)
	}
	readme := readFile(t, dir, ReadmeFileName)
	for _, want := range []string{"# Handed over resources", "before applying the roots the resources go to", "| `aws_subnet.a` | `aws/prod` |", "G8 moves"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md misses %q:\n%s", want, readme)
		}
	}

	if _, err := Vacate(t.TempDir(), Options{Previous: &Previous{}}); err == nil {
		t.Error("want an error without removed blocks")
	}
	moving := *opts.Previous
	moving.Moved = []Move{{From: "aws_subnet.x", To: "aws_subnet.a"}}
	if _, err := Vacate(t.TempDir(), Options{Previous: &moving}); err == nil || !strings.Contains(err.Error(), "still moves") {
		t.Errorf("want an error for moved blocks still there, got %v", err)
	}
}
