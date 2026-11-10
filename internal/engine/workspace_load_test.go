// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// TestLoadKeepsEditsApart checks that two Loads of the same content share
// the parse but not the edits: an edit to the first, unsaved, doesn't show
// in the second, and a Save or a Write of new content is what the next
// Load reads.
func TestLoadKeepsEditsApart(t *testing.T) {
	ws := memoryWorkspace(map[string]string{
		rootfile.GeneratedFileName: "resource \"aws_vpc\" \"a\" {\n  cidr_block = \"10.0.0.0/16\"\n}\n",
	})
	load := func() *hclwrite.Body {
		t.Helper()
		f, err := ws.Load(rootfile.GeneratedFileName)
		if err != nil {
			t.Fatal(err)
		}
		return f.Edit().Body().Blocks()[0].Body()
	}
	cidr := func(body *hclwrite.Body) string {
		return strings.TrimSpace(string(body.GetAttribute("cidr_block").Expr().BuildTokens(nil).Bytes()))
	}

	first, err := ws.Load(rootfile.GeneratedFileName)
	if err != nil {
		t.Fatal(err)
	}
	first.Edit().Body().Blocks()[0].Body().SetAttributeValue("cidr_block", cty.StringVal("10.1.0.0/16"))
	if got := cidr(load()); got != `"10.0.0.0/16"` {
		t.Errorf("an unsaved edit shows in the next Load: cidr_block = %s", got)
	}
	second, err := ws.Load(rootfile.GeneratedFileName)
	if err != nil {
		t.Fatal(err)
	}
	if second.Syntax != first.Syntax {
		t.Error("the second Load of the same content parsed it again")
	}

	ws.Save(first)
	if got := cidr(load()); got != `"10.1.0.0/16"` {
		t.Errorf("after Save, Load reads cidr_block = %s", got)
	}

	ws.Write(rootfile.GeneratedFileName, []byte("resource \"aws_vpc\" \"a\" {\n  cidr_block = \"10.2.0.0/16\"\n}\n"))
	if got := cidr(load()); got != `"10.2.0.0/16"` {
		t.Errorf("after Write, Load reads cidr_block = %s", got)
	}

	snap := ws.Snapshot()
	ws.Write(rootfile.GeneratedFileName, []byte("resource \"aws_vpc\" \"a\" {\n  cidr_block = \"10.3.0.0/16\"\n}\n"))
	_ = load()
	ws.Restore(snap)
	if got := cidr(load()); got != `"10.2.0.0/16"` {
		t.Errorf("after Restore, Load reads cidr_block = %s", got)
	}
}

// TestResourceSyntax checks that ResourceSyntax yields what Resources
// does, in the same order.
func TestResourceSyntax(t *testing.T) {
	ws := memoryWorkspace(map[string]string{rootfile.GeneratedFileName: string(benchGenerated(30)) + "data \"aws_region\" \"r\" {}\n"})
	f, err := ws.Load(rootfile.GeneratedFileName)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for r := range f.ResourceSyntax() {
		got = append(got, r.Address)
	}
	var want []string
	for _, r := range f.Resources() {
		want = append(want, r.Address)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") || len(got) != 30 {
		t.Errorf("ResourceSyntax = %v, want %v", got, want)
	}
}

// loadBenchResources is the size of the generated.tf the Load benchmarks
// read: the 2,000 blocks of the review that measured it.
const loadBenchResources = 2000

// BenchmarkWorkspaceLoad is one pass's Load of an unchanged generated.tf.
func BenchmarkWorkspaceLoad(b *testing.B) {
	ws := memoryWorkspace(map[string]string{rootfile.GeneratedFileName: string(benchGenerated(loadBenchResources))})
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ws.Load(rootfile.GeneratedFileName); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWorkspaceLoadResources is a Load and a read of the resources
// through Resources, which builds the editable tree.
func BenchmarkWorkspaceLoadResources(b *testing.B) {
	ws := memoryWorkspace(map[string]string{rootfile.GeneratedFileName: string(benchGenerated(loadBenchResources))})
	b.ReportAllocs()
	for b.Loop() {
		f, err := ws.Load(rootfile.GeneratedFileName)
		if err != nil {
			b.Fatal(err)
		}
		if len(f.Resources()) != loadBenchResources {
			b.Fatal("resources missing")
		}
	}
}

// BenchmarkWorkspaceLoadResourceSyntax is the same read through
// ResourceSyntax, which doesn't.
func BenchmarkWorkspaceLoadResourceSyntax(b *testing.B) {
	ws := memoryWorkspace(map[string]string{rootfile.GeneratedFileName: string(benchGenerated(loadBenchResources))})
	b.ReportAllocs()
	for b.Loop() {
		f, err := ws.Load(rootfile.GeneratedFileName)
		if err != nil {
			b.Fatal(err)
		}
		n := 0
		for range f.ResourceSyntax() {
			n++
		}
		if n != loadBenchResources {
			b.Fatal("resources missing")
		}
	}
}
