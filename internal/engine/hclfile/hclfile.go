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
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"

	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/tftypes"
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

// ResourceIndex returns the addresses of the file's resource blocks by
// the lines they span (see LineIndex).
func (f *File) ResourceIndex() LineIndex {
	var x LineIndex
	for _, r := range f.Resources() {
		x.add(r.Syntax.Range(), r.Address)
	}
	return x
}

// LineIndex finds the top-level block spanning a line of a file, built
// once for the many lines a plan's diagnostics point at: a search in the
// blocks, not a walk of them per line.
type LineIndex struct {
	blocks []lineBlock // in the file's order
}

type lineBlock struct {
	start, end int
	address    string
}

func (x *LineIndex) add(rng hcl.Range, address string) {
	x.blocks = append(x.blocks, lineBlock{rng.Start.Line, rng.End.Line, address})
}

// At returns the address of the first block spanning line, "" if none
// does. Top-level blocks don't overlap, so in the file's order the lines
// they end on never go down: the first that ends on line or after is the
// only one that may span it before a later one starts.
func (x LineIndex) At(line int) string {
	i := sort.Search(len(x.blocks), func(i int) bool { return x.blocks[i].end >= line })
	if i < len(x.blocks) && x.blocks[i].start <= line {
		return x.blocks[i].address
	}
	return ""
}

// Import is a top-level import block in both trees.
type Import struct {
	To     string // address of the resource it imports into
	ID     string // the import ID, if a literal (see ImportIDOf)
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
		id, _ := ImportIDOf(b.Body)
		imports = append(imports, Import{To: traversal.RootName() + "." + step.Name, ID: id, Write: blocks[i], Syntax: b})
	}
	return imports
}

// ImportIDOf returns the import ID of an import block's body: its id, if
// a literal, or, for a block that imports by identity, the identity's ID
// (see tftypes.IdentityID and IdentityOf). The engine tells a resource
// imported by identity by that ID, as the importer does (see
// Import.Identity).
func ImportIDOf(body *hclsyntax.Body) (string, bool) {
	if attr, ok := body.Attributes["id"]; ok {
		v, diags := attr.Expr.Value(nil)
		if diags.HasErrors() || v.Type() != cty.String || !v.IsKnown() || v.IsNull() {
			return "", false
		}
		return v.AsString(), true
	}
	identity, ok := IdentityOf(body)
	if !ok {
		return "", false
	}
	return tftypes.IdentityID(identity), true
}

// IdentityOf returns the identity an import block's body imports by, as
// strings (see LiteralIdentity), if it is a literal, as ImportsFile
// writes it; false for a block that imports by ID, or by an identity it
// can't read.
func IdentityOf(body *hclsyntax.Body) (map[string]string, bool) {
	attr, ok := body.Attributes["identity"]
	if !ok {
		return nil, false
	}
	v, diags := attr.Expr.Value(nil)
	if diags.HasErrors() {
		return nil, false
	}
	return LiteralIdentity(v)
}

// LiteralIdentity returns an identity, a value, as strings (see
// tftypes.IdentityValues), if it is an object or map of known strings,
// numbers and bools, with at least one that isn't null.
func LiteralIdentity(v cty.Value) (map[string]string, bool) {
	if !v.IsWhollyKnown() || v.IsNull() || (!v.Type().IsObjectType() && !v.Type().IsMapType()) {
		return nil, false
	}
	identity := map[string]string{}
	for it := v.ElementIterator(); it.Next(); {
		k, e := it.Element()
		if e.IsNull() {
			continue
		}
		s, err := convert.Convert(e, cty.String)
		if err != nil {
			return nil, false
		}
		identity[k.AsString()] = s.AsString()
	}
	if len(identity) == 0 {
		return nil, false
	}
	return identity, true
}

// ImportIndex returns the addresses the file's import blocks import into,
// by the lines the blocks span (see LineIndex).
func (f *File) ImportIndex() LineIndex {
	var x LineIndex
	for _, imp := range f.Imports() {
		x.add(imp.Syntax.Range(), imp.To)
	}
	return x
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

// SortResources orders the blocks of f by address, without the comments
// Terraform generates (see generatedComments), and reports whether that
// changed it: the caller saves it. Terraform writes generated
// configuration in the order its graph walk reaches the resources, which
// differs between runs. Other comments directly above a block move with
// it; anything else between blocks is dropped.
func SortResources(f *File) (bool, error) {
	src := f.Src
	before := hclwrite.Format(src)
	stripped := generatedComments.ReplaceAll(src, nil)
	if !bytes.Equal(stripped, src) {
		parsed, err := Parse(f.Path, stripped)
		if err != nil {
			return false, err
		}
		*f = *parsed
	}
	blocks := f.Edit().Body().Blocks()
	if len(blocks) == 0 {
		return !bytes.Equal(stripped, src), nil
	}
	key := func(b *hclwrite.Block) string { return b.Type() + " " + strings.Join(b.Labels(), ".") }
	sort.SliceStable(blocks, func(i, j int) bool { return key(blocks[i]) < key(blocks[j]) })
	// The blocks go to a new file, which replaces the old one whole:
	// removing each from the old file first would search its body for
	// every block.
	sorted := hclwrite.NewEmptyFile()
	for i, b := range blocks {
		if i > 0 {
			sorted.Body().AppendNewline()
		}
		sorted.Body().AppendBlock(b)
	}
	f.write = sorted
	return !bytes.Equal(before, hclwrite.Format(sorted.Bytes())), nil
}

// LiteralString returns the value of expr if it is a string literal.
func LiteralString(expr *hclsyntax.TemplateExpr) (string, bool) {
	if !expr.IsStringLiteral() {
		return "", false
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.Type().Equals(cty.String) {
		return "", false
	}
	return v.AsString(), true
}

// AttributeExpr returns the expression of body's attribute name, or nil
// if body doesn't set it.
func AttributeExpr(body *hclsyntax.Body, name string) hclsyntax.Expression {
	if attr, ok := body.Attributes[name]; ok {
		return attr.Expr
	}
	return nil
}

// StringLiteral returns the value of a string literal, "" for anything
// else (see LiteralString).
func StringLiteral(expr hclsyntax.Expression) string {
	t, ok := expr.(*hclsyntax.TemplateExpr)
	if !ok {
		return ""
	}
	s, _ := LiteralString(t)
	return s
}

// ObjectKey returns the key of an object constructor's item, "" if it
// isn't a constant.
func ObjectKey(expr hclsyntax.Expression) string {
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.IsKnown() || v.Type() != cty.String {
		return ""
	}
	return v.AsString()
}

// ConstantString returns the value of expr if it is a constant string.
func ConstantString(expr hclsyntax.Expression) (string, bool) {
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.IsKnown() || v.Type() != cty.String {
		return "", false
	}
	return v.AsString(), true
}

// AddressNames returns the names of an address, split at its dots,
// without its instance keys, whose quoted strings may have dots and
// brackets of their own: module, logs, aws_s3_bucket and this for
// module.logs["a.b"].aws_s3_bucket.this[0].
func AddressNames(address string) []string {
	var names []string
	var name strings.Builder
	depth, quoted, escaped := 0, false, false
	for _, r := range address {
		switch {
		case quoted:
			switch {
			case escaped:
				escaped = false
			case r == '\\':
				escaped = true
			case r == '"':
				quoted = false
			}
		case r == '"' && depth > 0:
			quoted = true
		case r == '[':
			depth++
		case r == ']' && depth > 0:
			depth--
		case depth > 0:
		case r == '.':
			names = append(names, name.String())
			name.Reset()
		default:
			name.WriteRune(r)
		}
	}
	return append(names, name.String())
}

// NamesModuleCall reports whether address is a module call's, in module
// calls or not, rather than a resource's.
func NamesModuleCall(address string) bool {
	names := AddressNames(address)
	return len(names) >= 2 && names[len(names)-2] == "module"
}

// TrimInstanceKey turns aws_vpc.a["x"] or aws_vpc.a[0] into aws_vpc.a.
func TrimInstanceKey(addr string) string {
	if i := strings.IndexByte(addr, '['); i >= 0 {
		return addr[:i]
	}
	return addr
}

// KeyName returns an object key as written: an identifier, or a string.
func KeyName(expr hclsyntax.Expression) string {
	if name := hcl.ExprAsKeyword(expr); name != "" {
		return name
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.IsKnown() || !v.Type().Equals(cty.String) {
		return ""
	}
	return v.AsString()
}

// Traversals returns the references of every argument in body, nested
// blocks' included.
func Traversals(body *hclsyntax.Body) []hcl.Traversal {
	var ts []hcl.Traversal
	for _, attr := range body.Attributes {
		ts = append(ts, attr.Expr.Variables()...)
	}
	for _, b := range body.Blocks {
		ts = append(ts, Traversals(b.Body)...)
	}
	return ts
}

// StringMap evaluates a literal map of strings; null is an empty map.
func StringMap(expr hclsyntax.Expression) (map[string]string, bool) {
	v, diags := expr.Value(nil)
	if diags.HasErrors() || !v.IsWhollyKnown() {
		return nil, false
	}
	m := map[string]string{}
	if v.IsNull() {
		return m, true
	}
	if !v.Type().IsMapType() && !v.Type().IsObjectType() {
		return nil, false
	}
	for it := v.ElementIterator(); it.Next(); {
		k, val := it.Element()
		if val.IsNull() || val.Type() != cty.String {
			return nil, false
		}
		m[k.AsString()] = val.AsString()
	}
	return m, true
}

// StringAttribute returns an attribute's literal string value, or "".
func StringAttribute(body *hclsyntax.Body, name string) string {
	attr, ok := body.Attributes[name]
	if !ok {
		return ""
	}
	v, diags := attr.Expr.Value(nil)
	if diags.HasErrors() || v.Type() != cty.String || !v.IsKnown() || v.IsNull() {
		return ""
	}
	return v.AsString()
}

// HasDefault reports whether the variable block b has a default. One
// without, such as a secret's, takes a value of its own: two of them
// declared alike are still two values.
func HasDefault(b *hclsyntax.Block) bool {
	_, ok := b.Body.Attributes["default"]
	return ok
}

// IsNullLiteral reports whether expr is the literal null.
func IsNullLiteral(expr hclsyntax.Expression) bool {
	lit, ok := expr.(*hclsyntax.LiteralValueExpr)
	return ok && lit.Val.IsNull()
}

// WithoutNullAttributes returns an expression without the attributes of
// its object constructors that are null, which a variable with optional
// attributes sets to null itself.
func WithoutNullAttributes(tokens hclwrite.Tokens) hclwrite.Tokens {
	src := tokens.Bytes()
	expr, diags := hclsyntax.ParseExpression(src, "", hcl.InitialPos)
	if diags.HasErrors() {
		return tokens
	}
	type span struct{ start, end int }
	var nulls []span
	_ = hclsyntax.VisitAll(expr, func(n hclsyntax.Node) hcl.Diagnostics {
		obj, ok := n.(*hclsyntax.ObjectConsExpr)
		if !ok {
			return nil
		}
		for _, item := range obj.Items {
			if IsNullLiteral(item.ValueExpr) {
				nulls = append(nulls, span{item.KeyExpr.Range().Start.Byte, item.ValueExpr.Range().End.Byte})
			}
		}
		return nil
	})
	if len(nulls) == 0 {
		return tokens
	}
	slices.SortFunc(nulls, func(a, b span) int { return a.start - b.start })
	var out []byte
	last := 0
	for _, s := range nulls {
		out = append(out, src[last:s.start]...)
		end := s.end
		// The item's separator: a comma, or the end of its line.
		for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
			end++
		}
		if end < len(src) && src[end] == ',' {
			end++
		}
		for end < len(src) && (src[end] == ' ' || src[end] == '\t' || src[end] == '\r' || src[end] == '\n') {
			end++
		}
		last = end
	}
	out = append(out, src[last:]...)
	if formatted := FormatExpr(out); formatted != nil {
		return formatted
	}
	return tokens
}

// ConfigFiles parses the .tf files of dir, in name order, without override
// files, which only change what the others declare.
func ConfigFiles(dir string) ([]*File, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var files []*File
	for _, path := range paths {
		name := filepath.Base(path)
		if name == "override.tf" || strings.HasSuffix(name, "_override.tf") {
			continue
		}
		f, err := Load(path)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

// CallSource is how a root in dir calls a module from source: a module in
// a directory, at an absolute path, by its path relative to dir, as
// Terraform requires; any other source as it is.
func CallSource(dir, source string) (string, error) {
	if !filepath.IsAbs(source) {
		return source, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(abs, source)
	if err != nil {
		return "", fmt.Errorf("module %s: %w", source, err)
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, "../") {
		rel = "./" + rel
	}
	return rel, nil
}

// Files and directories of the output that more than the engine, which
// writes them, reads.
const (
	// VariablesFileName declares the variables of a root, or of a
	// generated module.
	VariablesFileName = "variables.tf"
	// BackendFileName configures a root's backend.
	BackendFileName = "backend.tf"
	// ModulesDirName is the directory of the output that holds generated
	// local modules, next to the roots.
	ModulesDirName = "modules"
)

// ResourceAddress returns the resource a traversal refers to, or "".
func ResourceAddress(t hcl.Traversal) string {
	switch t.RootName() {
	case "var", "local", "module", "data", "path", "count", "each", "self", "terraform":
		return ""
	}
	if len(t) < 2 {
		return ""
	}
	step, ok := t[1].(hcl.TraverseAttr)
	if !ok {
		return ""
	}
	return t.RootName() + "." + step.Name
}

// CopyBlock returns an unattached copy of block, without the comments
// above it (Terraform's, naming the resource's import ID).
func CopyBlock(block *hclwrite.Block) (*hclwrite.Block, error) {
	src := block.BuildTokens(nil).Bytes()
	for {
		trimmed := bytes.TrimLeft(src, " \t\r\n")
		if !bytes.HasPrefix(trimmed, []byte("#")) && !bytes.HasPrefix(trimmed, []byte("//")) {
			src = trimmed
			break
		}
		_, rest, found := bytes.Cut(trimmed, []byte("\n"))
		if !found {
			src = nil
			break
		}
		src = rest
	}
	parsed, diags := ParseDiagnostics("", src)
	if diags.HasErrors() {
		return nil, fmt.Errorf("copy a block: %w", diags)
	}
	f := parsed.Edit()
	if len(f.Body().Blocks()) != 1 {
		return nil, errors.New("copy a block: not one block")
	}
	copied := f.Body().Blocks()[0]
	f.Body().RemoveBlock(copied)
	return copied, nil
}

// EvalContext evaluates the constants of generated configuration, in
// which Terraform writes JSON strings as jsonencode(...).
var EvalContext = &hcl.EvalContext{Functions: map[string]function.Function{"jsonencode": stdlib.JSONEncodeFunc}}
