// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package hclfile reads, edits and lays out Terraform configuration files:
// each a syntax tree with source positions and an editable tree, and the
// byte edits made to their source.
package hclfile

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

	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// File is a configuration file: a syntax tree with source positions,
// and an editable tree, parsed from the file the first time it is edited,
// since most readers only read. Both list blocks in the same order. The
// syntax tree describes the file as loaded, not later edits.
type File struct {
	Path   string
	Src    []byte // the file as loaded, which syntax ranges point into
	Syntax *hclsyntax.Body
	write  *hclwrite.File // nil until edit
}

// ParseDiagnostics parses src, the content of the file at path, or
// returns the diagnostics of why it doesn't parse, each naming the file
// and position; Parse and ReadVarFile wrap them in an error.
func ParseDiagnostics(path string, src []byte) (*File, hcl.Diagnostics) {
	syntax, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, diags
	}
	return &File{Path: path, Src: src, Syntax: syntax.Body.(*hclsyntax.Body)}, nil
}

// Parse parses src, the content of the file at path.
func Parse(path string, src []byte) (*File, error) {
	f, diags := ParseDiagnostics(path, src)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse %s: %w", path, diags)
	}
	return f, nil
}

func Load(path string) (*File, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(path, src)
}

// LoadOptional loads the file at path, or returns nil if there is none.
func LoadOptional(path string) (*File, error) {
	f, err := Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return f, err
}

// New is an empty file at path, to fill and save.
func New(path string) *File {
	return &File{Path: path, Syntax: &hclsyntax.Body{}, write: hclwrite.NewEmptyFile()}
}

// ReadVarFile reads the var file at path, such as a .tfvars file or a
// backend's settings; nil where there is none.
func ReadVarFile(path string) (*File, error) {
	src, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f, diags := ParseDiagnostics(path, src)
	if diags.HasErrors() {
		return nil, fmt.Errorf("%s: %w", path, diags)
	}
	return f, nil
}

// Values returns the values the file's arguments set, by name, of those
// that are literals, as a var file's are.
func (f *File) Values() map[string]cty.Value {
	values := map[string]cty.Value{}
	for name, attr := range f.Syntax.Attributes {
		if v, diags := attr.Expr.Value(nil); !diags.HasErrors() {
			values[name] = v
		}
	}
	return values
}

// Edit returns the editable tree of the file.
func (f *File) Edit() *hclwrite.File {
	if f.write == nil {
		// hclwrite parses src into a syntax tree first, as ParseDiagnostics
		// did without errors: it can't fail.
		f.write, _ = hclwrite.ParseConfig(f.Src, f.Path, hcl.InitialPos)
	}
	return f.write
}

// Save writes the edited file back, formatted. Removing the first block
// leaves the blank line that separated it from the next; Save drops it.
func (f *File) Save() error {
	return fsutil.WriteFile(f.Path, bytes.TrimLeft(hclwrite.Format(f.Edit().Bytes()), "\n"), 0o644)
}

// ParseExpr parses src, an expression, into the tokens of an editable
// tree; nil if it doesn't parse.
func ParseExpr(src string) hclwrite.Tokens {
	return valueOfX([]byte("x = " + src + "\n"))
}

// FormatExpr is ParseExpr of src formatted as the value of an argument.
func FormatExpr(src []byte) hclwrite.Tokens {
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

// Resource is a top-level resource block in both trees.
type Resource struct {
	Address string
	Write   *hclwrite.Block
	Syntax  *hclsyntax.Block
}

// Resources lists the resource blocks of the file.
func (f *File) Resources() []Resource {
	var resources []Resource
	blocks := f.Edit().Body().Blocks()
	for i, b := range f.Syntax.Blocks {
		if b.Type == "resource" && len(b.Labels) == 2 && i < len(blocks) {
			resources = append(resources, Resource{Address: b.Labels[0] + "." + b.Labels[1], Write: blocks[i], Syntax: b})
		}
	}
	return resources
}

// ResourceAt returns the address of the resource block spanning line.
func (f *File) ResourceAt(line int) string {
	for _, r := range f.Resources() {
		if Spans(r.Syntax.Range(), line) {
			return r.Address
		}
	}
	return ""
}

// Import is a top-level import block in both trees.
type Import struct {
	To     string // address of the resource it imports into
	ID     string // the import ID, if a literal
	Write  *hclwrite.Block
	Syntax *hclsyntax.Block
}

// Imports lists the import blocks of the file.
func (f *File) Imports() []Import {
	var imports []Import
	blocks := f.Edit().Body().Blocks()
	for i, b := range f.Syntax.Blocks {
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
		imports = append(imports, Import{To: traversal.RootName() + "." + step.Name, ID: id, Write: blocks[i], Syntax: b})
	}
	return imports
}

// ImportAt returns the address the import block spanning line imports into.
func (f *File) ImportAt(line int) string {
	for _, imp := range f.Imports() {
		if Spans(imp.Syntax.Range(), line) {
			return imp.To
		}
	}
	return ""
}

// ImportAddresses returns the addresses the file imports into.
func (f *File) ImportAddresses() map[string]bool {
	addresses := map[string]bool{}
	for _, imp := range f.Imports() {
		addresses[imp.To] = true
	}
	return addresses
}

func Spans(rng hcl.Range, line int) bool {
	return rng.Start.Line <= line && line <= rng.End.Line
}

// generatedComments are the comments Terraform writes into generated
// configuration: a header asking to review it, and the import ID above
// each resource, which imports.tf has.
var generatedComments = regexp.MustCompile(`(?m)^# (__generated__ by Terraform|Please review these resources and move them into your main configuration files).*\r?\n`)

// SortResources rewrites the file with its blocks ordered by address, and
// without the comments Terraform generates (see generatedComments), and
// reports whether that changed it. Terraform writes generated
// configuration in the order its graph walk reaches the resources, which
// differs between runs. Other comments directly above a block move with
// it; anything else between blocks is dropped.
func SortResources(path string) (bool, error) {
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
	f, err := Load(path)
	if err != nil {
		return false, err
	}
	blocks := f.Edit().Body().Blocks()
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
	return true, f.Save()
}
