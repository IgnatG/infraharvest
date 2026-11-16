// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"bytes"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// driftConfigShape is what a driftConfig declares and refers to, by name,
// with the file each declaration is in.
func driftConfigShape(cfg *driftConfig) map[string]any {
	files := func(blocks map[string]driftBlock) map[string]string {
		out := map[string]string{}
		for name, b := range blocks {
			out[name] = b.file
		}
		return out
	}
	locals := map[string]string{}
	for name, l := range cfg.locals {
		locals[name] = l.file
	}
	return map[string]any{
		"files":     slices.Sorted(maps.Keys(cfg.files)),
		"blocks":    files(cfg.blocks),
		"calls":     files(cfg.calls),
		"locals":    locals,
		"variables": slices.Sorted(maps.Keys(cfg.variables)),
		"uses":      cfg.uses,
		"varUses":   cfg.varUses,
	}
}

// Check edits one change at a time, each found in the files as the edits
// before left them: only the files an edit changed are parsed again, and
// the configuration is the one a parse of every file gives.
func TestDriftConfigReparsesOnlyEditedFiles(t *testing.T) {
	t.Parallel()
	contents := byteFiles(map[string]string{
		"main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = var.name
  tags   = local.tags
}
`,
		"locals.tf":                            "locals {\n  tags = { Env = \"prod\" }\n}\n",
		"variables.tf":                         "variable \"name\" {\n  type = string\n}\n",
		"../../../modules/s3_0a1b2c3d/main.tf": "resource \"aws_s3_bucket\" \"this\" {}\n",
	})
	cfg, err := parseDriftConfig(contents)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := cfg.reparse(maps.Clone(contents)); err != nil || again != cfg {
		t.Errorf("reparsed files that didn't change: %v", err)
	}

	contents["main.tf"] = []byte(strings.Replace(string(contents["main.tf"]), "local.tags", "merge(local.tags, { Team = local.team })", 1))
	contents["locals.tf"] = []byte("locals {\n  tags = { Env = \"prod\" }\n  team = \"data\"\n}\n")
	delete(contents, "variables.tf")
	contents["outputs.tf"] = []byte("output \"name\" {\n  value = var.name\n}\n")
	next, err := cfg.reparse(contents)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main.tf", "locals.tf"} {
		if next.files[name] == cfg.files[name] {
			t.Errorf("%s wasn't parsed again", name)
		}
	}
	if _, ok := next.files["variables.tf"]; ok {
		t.Error("variables.tf, no longer there, is still parsed")
	}
	full, err := parseDriftConfig(contents)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := driftConfigShape(next), driftConfigShape(full); !reflect.DeepEqual(got, want) {
		t.Errorf("reparsed:\n%v\nwant:\n%v", got, want)
	}

	// An edit of one file leaves the others' parse as it was.
	contents["outputs.tf"] = []byte("output \"bucket\" {\n  value = aws_s3_bucket.logs.id\n}\n")
	last, err := next.reparse(contents)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main.tf", "locals.tf"} {
		if last.files[name] != next.files[name] {
			t.Errorf("%s was parsed again, unchanged", name)
		}
	}
}

// BenchmarkDriftApply accepts a change in each of the files of a root of
// many: the edits parse again only the file each changed.
func BenchmarkDriftApply(b *testing.B) {
	const files = 200
	contents := map[string][]byte{}
	after := map[string]map[string]any{}
	var changes []*Change
	for i := range files {
		name := fmt.Sprintf("bucket_%d", i)
		contents[name+".tf"] = fmt.Appendf(nil, "resource \"aws_s3_bucket\" %q {\n  bucket = %q\n  tags = {\n    Env = \"prod\"\n  }\n}\n", name, name)
		address := "aws_s3_bucket." + name
		after[address] = map[string]any{"tags": map[string]any{"Env": "dev"}}
		changes = append(changes, driftAt(address, "tags.Env", "dev"))
	}
	e := testEditor(after)
	b.ResetTimer()
	for range b.N {
		_, _, failed, err := e.apply(maps.Clone(contents), changes)
		if err != nil || len(failed) > 0 {
			b.Fatal(err, failed)
		}
	}
}

// BenchmarkDriftApplyOneFile is BenchmarkDriftApply with every resource,
// and every change, in one file: each change edits the same large file.
func BenchmarkDriftApplyOneFile(b *testing.B) {
	for _, n := range []int{500, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			contents, changes, e := oneFileDrift(n, 20)
			b.ResetTimer()
			for range b.N {
				_, _, failed, err := e.apply(maps.Clone(contents), changes)
				if err != nil || len(failed) > 0 {
					b.Fatal(err, failed)
				}
			}
		})
	}
}

// TestApplyKeepsParsesAlike checks that apply, which keeps the parse an
// edit's check made for the next change, edits the files as when each
// change is applied on its own, from a fresh parse: byte for byte, with
// changes that add arguments and keys as well as change values.
func TestApplyKeepsParsesAlike(t *testing.T) {
	contents, changes, e := oneFileDrift(60, 5)
	for i, c := range changes {
		after := e.resources[c.Address].after.(map[string]any)
		switch i % 3 {
		case 1:
			after["tags"] = map[string]any{"Env": "prod", "Team": "data", "Owner": "ops"}
			changes[i] = driftAt(c.Address, "tags.Owner", "ops")
		case 2:
			after["acl"] = "private"
			changes[i] = driftAt(c.Address, "acl", "private")
		}
	}
	together := maps.Clone(contents)
	names, applied, failed, err := e.apply(together, changes)
	if err != nil || len(failed) > 0 {
		t.Fatal(err, failed)
	}
	if len(applied) != len(changes) || !slices.Equal(names, []string{"main.tf"}) {
		t.Fatalf("applied %d of %d changes, edited %v", len(applied), len(changes), names)
	}
	alone := maps.Clone(contents)
	for _, c := range changes {
		if _, _, failed, err := e.apply(alone, []*Change{c}); err != nil || len(failed) > 0 {
			t.Fatal(err, failed)
		}
	}
	if !bytes.Equal(together["main.tf"], alone["main.tf"]) {
		t.Errorf("applied together:\n%s\napplied one at a time:\n%s", together["main.tf"], alone["main.tf"])
	}
	for _, want := range []string{`Env  = "dev"`, `Owner = "ops"`, `acl           = "private"`} {
		if !bytes.Contains(together["main.tf"], []byte(want)) {
			t.Errorf("no %s in the edited file", want)
		}
	}
}

// oneFileDrift returns a main.tf of n buckets, and a change to the tags
// of one in every step, with the editor that takes them.
func oneFileDrift(n, step int) (map[string][]byte, []*Change, *driftEditor) {
	var src []byte
	after := map[string]map[string]any{}
	var changes []*Change
	for i := range n {
		name := fmt.Sprintf("bucket_%d", i)
		src = fmt.Appendf(src, "resource \"aws_s3_bucket\" %q {\n  bucket        = %q\n  force_destroy = false\n  tags = {\n    Env  = \"prod\"\n    Team = \"data\"\n  }\n}\n\n", name, name)
		if i%step == 0 {
			address := "aws_s3_bucket." + name
			after[address] = map[string]any{"tags": map[string]any{"Env": "dev", "Team": "data"}}
			changes = append(changes, driftAt(address, "tags.Env", "dev"))
		}
	}
	return map[string][]byte{"main.tf": src}, changes, testEditor(after)
}
