// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package hclfile

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclwrite"
)

// removeSource is a file of n resources, with comments above some, blank
// lines between, a nested block in each, and a variable at the end.
func removeSource(n int) []byte {
	var b strings.Builder
	b.WriteString("# The file's own comment.\n\n")
	for i := range n {
		if i%3 == 0 {
			fmt.Fprintf(&b, "# __generated__ by Terraform from \"id-%d\"\n", i)
		}
		fmt.Fprintf(&b, "resource \"aws_s3_bucket\" \"b%d\" {\n  bucket = \"b%d\" # trailing\n  lifecycle {\n    ignore_changes = all\n  }\n}\n", i, i)
		if i%4 != 0 {
			b.WriteString("\n")
		}
	}
	b.WriteString("variable \"v\" {}\n")
	return []byte(b.String())
}

// TestRemoveBlocksAsOneByOne checks that RemoveBlocks leaves the file as
// removing each block with RemoveBlock does, byte for byte, both below
// and above the count it removes one at a time, and that an edit of a
// block that stays, made before, is kept.
func TestRemoveBlocksAsOneByOne(t *testing.T) {
	src := removeSource(40)
	for _, tc := range []struct {
		name string
		pick func(i int) bool
	}{
		{"none", func(int) bool { return false }},
		{"a few", func(i int) bool { return i == 3 || i == 17 }},
		{"every other", func(i int) bool { return i%2 == 0 }},
		{"the first and the last", func(i int) bool { return i == 0 || i == 39 }},
		{"all but one", func(i int) bool { return i != 20 }},
		{"all", func(int) bool { return true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edit := func(f *File) []*hclwrite.Block {
				var gone []*hclwrite.Block
				for i, r := range f.Resources() {
					if tc.pick(i) {
						gone = append(gone, r.Write)
					} else {
						r.Write.Body().SetAttributeRaw("bucket", ParseExpr(fmt.Sprintf("%q", fmt.Sprint("kept-", i))))
					}
				}
				// One a block of a block, which neither removes.
				nested := f.Edit().Body().Blocks()[0].Body().Blocks()
				return append(gone, nested...)
			}
			one, err := Parse("main.tf", src)
			if err != nil {
				t.Fatal(err)
			}
			call := func() *hclwrite.Block {
				b := hclwrite.NewBlock("module", []string{"m"})
				b.Body().SetAttributeRaw("source", ParseExpr(`"./m"`))
				return b
			}
			for _, b := range edit(one) {
				one.Edit().Body().RemoveBlock(b)
			}
			one.Edit().Body().AppendNewline()
			one.Edit().Body().AppendBlock(call())
			all, err := Parse("main.tf", src)
			if err != nil {
				t.Fatal(err)
			}
			all.RemoveBlocks(edit(all))
			all.AppendBlocks(call())
			if got, want := string(all.Bytes()), string(one.Edit().Bytes()); got != want {
				t.Errorf("RemoveBlocks:\n%s\nRemoveBlock one by one:\n%s", got, want)
			}
			// What remains can be edited as a tree again.
			if got, want := len(all.Edit().Body().Blocks()), len(one.Edit().Body().Blocks()); got != want {
				t.Errorf("%d blocks after RemoveBlocks, want %d", got, want)
			}
			if got, want := string(all.Edit().Bytes()), string(one.Edit().Bytes()); got != want {
				t.Errorf("parsed again:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// BenchmarkRemoveBlocks removes every other of 20,000 resource blocks,
// one by one and at once.
func BenchmarkRemoveBlocks(b *testing.B) {
	src := removeSource(20000)
	for _, at := range []struct {
		name   string
		remove func(f *File, blocks []*hclwrite.Block)
	}{
		{"one by one", func(f *File, blocks []*hclwrite.Block) {
			for _, block := range blocks {
				f.Edit().Body().RemoveBlock(block)
			}
		}},
		{"at once", func(f *File, blocks []*hclwrite.Block) { f.RemoveBlocks(blocks) }},
	} {
		b.Run(at.name, func(b *testing.B) {
			for range b.N {
				b.StopTimer()
				f, err := Parse("main.tf", src)
				if err != nil {
					b.Fatal(err)
				}
				var gone []*hclwrite.Block
				for i, r := range f.Resources() {
					if i%2 == 0 {
						gone = append(gone, r.Write)
					}
				}
				b.StartTimer()
				at.remove(f, gone)
			}
		})
	}
}
