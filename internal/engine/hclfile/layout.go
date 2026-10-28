// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package hclfile

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// LayoutDir lays out the resource and data blocks of the .tf files of
// dir as HashiCorp's style guide does (see LayoutBody). The layout changes
// nothing Terraform reads, so no plan checks it. The engine lays out
// generated local modules as it writes them: the modules directory is
// shared with the roots generating in parallel.
func LayoutDir(dir string) error {
	paths, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if _, err := layoutFile(path); err != nil {
			return err
		}
	}
	return nil
}

// layoutFile lays out the resource and data blocks of the file at path
// (see LayoutBody), formatted, and reports whether that changed it. It
// leaves a file that doesn't parse as it is.
func layoutFile(path string) (bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	out, ok := Layout(src)
	if !ok || bytes.Equal(out, src) {
		return false, nil
	}
	return true, fsutil.WriteFile(path, out, 0o644)
}

// Layout returns src with its resource and data blocks laid out (see
// LayoutBody), formatted. It reports false if src doesn't parse.
func Layout(src []byte) ([]byte, bool) {
	f, err := Parse("", src)
	if err != nil {
		return nil, false
	}
	out := slices.Clone(src)
	blocks := f.Syntax.Blocks
	// From the last block, so that the offsets of the others hold.
	for i := len(blocks) - 1; i >= 0; i-- {
		b := blocks[i]
		if (b.Type != "resource" && b.Type != "data") || b.OpenBraceRange.Start.Line == b.CloseBraceRange.Start.Line {
			continue
		}
		inner, ok := LayoutBody(src, b, true)
		if !ok {
			continue
		}
		out = slices.Concat(out[:b.OpenBraceRange.End.Byte], []byte("\n"), inner, out[b.CloseBraceRange.Start.Byte:])
	}
	formatted := CollapseEmptyBlocks(hclwrite.Format(out))
	if _, err := Parse("", formatted); err != nil {
		return nil, false
	}
	return formatted, true
}

// bodyItem is an argument or a nested block of a body, with the lines it
// takes in the source: from the comments right above it to the end of its
// last line.
type bodyItem struct {
	name       string
	attr       *hclsyntax.Attribute // nil for a block
	block      *hclsyntax.Block     // nil for an argument
	start, end int
}

// identityArguments name a resource, in the order they go first in its
// block: its name, or what stands for it, then for Azure its resource
// group and location.
var identityArguments = []string{"name", "name_prefix", "bucket", "function_name", "family", "identifier", "cluster_identifier", "resource_group_name", "location"}

// lastArguments are the arguments that go after the others.
var lastArguments = []string{"tags", "labels"}

// metaArguments go first in a resource's block, as Terraform's style
// guide has them.
var metaArguments = []string{"count", "for_each", "provider"}

// argumentRank orders the arguments of a resource's block: meta-arguments,
// the arguments that identify the resource, references to the resources
// it belongs to or uses, the others, and its tags, as people write them.
func argumentRank(it bodyItem) int {
	if i := slices.Index(metaArguments, it.name); i >= 0 {
		return i
	}
	if i := slices.Index(identityArguments, it.name); i >= 0 {
		return 10 + i
	}
	if i := slices.Index(lastArguments, it.name); i >= 0 {
		return 50 + i
	}
	if isReference(it.attr.Expr) {
		return 30
	}
	return 40
}

// isReference reports whether expr is a reference to another resource,
// a data source or a module, such as aws_vpc.main.id.
func isReference(expr hclsyntax.Expression) bool {
	traversal, diags := hcl.AbsTraversalForExpr(expr)
	if diags.HasErrors() || len(traversal) < 2 {
		return false
	}
	switch traversal.RootName() {
	case "var", "local", "each", "count", "path", "terraform", "self":
		return false
	}
	return true
}

// LayoutBody returns the lines of the body of b, which src holds, laid out:
// arguments first, then nested blocks, each group separated by one blank
// line, and the nested blocks laid out the same way. In a resource's or
// data source's own block, top, the arguments go in the order of
// argumentRank, with the meta-arguments in a group of their own, each
// nested block is a group, lifecycle goes last but for depends_on, after
// it. It reports false if the body holds anything but arguments, blocks
// and the comments above them, which it then leaves as they are.
func LayoutBody(src []byte, b *hclsyntax.Block, top bool) ([]byte, bool) {
	body := b.Body
	// Nothing after the opening brace on its line.
	if len(bytes.TrimSpace(src[b.OpenBraceRange.End.Byte:LineEnd(src, b.OpenBraceRange.End.Byte)])) > 0 {
		return nil, false
	}
	var items []bodyItem
	for name, attr := range body.Attributes {
		start, end := itemLines(src, attr.SrcRange)
		items = append(items, bodyItem{name: name, attr: attr, start: start, end: end})
	}
	for _, nested := range body.Blocks {
		start, end := itemLines(src, nested.Range())
		items = append(items, bodyItem{name: nested.Type, block: nested, start: start, end: end})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].start < items[j].start })
	// Nothing but blank lines between the items, so that nothing is lost.
	from := LineEnd(src, b.OpenBraceRange.End.Byte)
	for _, it := range items {
		if it.start < from || len(bytes.TrimSpace(src[from:it.start])) > 0 {
			return nil, false
		}
		from = it.end
	}
	if to := LineStart(src, b.CloseBraceRange.Start.Byte); from > to || len(bytes.TrimSpace(src[from:to])) > 0 {
		return nil, false
	}
	var meta, arguments, blocks, last [][]byte
	var ranked []bodyItem
	for _, it := range items {
		switch {
		case it.block != nil && top && it.name == "lifecycle":
			last = append([][]byte{itemText(src, it)}, last...)
		case it.block != nil:
			blocks = append(blocks, itemText(src, it))
		case top && it.name == "depends_on":
			last = append(last, itemText(src, it))
		case top:
			ranked = append(ranked, it)
		default:
			arguments = append(arguments, itemText(src, it))
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return argumentRank(ranked[i]) < argumentRank(ranked[j]) })
	for _, it := range ranked {
		if slices.Contains(metaArguments, it.name) {
			meta = append(meta, itemText(src, it))
		} else {
			arguments = append(arguments, itemText(src, it))
		}
	}
	var groups [][]byte
	for _, group := range [][][]byte{meta, arguments} {
		if len(group) > 0 {
			groups = append(groups, bytes.Join(group, nil))
		}
	}
	if top {
		groups = append(groups, blocks...)
	} else if len(blocks) > 0 {
		groups = append(groups, bytes.Join(blocks, nil))
	}
	groups = append(groups, last...)
	return bytes.Join(groups, []byte("\n")), true
}

// itemText returns an item's lines, those of a nested block laid out too.
func itemText(src []byte, it bodyItem) []byte {
	text := src[it.start:it.end]
	b := it.block
	if b == nil || b.OpenBraceRange.Start.Line == b.CloseBraceRange.Start.Line {
		return withNewline(text)
	}
	inner, ok := LayoutBody(src, b, false)
	if !ok {
		return withNewline(text)
	}
	return withNewline(slices.Concat(src[it.start:b.OpenBraceRange.End.Byte], []byte("\n"), inner, src[b.CloseBraceRange.Start.Byte:it.end]))
}

// itemLines returns where the lines of an item that spans rng start and
// end: from the comments right above it, to after the newline that ends
// its last line.
func itemLines(src []byte, rng hcl.Range) (int, int) {
	start := LineStart(src, rng.Start.Byte)
	for start > 0 {
		above := LineStart(src, start-1)
		line := bytes.TrimSpace(src[above:start])
		if !bytes.HasPrefix(line, []byte("#")) && !bytes.HasPrefix(line, []byte("//")) {
			break
		}
		start = above
	}
	return start, LineEnd(src, rng.End.Byte)
}

// LineStart returns the offset of the start of the line offset is on.
func LineStart(src []byte, offset int) int {
	return bytes.LastIndexByte(src[:offset], '\n') + 1
}

// LineEnd returns the offset after the newline that ends the line offset
// is on, or the end of src.
func LineEnd(src []byte, offset int) int {
	if i := bytes.IndexByte(src[offset:], '\n'); i >= 0 {
		return offset + i + 1
	}
	return len(src)
}

func withNewline(text []byte) []byte {
	if bytes.HasSuffix(text, []byte("\n")) {
		return text
	}
	return append(slices.Clone(text), '\n')
}

// CollapseEmptyBlocks writes the blocks and objects of src that hold
// nothing on one line, as allow {} and tags = {}, as people write them.
func CollapseEmptyBlocks(src []byte) []byte {
	tokens, diags := hclsyntax.LexConfig(src, "", hcl.InitialPos)
	if diags.HasErrors() {
		return src
	}
	var out []byte
	last := 0
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Type != hclsyntax.TokenOBrace {
			continue
		}
		j := i + 1
		for j < len(tokens) && tokens[j].Type == hclsyntax.TokenNewline {
			j++
		}
		if j == i+1 || j == len(tokens) || tokens[j].Type != hclsyntax.TokenCBrace {
			continue
		}
		out = append(out, src[last:tokens[i].Range.End.Byte]...)
		last = tokens[j].Range.Start.Byte
		i = j
	}
	return append(out, src[last:]...)
}
