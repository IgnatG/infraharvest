// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"
)

// configFiles parses the .tf files of dir, in name order, without override
// files, which only change what the others declare.
func configFiles(dir string) ([]*hclFile, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var files []*hclFile
	for _, path := range paths {
		name := filepath.Base(path)
		if name == "override.tf" || strings.HasSuffix(name, "_override.tf") {
			continue
		}
		f, err := loadHCL(path)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

// HasConfiguration reports whether dir holds a root with resources or
// module calls, such as one an earlier import generated.
func HasConfiguration(dir string) (bool, error) {
	files, err := configFiles(dir)
	if err != nil {
		return false, err
	}
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			if b.Type == "resource" || b.Type == "module" {
				return true, nil
			}
		}
	}
	return false, nil
}

// rootNames returns the names files use (see Names).
func rootNames(files []*hclFile) Names {
	names := Names{}
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			switch {
			case b.Type == "resource" && len(b.Labels) == 2:
				names[b.Labels[0]+"."+b.Labels[1]] = true
			case b.Type == "data" && len(b.Labels) == 2:
				names["data."+b.Labels[0]+"."+b.Labels[1]] = true
			case b.Type == "module" && len(b.Labels) == 1:
				names["module."+b.Labels[0]] = true
			case b.Type == "variable" && len(b.Labels) == 1:
				names["var."+b.Labels[0]] = true
			case b.Type == "locals":
				for name := range b.Body.Attributes {
					names["local."+name] = true
				}
			}
		}
	}
	return names
}

// Existing returns the resources the root in dir already has, by type and
// import ID: those its import blocks import, and those previous, the
// result of the import that generated it, imported or left out. Each has
// its address if it is a resource block of the root, so that what is
// added can refer to it; "" if not, such as inside a module.
func Existing(dir string, previous *Result) (map[External]string, error) {
	files, err := configFiles(dir)
	if err != nil {
		return nil, err
	}
	resources := map[string]bool{}
	for _, f := range files {
		for _, r := range f.resources() {
			resources[r.address] = true
		}
	}
	existing := map[External]string{}
	add := func(typ, id, address string) {
		if !resources[address] {
			address = ""
		}
		e := External{Type: typ, ID: id}
		if existing[e] == "" {
			existing[e] = address
		}
	}
	if previous != nil {
		for _, imp := range previous.Imported {
			add(imp.Type, imp.ID, imp.Type+"."+imp.Name)
		}
		for _, r := range previous.Rejected {
			add(addressType(r.Address), r.ID, "")
		}
	}
	for _, f := range files {
		for key, address := range importTargetsIn(f) {
			typ, id, _ := strings.Cut(key, "\x00")
			add(typ, id, address)
		}
	}
	return existing, nil
}

// appliedTags returns the tags the root's provider applies to every
// resource through dt.Block, if they are literal, directly or through a
// local.
func appliedTags(files []*hclFile, dt DefaultTags) (map[string]string, bool) {
	var expr hclsyntax.Expression
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			if b.Type != "provider" || len(b.Labels) != 1 || b.Labels[0] != dt.Provider {
				continue
			}
			for _, inner := range b.Body.Blocks {
				if attr, ok := inner.Body.Attributes[dt.Attribute]; ok && inner.Type == dt.Block {
					expr = attr.Expr
				}
			}
		}
	}
	if expr == nil {
		return nil, false
	}
	if tags, ok := stringMap(expr); ok {
		return tags, true
	}
	traversal, diags := hcl.AbsTraversalForExpr(expr)
	if diags.HasErrors() || len(traversal) != 2 || traversal.RootName() != "local" {
		return nil, false
	}
	local, ok := traversal[1].(hcl.TraverseAttr)
	if !ok {
		return nil, false
	}
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			if attr, ok := b.Body.Attributes[local.Name]; ok && b.Type == "locals" {
				return stringMap(attr.Expr)
			}
		}
	}
	return nil, false
}

// Add adds the resources of imports to root, a root an earlier import
// generated, without changing what root has. It generates their
// configuration in staging, a directory of its own, with tf, as Generate
// does, without local modules, with the tags root's provider applies, and
// naming nothing as root does. The resources root has, existing (see
// Existing), are external there: references to the ones with an address
// in root then refer to them, and references to the others read them.
// Then it adds the result to root (see merge), where it runs the
// verification gate with rootTF: the plan check is staging's, as root's
// plan needs its state.
func Add(ctx context.Context, tf, rootTF Terraform, staging, root string, imports []Import, opts Options, existing map[External]string) (*Result, error) {
	if err := os.RemoveAll(staging); err != nil {
		return nil, err
	}
	files, err := configFiles(root)
	if err != nil {
		return nil, err
	}
	opts.Taken = rootNames(files)
	opts.ModulesDir = ""
	if opts.DefaultTags != nil {
		dt := *opts.DefaultTags
		if applied, ok := appliedTags(files, dt); ok {
			dt.Applied = applied
			opts.DefaultTags = &dt
		} else {
			// Lifting tags would change root's provider.
			opts.DefaultTags = nil
		}
	}
	external := map[External]bool{}
	for _, e := range opts.External {
		external[e] = true
	}
	opts.External = slices.Clone(opts.External)
	for _, e := range sortedExternal(existing) {
		if !external[e] {
			opts.External = append(opts.External, e)
		}
	}
	result, err := Generate(ctx, tf, staging, imports, opts)
	if err != nil {
		return nil, err
	}
	// What follows changes root: undo it on failure, so that root stays as
	// it was and a rerun adds the resources again.
	backup, err := backupFiles(root, DataFileName, ImportsFileName, VariablesFileName, LocalsFileName, RejectedFileName, LockFileName)
	if err != nil {
		return nil, err
	}
	added, err := merge(staging, root, existing, opts.DataSources)
	undo := func() error {
		if added != "" {
			if err := os.Remove(filepath.Join(root, added)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return backup.restore()
	}
	if err != nil {
		return nil, errors.Join(err, undo())
	}
	// After merge: the calls it added need their modules installed.
	if err := rootTF.Init(ctx, tfexec.Backend(false)); err != nil {
		return nil, errors.Join(fmt.Errorf("terraform init: %w", err), undo())
	}
	staged := result.Gate
	gate, err := gateWith(ctx, rootTF, root, opts, func() (Check, []string, error) {
		return staged.check(CheckPlan), nil, nil
	})
	if err != nil {
		return nil, errors.Join(err, undo())
	}
	// Only staging's plan knew the values to look for.
	if secrets := staged.check(CheckSecrets); !secrets.Passed {
		for i := range gate {
			if gate[i].Name == CheckSecrets {
				gate[i].Passed = false
				gate[i].Details = append(gate[i].Details, secrets.Details...)
			}
		}
	}
	result.Gate = gate
	if err := os.RemoveAll(staging); err != nil {
		return nil, err
	}
	return result, nil
}

func sortedExternal(m map[External]string) []External {
	list := make([]External, 0, len(m))
	for e := range m {
		list = append(list, e)
	}
	sortExternal(list)
	return list
}

// sortExternal sorts list by type, then ID.
func sortExternal(list []External) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].Type != list[j].Type {
			return list[i].Type < list[j].Type
		}
		return list[i].ID < list[j].ID
	})
}

// With returns what a root holds once an incremental import added added
// to it (see Add): r's resources and added's, with added's checks.
func (r *Result) With(added *Result) *Result {
	if r == nil {
		return added
	}
	return &Result{
		Imported: append(slices.Clone(r.Imported), added.Imported...),
		Secrets:  append(slices.Clone(r.Secrets), added.Secrets...),
		Rejected: append(slices.Clone(r.Rejected), added.Rejected...),
		Modules:  append(slices.Clone(r.Modules), added.Modules...),
		Gate:     added.Gate,
	}
}

// AddedFileName returns the name of the n-th file of resources an
// incremental import adds: generated_2.tf, generated_3.tf, ...
func AddedFileName(n int) string {
	return fmt.Sprintf("generated_%d.tf", n)
}

// merge adds what Generate wrote into staging to root: the resources and
// module calls in a file of their own (see AddedFileName), and the import
// blocks, variables, locals, data sources and resources left out after
// root's own. References to data sources that read a resource root has,
// existing, as a resource block, or that root already has, use root's.
// The provider configuration and the locals it uses stay root's. It
// returns the name of the file of resources it added to root, "" if it
// failed before writing it.
func merge(staging, root string, existing map[External]string, sources map[string]DataSource) (string, error) {
	generated, err := loadHCL(filepath.Join(staging, GeneratedFileName))
	if err != nil {
		return "", err
	}
	rootFiles, err := configFiles(root)
	if err != nil {
		return "", err
	}
	// Where root reads or has each resource, by data source type and ID.
	rootData := map[string][]string{}
	for e, address := range existing {
		if source, ok := sources[e.Type]; ok && address != "" {
			rootData[source.Type+"\x00"+e.ID] = strings.Split(address, ".")
		}
	}
	for _, f := range rootFiles {
		for _, b := range f.syntax.Blocks {
			if b.Type != "data" || len(b.Labels) != 2 {
				continue
			}
			key := dataKey(b, sources)
			if _, ok := rootData[key]; key != "" && !ok {
				rootData[key] = []string{"data", b.Labels[0], b.Labels[1]}
			}
		}
	}
	var data []*hclwrite.Block
	staged, err := loadOptionalHCL(filepath.Join(staging, DataFileName))
	if err != nil {
		return "", err
	}
	if staged != nil {
		blocks := staged.file.Body().Blocks()
		for i, b := range staged.syntax.Blocks {
			if b.Type != "data" || len(b.Labels) != 2 || i >= len(blocks) {
				continue
			}
			replacement, ok := rootData[dataKey(b, sources)]
			if !ok {
				data = append(data, blocks[i])
				continue
			}
			for _, block := range generated.file.Body().Blocks() {
				replaceTraversals(block.Body(), []string{"data", b.Labels[0], b.Labels[1]}, replacement)
			}
		}
	}

	n := 2
	for ; ; n++ {
		if _, err := os.Stat(filepath.Join(root, AddedFileName(n))); errors.Is(err, fs.ErrNotExist) {
			break
		} else if err != nil {
			return "", err
		}
	}
	added := AddedFileName(n)
	generated.path = filepath.Join(root, added)
	if err := generated.save(); err != nil {
		return added, err
	}
	if err := appendBlocks(filepath.Join(root, DataFileName), dataFileHeader, data); err != nil {
		return added, err
	}
	for _, name := range []string{ImportsFileName, VariablesFileName} {
		f, err := loadOptionalHCL(filepath.Join(staging, name))
		if err != nil {
			return added, err
		}
		if f != nil {
			if err := appendBlocks(filepath.Join(root, name), "", f.file.Body().Blocks()); err != nil {
				return added, err
			}
		}
	}
	if err := mergeLocals(staging, root); err != nil {
		return added, err
	}
	if rejected, err := os.ReadFile(filepath.Join(staging, RejectedFileName)); err == nil {
		if err := appendFile(filepath.Join(root, RejectedFileName), rejected); err != nil {
			return added, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return added, err
	}
	lock := filepath.Join(root, LockFileName)
	if _, err := os.Stat(lock); errors.Is(err, fs.ErrNotExist) {
		if content, err := os.ReadFile(filepath.Join(staging, LockFileName)); err == nil {
			return added, os.WriteFile(lock, content, 0o644)
		}
	}
	return added, nil
}

// replaceTraversals replaces the start of references that begin with
// search, such as data.aws_vpc.main, with replacement, which may have
// another length, such as aws_vpc.main, in body and its nested blocks.
func replaceTraversals(body *hclwrite.Body, search, replacement []string) {
	for name, attr := range body.Attributes() {
		if tokens, ok := replacedTokens(attr.Expr().BuildTokens(nil), search, replacement); ok {
			body.SetAttributeRaw(name, tokens)
		}
	}
	for _, b := range body.Blocks() {
		replaceTraversals(b.Body(), search, replacement)
	}
}

// replacedTokens replaces each traversal start search in tokens, and
// reports whether there was one: identifiers separated by dots, not
// preceded by a dot.
func replacedTokens(tokens hclwrite.Tokens, search, replacement []string) (hclwrite.Tokens, bool) {
	n := 2*len(search) - 1
	var out hclwrite.Tokens
	replaced := false
	for i := 0; i < len(tokens); i++ {
		if i+n <= len(tokens) && (i == 0 || tokens[i-1].Type != hclsyntax.TokenDot) && matchesTraversal(tokens[i:i+n], search) {
			out = append(out, hclwrite.TokensForTraversal(traversalOf(replacement))...)
			i += n - 1
			replaced = true
			continue
		}
		out = append(out, tokens[i])
	}
	return out, replaced
}

func matchesTraversal(tokens hclwrite.Tokens, names []string) bool {
	for i, name := range names {
		if tokens[2*i].Type != hclsyntax.TokenIdent || string(tokens[2*i].Bytes) != name {
			return false
		}
		if i > 0 && tokens[2*i-1].Type != hclsyntax.TokenDot {
			return false
		}
	}
	return true
}

func traversalOf(names []string) hcl.Traversal {
	t := hcl.Traversal{hcl.TraverseRoot{Name: names[0]}}
	for _, name := range names[1:] {
		t = append(t, hcl.TraverseAttr{Name: name})
	}
	return t
}

// dataKey identifies what a data block reads: its type and the ID its
// source's argument (see DataSource) is set to, or "".
func dataKey(b *hclsyntax.Block, sources map[string]DataSource) string {
	for _, source := range sources {
		if source.Type != b.Labels[0] {
			continue
		}
		if id := stringAttribute(b.Body, source.Argument); id != "" {
			return source.Type + "\x00" + id
		}
	}
	return ""
}

// mergeLocals adds staging's locals to root's locals.tf, but for those its
// provider configuration uses, which root has its own of.
func mergeLocals(staging, root string) error {
	staged, err := loadOptionalHCL(filepath.Join(staging, LocalsFileName))
	if err != nil || staged == nil {
		return err
	}
	providers, err := loadHCL(filepath.Join(staging, ProvidersFileName))
	if err != nil {
		return err
	}
	used := map[string]bool{}
	for _, b := range providers.syntax.Blocks {
		for _, t := range allTraversals(b.Body) {
			if t.RootName() == "local" && len(t) > 1 {
				if attr, ok := t[1].(hcl.TraverseAttr); ok {
					used[attr.Name] = true
				}
			}
		}
	}
	type local struct {
		name   string
		tokens hclwrite.Tokens
	}
	var added []local
	for _, b := range staged.file.Body().Blocks() {
		if b.Type() != "locals" {
			continue
		}
		for name, attr := range b.Body().Attributes() {
			if !used[name] {
				added = append(added, local{name, attr.Expr().BuildTokens(nil)})
			}
		}
	}
	if len(added) == 0 {
		return nil
	}
	sort.Slice(added, func(i, j int) bool { return added[i].name < added[j].name })
	path := filepath.Join(root, LocalsFileName)
	f, err := loadOptionalHCL(path)
	if err != nil {
		return err
	}
	var body *hclwrite.Body
	if f == nil {
		f = &hclFile{path: path, file: hclwrite.NewEmptyFile()}
	} else {
		for _, b := range f.file.Body().Blocks() {
			if b.Type() == "locals" {
				body = b.Body()
				break
			}
		}
	}
	if body == nil {
		body = f.file.Body().AppendNewBlock("locals", nil).Body()
	}
	for _, l := range added {
		body.SetAttributeRaw(l.name, l.tokens)
	}
	return f.save()
}

// appendBlocks adds blocks to the end of the file at path, which starts
// with header if it is new.
func appendBlocks(path, header string, blocks []*hclwrite.Block) error {
	if len(blocks) == 0 {
		return nil
	}
	f, err := loadOptionalHCL(path)
	if err != nil {
		return err
	}
	if f == nil {
		f = &hclFile{path: path, file: hclwrite.NewEmptyFile()}
		if header != "" {
			f.file.Body().AppendUnstructuredTokens(hclwrite.Tokens{{Type: hclsyntax.TokenComment, Bytes: []byte(header)}})
		}
	}
	for _, b := range blocks {
		if len(f.file.Body().Blocks()) > 0 || header != "" {
			f.file.Body().AppendNewline()
		}
		f.file.Body().AppendBlock(b)
	}
	return f.save()
}

// appendFile adds content to the end of the file at path.
func appendFile(path string, content []byte) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n\n")) {
		existing = append(bytes.TrimRight(existing, "\n"), '\n', '\n')
	}
	return os.WriteFile(path, append(existing, content...), 0o644)
}

// loadOptionalHCL loads the file at path, or returns nil if there is none.
func loadOptionalHCL(path string) (*hclFile, error) {
	f, err := loadHCL(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return f, err
}
