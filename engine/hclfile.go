// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// hclFile is a configuration file parsed twice: as an editable tree, and as
// a syntax tree with source positions. Both list blocks in the same order.
// The syntax tree describes the file as loaded, not later edits.
type hclFile struct {
	path   string
	file   *hclwrite.File
	syntax *hclsyntax.Body
	src    []byte // the file as loaded, which syntax ranges point into
}

func loadHCL(path string) (*hclFile, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, diags := hclwrite.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse %s: %w", path, diags)
	}
	syntax, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse %s: %w", path, diags)
	}
	return &hclFile{path: path, file: f, syntax: syntax.Body.(*hclsyntax.Body), src: src}, nil
}

// save writes the edited file back, formatted. Removing the first block
// leaves the blank line that separated it from the next; save drops it.
func (f *hclFile) save() error {
	return os.WriteFile(f.path, bytes.TrimLeft(hclwrite.Format(f.file.Bytes()), "\n"), 0o644)
}

// resourceBlock is a top-level resource block in both trees.
type resourceBlock struct {
	address string
	write   *hclwrite.Block
	syntax  *hclsyntax.Block
}

// resources lists the resource blocks of the file.
func (f *hclFile) resources() []resourceBlock {
	var resources []resourceBlock
	blocks := f.file.Body().Blocks()
	for i, b := range f.syntax.Blocks {
		if b.Type == "resource" && len(b.Labels) == 2 && i < len(blocks) {
			resources = append(resources, resourceBlock{address: b.Labels[0] + "." + b.Labels[1], write: blocks[i], syntax: b})
		}
	}
	return resources
}

// resourceAt returns the address of the resource block spanning line.
func (f *hclFile) resourceAt(line int) string {
	for _, r := range f.resources() {
		if spans(r.syntax.Range(), line) {
			return r.address
		}
	}
	return ""
}

// importBlock is a top-level import block in both trees.
type importBlock struct {
	to     string // address of the resource it imports into
	id     string // the import ID, if a literal
	write  *hclwrite.Block
	syntax *hclsyntax.Block
}

// imports lists the import blocks of the file.
func (f *hclFile) imports() []importBlock {
	var imports []importBlock
	blocks := f.file.Body().Blocks()
	for i, b := range f.syntax.Blocks {
		if b.Type != "import" || i >= len(blocks) {
			continue
		}
		attr, ok := b.Body.Attributes["to"]
		if !ok {
			continue
		}
		traversal, diags := hcl.AbsTraversalForExpr(attr.Expr)
		if diags.HasErrors() || len(traversal) != 2 {
			continue
		}
		step, ok := traversal[1].(hcl.TraverseAttr)
		if !ok {
			continue
		}
		id := ""
		if idAttr, ok := b.Body.Attributes["id"]; ok {
			if v, diags := idAttr.Expr.Value(nil); !diags.HasErrors() && v.Type() == cty.String && v.IsKnown() && !v.IsNull() {
				id = v.AsString()
			}
		}
		imports = append(imports, importBlock{to: traversal.RootName() + "." + step.Name, id: id, write: blocks[i], syntax: b})
	}
	return imports
}

// importAt returns the address the import block spanning line imports into.
func (f *hclFile) importAt(line int) string {
	for _, imp := range f.imports() {
		if spans(imp.syntax.Range(), line) {
			return imp.to
		}
	}
	return ""
}

// importAddresses returns the addresses the file imports into.
func (f *hclFile) importAddresses() map[string]bool {
	addresses := map[string]bool{}
	for _, imp := range f.imports() {
		addresses[imp.to] = true
	}
	return addresses
}

func spans(rng hcl.Range, line int) bool {
	return rng.Start.Line <= line && line <= rng.End.Line
}

// sortResources rewrites the file with its blocks ordered by address, and
// reports whether that changed it. Terraform writes generated
// configuration in the order its graph walk reaches the resources, which
// differs between runs. Comments directly above a block move with it;
// anything else between blocks is dropped.
func sortResources(path string) (bool, error) {
	f, err := loadHCL(path)
	if err != nil {
		return false, err
	}
	before := hclwrite.Format(f.file.Bytes())
	blocks := f.file.Body().Blocks()
	if len(blocks) == 0 {
		return false, nil
	}
	key := func(b *hclwrite.Block) string { return b.Type() + " " + strings.Join(b.Labels(), ".") }
	sort.SliceStable(blocks, func(i, j int) bool { return key(blocks[i]) < key(blocks[j]) })
	sorted := hclwrite.NewEmptyFile()
	for i, b := range blocks {
		f.file.Body().RemoveBlock(b)
		if i > 0 {
			sorted.Body().AppendNewline()
		}
		sorted.Body().AppendBlock(b)
	}
	f.file = sorted
	if bytes.Equal(before, hclwrite.Format(sorted.Bytes())) {
		return false, nil
	}
	return true, f.save()
}
