// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
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
