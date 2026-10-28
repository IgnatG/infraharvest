// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"

	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/tftypes"
)

// hclFile is a configuration file: a syntax tree with source positions,
// and an editable tree, parsed from the file the first time it is edited,
// since most readers only read. Both list blocks in the same order. The
// syntax tree describes the file as loaded, not later edits.
type hclFile struct {
	path   string
	src    []byte // the file as loaded, which syntax ranges point into
	syntax *hclsyntax.Body
	write  *hclwrite.File // nil until edit
}

// parseFile parses src, the content of the file at path, or returns the
// diagnostics of why it doesn't parse, each naming the file and position;
// parseHCL and readVarFile wrap them in an error.
func parseFile(path string, src []byte) (*hclFile, hcl.Diagnostics) {
	syntax, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, diags
	}
	return &hclFile{path: path, src: src, syntax: syntax.Body.(*hclsyntax.Body)}, nil
}

// parseHCL parses src, the content of the file at path.
func parseHCL(path string, src []byte) (*hclFile, error) {
	f, diags := parseFile(path, src)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse %s: %w", path, diags)
	}
	return f, nil
}

func loadHCL(path string) (*hclFile, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseHCL(path, src)
}

// loadOptionalHCL loads the file at path, or returns nil if there is none.
func loadOptionalHCL(path string) (*hclFile, error) {
	f, err := loadHCL(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return f, err
}

// newHCL is an empty file at path, to fill and save.
func newHCL(path string) *hclFile {
	return &hclFile{path: path, syntax: &hclsyntax.Body{}, write: hclwrite.NewEmptyFile()}
}

// readVarFile reads the var file at path, such as a .tfvars file or a
// backend's settings; nil where there is none.
func readVarFile(path string) (*hclFile, error) {
	src, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f, diags := parseFile(path, src)
	if diags.HasErrors() {
		return nil, fmt.Errorf("%s: %w", path, diags)
	}
	return f, nil
}

// values returns the values the file's arguments set, by name, of those
// that are literals, as a var file's are.
func (f *hclFile) values() map[string]cty.Value {
	values := map[string]cty.Value{}
	for name, attr := range f.syntax.Attributes {
		if v, diags := attr.Expr.Value(nil); !diags.HasErrors() {
			values[name] = v
		}
	}
	return values
}

// edit returns the editable tree of the file.
func (f *hclFile) edit() *hclwrite.File {
	if f.write == nil {
		// hclwrite parses src into a syntax tree first, as parseFile
		// did without errors: it can't fail.
		f.write, _ = hclwrite.ParseConfig(f.src, f.path, hcl.InitialPos)
	}
	return f.write
}

// save writes the edited file back, formatted. Removing the first block
// leaves the blank line that separated it from the next; save drops it.
func (f *hclFile) save() error {
	return fsutil.WriteFile(f.path, bytes.TrimLeft(hclwrite.Format(f.edit().Bytes()), "\n"), 0o644)
}

// parseExpr parses src, an expression, into the tokens of an editable
// tree; nil if it doesn't parse.
func parseExpr(src string) hclwrite.Tokens {
	return valueOfX([]byte("x = " + src + "\n"))
}

// formatExpr is parseExpr of src formatted as the value of an argument.
func formatExpr(src []byte) hclwrite.Tokens {
	return valueOfX(hclwrite.Format(slices.Concat([]byte("x = "), src, []byte("\n"))))
}

// valueOfX returns the value of the argument x that src, a file, sets:
// the parser reads files, not expressions on their own. It returns nil if
// src doesn't parse.
func valueOfX(src []byte) hclwrite.Tokens {
	f, diags := hclwrite.ParseConfig(src, "", hcl.InitialPos)
	if diags.HasErrors() {
		return nil
	}
	attr := f.Body().GetAttribute("x")
	if attr == nil {
		return nil
	}
	return attr.Expr().BuildTokens(nil)
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
	blocks := f.edit().Body().Blocks()
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
	id     string // the import ID, if a literal (see importIDOf)
	write  *hclwrite.Block
	syntax *hclsyntax.Block
}

// imports lists the import blocks of the file.
func (f *hclFile) imports() []importBlock {
	var imports []importBlock
	blocks := f.edit().Body().Blocks()
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
		id, _ := importIDOf(b.Body)
		imports = append(imports, importBlock{to: traversal.RootName() + "." + step.Name, id: id, write: blocks[i], syntax: b})
	}
	return imports
}

// importIDOf returns the import ID of an import block's body: its id, if
// a literal, or, for a block that imports by identity, the identity's ID
// (see tftypes.IdentityID), if it is an object of literal strings,
// numbers and bools, as ImportsFile writes it. The engine tells a
// resource imported by identity by that ID, as the importer does (see
// Import.Identity).
func importIDOf(body *hclsyntax.Body) (string, bool) {
	if attr, ok := body.Attributes["id"]; ok {
		v, diags := attr.Expr.Value(nil)
		if diags.HasErrors() || v.Type() != cty.String || !v.IsKnown() || v.IsNull() {
			return "", false
		}
		return v.AsString(), true
	}
	attr, ok := body.Attributes["identity"]
	if !ok {
		return "", false
	}
	v, diags := attr.Expr.Value(nil)
	if diags.HasErrors() || !v.IsWhollyKnown() || v.IsNull() || (!v.Type().IsObjectType() && !v.Type().IsMapType()) {
		return "", false
	}
	identity := map[string]string{}
	for it := v.ElementIterator(); it.Next(); {
		k, e := it.Element()
		if e.IsNull() {
			continue
		}
		s, err := convert.Convert(e, cty.String)
		if err != nil {
			return "", false
		}
		identity[k.AsString()] = s.AsString()
	}
	if len(identity) == 0 {
		return "", false
	}
	return tftypes.IdentityID(identity), true
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

// generatedComments are the comments Terraform writes into generated
// configuration: a header asking to review it, and the import ID above
// each resource, which imports.tf has.
var generatedComments = regexp.MustCompile(`(?m)^# (__generated__ by Terraform|Please review these resources and move them into your main configuration files).*\r?\n`)

// sortResources rewrites the file with its blocks ordered by address, and
// without the comments Terraform generates (see generatedComments), and
// reports whether that changed it. Terraform writes generated
// configuration in the order its graph walk reaches the resources, which
// differs between runs. Other comments directly above a block move with
// it; anything else between blocks is dropped.
func sortResources(path string) (bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	before := hclwrite.Format(content)
	if stripped := generatedComments.ReplaceAll(content, nil); !bytes.Equal(stripped, content) {
		if err := fsutil.WriteFile(path, stripped, 0o644); err != nil {
			return false, err
		}
	}
	f, err := loadHCL(path)
	if err != nil {
		return false, err
	}
	blocks := f.edit().Body().Blocks()
	if len(blocks) == 0 {
		return false, nil
	}
	key := func(b *hclwrite.Block) string { return b.Type() + " " + strings.Join(b.Labels(), ".") }
	sort.SliceStable(blocks, func(i, j int) bool { return key(blocks[i]) < key(blocks[j]) })
	sorted := hclwrite.NewEmptyFile()
	for i, b := range blocks {
		f.write.Body().RemoveBlock(b)
		if i > 0 {
			sorted.Body().AppendNewline()
		}
		sorted.Body().AppendBlock(b)
	}
	f.write = sorted
	if bytes.Equal(before, hclwrite.Format(sorted.Bytes())) {
		return false, nil
	}
	return true, f.save()
}
