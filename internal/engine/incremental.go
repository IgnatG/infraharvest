// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/fsutil"
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

// rootNames returns the names files use (see Names), with those that
// moved and removed blocks take resources from: a plan rejects a moved
// block from an address the configuration declares.
func rootNames(files []*hclFile) Names {
	names := Names{}
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			switch {
			case b.Type == "moved" || b.Type == "removed":
				if from, ok := movedFrom(b); ok {
					names[from] = true
				}
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
			add(AddressType(r.Address), r.ID, "")
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
// resource through dt.Block (an argument with dt.Argument), if they are
// literal, directly or through a local. Without dt.Block, they are the
// local the resources refer to, local.<dt.Attribute>.
func appliedTags(files []*hclFile, dt DefaultTags) (map[string]string, bool) {
	if dt.Block == "" {
		return literalLocal(files, dt.Attribute)
	}
	var expr hclsyntax.Expression
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			if b.Type != "provider" || len(b.Labels) != 1 || b.Labels[0] != dt.Provider {
				continue
			}
			if attr, ok := b.Body.Attributes[dt.Block]; ok && dt.Argument {
				expr = attr.Expr
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
	return literalLocal(files, local.Name)
}

// literalLocal returns the local named name, if it is a literal map of
// strings.
func literalLocal(files []*hclFile, name string) (map[string]string, bool) {
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			if attr, ok := b.Body.Attributes[name]; ok && b.Type == "locals" {
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
// plan needs its state. Should running it fail, or its secrets check, root
// is left as it was.
//
// Staging may hold the lock file to generate with, such as root's: Add
// empties staging but for it, and pins the providers opts.Config requires
// to its releases (see pinnedToLock), so that the additions are generated
// with the provider releases root runs.
func Add(ctx context.Context, tf, rootTF Terraform, staging, root string, imports []Import, opts Options, existing map[External]string) (*Result, error) {
	rootTF = withTimeout(rootTF, opts.CommandTimeout)
	lock, err := os.ReadFile(filepath.Join(staging, LockFileName))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := os.RemoveAll(staging); err != nil {
		return nil, err
	}
	if lock != nil {
		if err := os.MkdirAll(staging, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(staging, LockFileName), lock, 0o644); err != nil {
			return nil, err
		}
	}
	files, err := configFiles(root)
	if err != nil {
		return nil, err
	}
	r, err := adding(opts, lock, files, existing)
	if err != nil {
		return nil, err
	}
	result, err := generate(ctx, tf, staging, imports, r)
	if err != nil {
		return nil, err
	}
	// What follows changes root: undo it on failure, so that root stays as
	// it was and a rerun adds the resources again.
	// The resources go into the files of their types, such as network.tf,
	// after the root's own blocks: those files are backed up too.
	fileOf := blockFiles(result.Imported, result.Modules)
	// The providers that added module calls use go into providers.tf and
	// the file of the root's required_providers (see mergeProviders).
	names := []string{DataFileName, ImportsFileName, VariablesFileName, LocalsFileName, RejectedFileName, LockFileName, ProvidersFileName, VersionsFileName}
	if required := requiredProvidersFile(files); !slices.Contains(names, required) {
		names = append(names, required)
	}
	for _, name := range fileOf {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	backup, err := backupFiles(root, names...)
	if err != nil {
		return nil, err
	}
	added, err := merge(staging, root, existing, opts.DataSources, fileOf)
	if err == nil && r.opts.DefaultTags != nil {
		// The added resources without the root's default tags use the
		// configuration without them, which root may not have yet.
		_, err = addRootAlias(staging, root, *r.opts.DefaultTags)
	}
	undo := func() error {
		for _, name := range added {
			if _, backedUp := backup.files[name]; backedUp {
				continue
			}
			if err := os.Remove(filepath.Join(root, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return backup.restore()
	}
	if err != nil {
		return nil, errors.Join(err, undo())
	}
	result.Files = added
	// After merge: the calls it added need their modules installed.
	if err := initTerraform(ctx, rootTF, tfexec.Backend(false)); err != nil {
		return nil, errors.Join(fmt.Errorf("terraform init: %w", err), undo())
	}
	staged := result.Gate
	// The root's gate knows the organisation's own modules root calls, as
	// Verify's does, although staging calls none of them. Only staging's
	// plan knew the secret values to look for: where root has the ones it
	// found, its secrets check fails and its scanners don't run either.
	secrets := staged.check(CheckSecrets)
	policy := fullGate(opts)
	policy.plan = func() (Check, []string, error) { return staged.check(CheckPlan), nil, nil }
	policy.withheld = &secrets
	gate, _, err := policy.run(ctx, rootTF, root)
	if err != nil {
		return nil, errors.Join(err, undo())
	}
	// A secret value in root, which the output must never keep, undoes
	// the addition.
	for _, c := range gate {
		if c.Name == CheckSecrets && !c.Passed {
			return nil, errors.Join(fmt.Errorf("%s failed, so nothing is added: %s", c.Name, strings.Join(c.Details, "; ")), undo())
		}
	}
	result.Gate = gate
	if err := os.RemoveAll(staging); err != nil {
		return nil, err
	}
	return result, nil
}

// adding is Add's run with opts, of resources to add to a root whose
// configuration files are root, and which has the resources existing (see
// Existing). Its options differ from the caller's (see generating): with
// lock, the lock file staging generates with, the providers opts.Config
// requires are pinned to its releases (see pinnedToLock); it names
// nothing as root does; it makes no local modules, and calls of curated
// modules only where it can by the same source, as a directory's module
// is called by its path relative to the root it is in, and staging isn't
// root; it lifts the tags root's provider applies, if any; the resources
// root has are external; and it writes the configuration into
// generated.tf, whose resources merge then puts after the blocks of the
// file of their type, or into a file of their own. The caller's opts stay
// as they are.
func adding(opts Options, lock []byte, root []*hclFile, existing map[External]string) (*run, error) {
	r := generating(opts)
	o := &r.opts
	if versions, ok := opts.Config[VersionsFileName]; ok && lock != nil {
		pinned, err := pinnedToLock(versions, lock)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", VersionsFileName, err)
		}
		o.Config = maps.Clone(opts.Config)
		o.Config[VersionsFileName] = pinned
	}
	r.taken = rootNames(root)
	o.ModulesDir = ""
	o.Adapters = slices.DeleteFunc(slices.Clone(opts.Adapters), func(a adapters.Adapter) bool { return filepath.IsAbs(a.Source) })
	if opts.DefaultTags != nil {
		dt := *opts.DefaultTags
		if applied, ok := appliedTags(root, dt); ok {
			dt.Applied = applied
			o.DefaultTags = &dt
		} else {
			// Lifting tags would change root's provider, or the tags its
			// resources refer to.
			o.DefaultTags = nil
		}
	}
	external := map[External]bool{}
	for _, e := range opts.External {
		external[ResourceKey(e.Type, e.ID)] = true
	}
	o.External = slices.Clone(opts.External)
	for _, e := range sortedExternal(existing) {
		if !external[ResourceKey(e.Type, e.ID)] {
			o.External = append(o.External, e)
		}
	}
	o.SplitFiles = false
	return r, nil
}

func sortedExternal(m map[External]string) []External {
	list := make([]External, 0, len(m))
	for e := range m {
		list = append(list, e)
	}
	sortExternal(list)
	return list
}

// sortExternal sorts list by type, then ID, then name, with a name before
// none.
func sortExternal(list []External) {
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		if (a.Name == "") != (b.Name == "") {
			return a.Name != ""
		}
		return a.Name < b.Name
	})
}

// With returns what a root holds once an incremental import added added
// to it (see Add): r's resources and added's, in the files of both, with
// added's checks. Its addresses are both's, unless r, as from an older
// release, recorded none for what it imports: InferredAddresses and the
// root's import blocks then tell all of them, not only added's. Its moves
// are r's: Add leaves moves.tf as it is.
func (r *Result) With(added *Result) *Result {
	if r == nil {
		return added
	}
	var addresses []Managed
	if len(r.Addresses) > 0 || len(r.Imported) == 0 {
		addresses = append(slices.Clone(r.Addresses), added.Addresses...)
		sortManaged(addresses)
	}
	files := slices.Clone(r.Files)
	for _, name := range added.Files {
		if !slices.Contains(files, name) {
			files = append(files, name)
		}
	}
	slices.Sort(files)
	return &Result{
		Imported:  append(slices.Clone(r.Imported), added.Imported...),
		Secrets:   append(slices.Clone(r.Secrets), added.Secrets...),
		Rejected:  append(slices.Clone(r.Rejected), added.Rejected...),
		Owned:     append(slices.Clone(r.Owned), added.Owned...),
		Modules:   append(slices.Clone(r.Modules), added.Modules...),
		Warnings:  append(slices.Clone(r.Warnings), added.Warnings...),
		Gate:      added.Gate,
		Files:     files,
		Addresses: addresses,
		Moves:     r.Moves,
		Removed:   r.Removed,
		Arrived:   r.Arrived,
	}
}

// AddedFileName returns the name of the n-th file of resources an
// incremental import adds: generated_2.tf, generated_3.tf, ...
func AddedFileName(n int) string {
	return fmt.Sprintf("generated_%d.tf", n)
}

// merge adds what Generate wrote into staging to root: each resource and
// module call after the blocks of the file fileOf names for it by address
// (see blockFiles), such as network.tf, or, where it names none, in a file
// of their own (see AddedFileName); and the import blocks, variables,
// locals, data sources and resources left out after root's own.
// References to data sources that read a resource root has, existing, as
// a resource block, or that root already has, use root's, as do references
// to variables root declares the same (see stagedVariables). The provider
// configuration and the locals it uses stay root's, but for the providers
// root has none of, which it gets (see mergeProviders). It returns the
// files of resources it wrote to, sorted, also when it fails after
// writing them.
func merge(staging, root string, existing map[External]string, sources map[string]DataSource, fileOf map[string]string) ([]string, error) {
	generated, err := loadHCL(filepath.Join(staging, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	rootFiles, err := configFiles(root)
	if err != nil {
		return nil, err
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
	declared := rootDeclarations(rootFiles)
	var data []*hclwrite.Block
	staged, err := loadOptionalHCL(filepath.Join(staging, DataFileName))
	if err != nil {
		return nil, err
	}
	if staged != nil {
		blocks := staged.edit().Body().Blocks()
		for i, b := range staged.syntax.Blocks {
			if b.Type != "data" || len(b.Labels) != 2 || i >= len(blocks) {
				continue
			}
			replacement, ok := rootData[dataKey(b, sources)]
			if !ok {
				// A data source root declares the same, such as a lookup
				// of the tenant: the staged one has another name.
				replacement, ok = declared[declarationKey(b, staged.src)]
			}
			if !ok {
				data = append(data, blocks[i])
				continue
			}
			for _, block := range generated.edit().Body().Blocks() {
				replaceTraversals(block.Body(), []string{"data", b.Labels[0], b.Labels[1]}, replacement)
			}
		}
	}
	variables, err := stagedVariables(staging, declared, generated)
	if err != nil {
		return nil, err
	}

	// The blocks for each file, in the order they come; those without one
	// go to the next file of their own.
	own := ""
	var names []string
	blocksFor := map[string][]*hclwrite.Block{}
	for _, b := range generated.edit().Body().Blocks() {
		name := fileOf[blockAddress(b)]
		if name == "" {
			if own == "" {
				if own, err = nextAddedFile(root); err != nil {
					return nil, err
				}
			}
			name = own
		}
		if _, ok := blocksFor[name]; !ok {
			names = append(names, name)
		}
		blocksFor[name] = append(blocksFor[name], b)
	}
	slices.Sort(names)
	var added []string
	for _, name := range names {
		added = append(added, name)
		if err := appendBlocks(filepath.Join(root, name), "", blocksFor[name]); err != nil {
			return added, err
		}
	}
	if err := appendBlocks(filepath.Join(root, DataFileName), dataFileHeader, data); err != nil {
		return added, err
	}
	imports, err := loadOptionalHCL(filepath.Join(staging, ImportsFileName))
	if err != nil {
		return added, err
	}
	if imports != nil {
		if err := appendBlocks(filepath.Join(root, ImportsFileName), "", imports.edit().Body().Blocks()); err != nil {
			return added, err
		}
	}
	if err := appendBlocks(filepath.Join(root, VariablesFileName), "", variables); err != nil {
		return added, err
	}
	if err := mergeLocals(staging, root); err != nil {
		return added, err
	}
	if err := mergeProviders(staging, root, rootFiles); err != nil {
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
			return added, fsutil.WriteFile(lock, content, 0o644)
		}
	}
	return added, nil
}

// replaceTraversals replaces the start of references that begin with
// search, such as data.aws_vpc.main, with replacement, which may have
// another length, such as aws_vpc.main, in body and its nested blocks.
func replaceTraversals(body *hclwrite.Body, search, replacement []string) {
	replaceReferences(body, search, hclwrite.TokensForTraversal(traversalOf(replacement)))
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

// rootDeclarations returns, by declarationKey, the address of each data
// source and each variable with a default that files declare, the first
// of those that declare the same: data.<type>.<name> or var.<name>, as
// steps of a reference.
func rootDeclarations(files []*hclFile) map[string][]string {
	declared := map[string][]string{}
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			var address []string
			switch {
			case b.Type == "data" && len(b.Labels) == 2:
				address = []string{"data", b.Labels[0], b.Labels[1]}
			case b.Type == "variable" && len(b.Labels) == 1 && hasDefault(b):
				address = []string{"var", b.Labels[0]}
			default:
				continue
			}
			key := declarationKey(b, f.src)
			if _, ok := declared[key]; key != "" && !ok {
				declared[key] = address
			}
		}
	}
	return declared
}

// hasDefault reports whether the variable block b has a default. One
// without, such as a secret's, takes a value of its own: two of them
// declared alike are still two values.
func hasDefault(b *hclsyntax.Block) bool {
	_, ok := b.Body.Attributes["default"]
	return ok
}

// declarationKey identifies what the data or variable block b declares:
// its type, its data source's type, and its arguments as src writes them,
// whitespace aside. Two blocks with one key declare the same, such as two
// lookups of the tenant Azure signs in to, or two variables for one
// resource's ID with the same default. It is "" for a block with nested
// blocks, which it doesn't compare.
func declarationKey(b *hclsyntax.Block, src []byte) string {
	if len(b.Labels) == 0 || len(b.Body.Blocks) > 0 {
		return ""
	}
	key := b.Type + "\x00" + strings.Join(b.Labels[:len(b.Labels)-1], ".")
	names := make([]string, 0, len(b.Body.Attributes))
	for name := range b.Body.Attributes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r := b.Body.Attributes[name].Expr.Range()
		if r.Start.Byte < 0 || r.End.Byte > len(src) || r.Start.Byte > r.End.Byte {
			return ""
		}
		key += "\x00" + name + "=" + strings.Join(strings.Fields(string(src[r.Start.Byte:r.End.Byte])), " ")
	}
	return key
}

// stagedVariables returns the blocks of staging's variables.tf to add to
// root's, but for the variables with a default that root declares the
// same (see rootDeclarations): references to those in generated's blocks
// refer to root's instead, which has another name.
func stagedVariables(staging string, declared map[string][]string, generated *hclFile) ([]*hclwrite.Block, error) {
	staged, err := loadOptionalHCL(filepath.Join(staging, VariablesFileName))
	if err != nil || staged == nil {
		return nil, err
	}
	blocks := staged.edit().Body().Blocks()
	var kept []*hclwrite.Block
	for i, b := range staged.syntax.Blocks {
		if i >= len(blocks) {
			break
		}
		if b.Type == "variable" && len(b.Labels) == 1 && hasDefault(b) {
			if replacement, ok := declared[declarationKey(b, staged.src)]; ok {
				for _, block := range generated.edit().Body().Blocks() {
					replaceTraversals(block.Body(), []string{"var", b.Labels[0]}, replacement)
				}
				continue
			}
		}
		kept = append(kept, blocks[i])
	}
	return kept, nil
}

// mergeLocals adds staging's locals to root's locals.tf, but for those its
// provider configuration uses and those root has, such as the tags its
// resources refer to: root has its own of them.
func mergeLocals(staging, root string) error {
	staged, err := loadOptionalHCL(filepath.Join(staging, LocalsFileName))
	if err != nil || staged == nil {
		return err
	}
	rootFiles, err := configFiles(root)
	if err != nil {
		return err
	}
	taken := rootNames(rootFiles)
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
	for _, b := range staged.edit().Body().Blocks() {
		if b.Type() != "locals" {
			continue
		}
		for name, attr := range b.Body().Attributes() {
			if !used[name] && !taken["local."+name] {
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
		f = newHCL(path)
	} else {
		for _, b := range f.edit().Body().Blocks() {
			if b.Type() == "locals" {
				body = b.Body()
				break
			}
		}
	}
	if body == nil {
		body = f.edit().Body().AppendNewBlock("locals", nil).Body()
	}
	for _, l := range added {
		body.SetAttributeRaw(l.name, l.tokens)
	}
	return f.save()
}

// mergeProviders adds to root the providers staging configures and
// requires that root doesn't, such as google-beta, which a curated module
// creates resources with (see writeModuleProviders): each block of
// staging's providers.tf for a provider root has no block for goes to
// root's providers.tf, and each provider staging's versions.tf requires
// and root doesn't goes, with the version staging pinned it to, to root's
// required_providers (see requiredProvidersFile). Without them, an added
// module call would run on a provider root neither configures nor pins.
// rootFiles are root's files before the merge.
func mergeProviders(staging, root string, rootFiles []*hclFile) error {
	configured := map[string]bool{}
	required := map[string]bool{}
	for _, f := range rootFiles {
		for _, b := range f.syntax.Blocks {
			switch b.Type {
			case "provider":
				if len(b.Labels) == 1 {
					configured[b.Labels[0]] = true
				}
			case "terraform":
				for _, nested := range b.Body.Blocks {
					if nested.Type != "required_providers" {
						continue
					}
					for name := range nested.Body.Attributes {
						required[name] = true
					}
				}
			}
		}
	}
	providers, err := loadOptionalHCL(filepath.Join(staging, ProvidersFileName))
	if err != nil {
		return err
	}
	if providers != nil {
		var blocks []*hclwrite.Block
		for _, b := range providers.edit().Body().Blocks() {
			if b.Type() == "provider" && len(b.Labels()) == 1 && !configured[b.Labels()[0]] {
				blocks = append(blocks, b)
			}
		}
		if err := appendBlocks(filepath.Join(root, ProvidersFileName), "", blocks); err != nil {
			return err
		}
	}
	versions, err := loadOptionalHCL(filepath.Join(staging, VersionsFileName))
	if err != nil || versions == nil {
		return err
	}
	missing := map[string]hclwrite.Tokens{}
	for _, b := range versions.edit().Body().Blocks() {
		if b.Type() != "terraform" {
			continue
		}
		for _, nested := range b.Body().Blocks() {
			if nested.Type() != "required_providers" {
				continue
			}
			for name, attr := range nested.Body().Attributes() {
				// A root that requires none of its providers, as one
				// written by hand may, gets only the new ones.
				if !required[name] && (len(required) > 0 || !configured[name]) {
					missing[name] = attr.Expr().BuildTokens(nil)
				}
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	path := filepath.Join(root, requiredProvidersFile(rootFiles))
	f, err := loadOptionalHCL(path)
	if err != nil {
		return err
	}
	if f == nil {
		f = newHCL(path)
	}
	body := requiredProvidersBody(f.edit().Body())
	names := make([]string, 0, len(missing))
	for name := range missing {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body.SetAttributeRaw(name, missing[name])
	}
	return f.save()
}

// requiredProvidersFile returns the name of the file of files that has
// the root's required_providers, or versions.tf, where Generate writes
// them, if none has.
func requiredProvidersFile(files []*hclFile) string {
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			if b.Type != "terraform" {
				continue
			}
			for _, nested := range b.Body.Blocks {
				if nested.Type == "required_providers" {
					return filepath.Base(f.path)
				}
			}
		}
	}
	return VersionsFileName
}

// requiredProvidersBody returns the body of body's required_providers
// block, adding the block, and a terraform block for it, where body has
// none.
func requiredProvidersBody(body *hclwrite.Body) *hclwrite.Body {
	var terraform *hclwrite.Block
	for _, b := range body.Blocks() {
		if b.Type() != "terraform" {
			continue
		}
		if required := b.Body().FirstMatchingBlock("required_providers", nil); required != nil {
			return required.Body()
		}
		if terraform == nil {
			terraform = b
		}
	}
	if terraform == nil {
		if len(body.Blocks()) > 0 {
			body.AppendNewline()
		}
		terraform = body.AppendNewBlock("terraform", nil)
	}
	return terraform.Body().AppendNewBlock("required_providers", nil).Body()
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
		f = newHCL(path)
		if header != "" {
			f.edit().Body().AppendUnstructuredTokens(hclwrite.Tokens{{Type: hclsyntax.TokenComment, Bytes: []byte(header)}})
		}
	}
	for _, b := range blocks {
		if len(f.edit().Body().Blocks()) > 0 || header != "" {
			f.edit().Body().AppendNewline()
		}
		f.edit().Body().AppendBlock(b)
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
	return fsutil.WriteFile(path, append(existing, content...), 0o644)
}

// nextAddedFile returns the first of generated_2.tf, generated_3.tf and so
// on (see AddedFileName) that root doesn't have.
func nextAddedFile(root string) (string, error) {
	for n := 2; ; n++ {
		name := AddedFileName(n)
		if _, err := os.Stat(filepath.Join(root, name)); errors.Is(err, fs.ErrNotExist) {
			return name, nil
		} else if err != nil {
			return "", err
		}
	}
}
