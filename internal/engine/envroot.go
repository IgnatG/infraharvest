// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// mainFileName holds the resources whose roots name no file of their own
// for them.
const mainFileName = "main.tf"

// mergedGitignore keeps the merged root's var files in version control,
// which the output directory's .gitignore leaves out as .tfvars files.
const mergedGitignore = `# The environments' var files hold no secret values (see README.md):
# commit them.
!` + hclfile.VarsDir + `/**/*.tfvars
`

// rootWriter writes the files of a merged root.
type rootWriter struct {
	m *stackMerge
	p rootfile.Provider
	// fileOf is the file each resource of m, and each forEach, goes in.
	fileOf map[*stackResource]string
	// groupFile is the file each forEach goes in: its first key's.
	groupFile map[*forEachGroup]string
	// sectionOf is the section of the var files each variable is in: the
	// file of what it is for, "" for the variables of the root as a
	// whole.
	sectionOf map[*envVariable]string
	// extra are the variables of the provider block (see providerFile),
	// copied the variables the roots declare that the merged code reads
	// as they are, and region the variable of an AWS root's region.
	extra  []*envVariable
	copied []*hclwrite.Block
	region *envVariable
	// used are the names of the variables, so that extra ones get names
	// of their own.
	used map[string]bool
	// noEnv is set when nothing reads var.env, such as where no name has
	// an environment token: the root then declares none, which would be
	// an unused declaration, and the var files set none.
	noEnv bool
}

// readsEnv matches a reference to var.env.
var readsEnv = regexp.MustCompile(`\bvar\.` + envVariableName + `\b`)

// mergedRootFiles returns the files of m's merged root, the one root
// that every environment plans and applies with its own var files, by
// path with forward slashes: providers.tf, with the terraform block and its partial
// backend; variables.tf; locals.tf and data.tf, where values read them;
// the resources, split into the files their roots put them in; imports.tf,
// whose IDs come from each environment's vars/<env>/imports.tfvars;
// vars/common.tfvars and vars/<env>/terraform.tfvars and backend.hcl; a
// README.md and a .gitignore. It fails where the roots can't share one
// root as they are: they use different providers or versions, set a
// provider argument that differs and isn't a value, or a resource refers
// to locals or data sources of the roots.
func mergedRootFiles(m *stackMerge) (map[string][]byte, error) {
	files, _, err := buildMergedRoot(m)
	return files, err
}

// buildMergedRoot is mergedRootFiles, which also returns the writer that
// wrote the files, which knows their variables.
func buildMergedRoot(m *stackMerge) (map[string][]byte, *rootWriter, error) {
	if len(m.roots) == 0 || len(m.roots) != len(m.envs) {
		return nil, nil, errors.New("no roots to merge")
	}
	if m.reason != "" {
		return nil, nil, errors.New(m.reason)
	}
	newest, err := newestProvider(m.roots)
	if err != nil {
		return nil, nil, err
	}
	w := &rootWriter{m: m, p: m.roots[newest].provider, fileOf: map[*stackResource]string{}, groupFile: map[*forEachGroup]string{}, sectionOf: map[*envVariable]string{}, used: map[string]bool{hclfile.ImportIDsVariableName: true, hclfile.ImportIdentitiesVariableName: true}}
	if w.p.Name == "" {
		return nil, nil, errors.New("the roots name no provider")
	}
	for _, v := range m.variables {
		w.used[v.name] = true
	}
	w.placeResources()
	files := map[string][]byte{}
	resources, err := w.resourceFiles()
	if err != nil {
		return nil, nil, err
	}
	maps.Copy(files, resources)
	if len(m.data) > 0 || len(m.rootData) > 0 {
		files[rootfile.DataFileName] = w.dataFile()
	}
	if err := w.copyVariables(files); err != nil {
		return nil, nil, err
	}
	if len(m.locals) > 0 {
		files[rootfile.LocalsFileName] = w.localsFile()
	}
	imports, err := w.importsFile()
	if err != nil {
		return nil, nil, err
	}
	if imports != nil {
		files[rootfile.ImportsFileName] = imports
	}
	// The provider block's variables come before variables.tf and the
	// var files, which hold them too.
	providers, err := w.providerBlocks()
	if err != nil {
		return nil, nil, err
	}
	w.noEnv = !readsEnv.Match(providers)
	for _, src := range files {
		if readsEnv.Match(src) {
			w.noEnv = false
		}
	}
	variables, err := w.variablesFile()
	if err != nil {
		return nil, nil, err
	}
	files[hclfile.VariablesFileName] = variables
	terraform, err := w.terraformBlock(files)
	if err != nil {
		return nil, nil, err
	}
	files[rootfile.ProvidersFileName] = formatHCL(string(terraform) + string(providers))
	files[hclfile.CommonVarsPath] = []byte("# Values every environment shares, which the defaults in variables.tf don't\n# give: none yet. Each environment's own are in " + hclfile.EnvVarsPath("<env>") + ".\n")
	for e, env := range m.envs {
		files[hclfile.EnvVarsPath(env)] = w.tfvarsFile(e)
		files[hclfile.ImportVarsPath(env)] = w.importVarsFile(e)
		if backend := w.backendConfig(e); backend != nil {
			files[hclfile.BackendConfigPath(env)] = backend
		}
	}
	files[rootfile.ReadmeFileName] = w.readme()
	files[".gitignore"] = []byte(mergedGitignore)
	return files, w, nil
}

// placeResources records the file each resource and forEach goes in: the
// one its first environment's root put it in, or main.tf; and the section
// of the var files each variable is in.
func (w *rootWriter) placeResources() {
	fileOf := func(r *stackResource) string {
		for e, member := range r.members {
			if member == nil {
				continue
			}
			if f := w.m.roots[e].files[member.block.Address]; f != "" && strings.HasSuffix(f, ".tf") && !rootfile.IsReserved(f) && !strings.Contains(f, "/") {
				return f
			}
			break
		}
		return mainFileName
	}
	section := func(v *envVariable, file string) {
		if _, ok := w.sectionOf[v]; !ok && v != nil {
			w.sectionOf[v] = file
		}
	}
	collect := func(b *mergedBody, file string) {
		if b == nil {
			return
		}
		b.visit(func(a *mergedAttribute) { section(a.variable, file) }, func(blk *mergedBlock) { section(blk.variable, file) })
	}
	for _, r := range w.m.resources {
		if r.forEach != nil {
			continue
		}
		w.fileOf[r] = fileOf(r)
		section(r.deploy, w.fileOf[r])
		collect(r.body, w.fileOf[r])
	}
	for _, g := range w.m.forEach {
		w.groupFile[g] = fileOf(g.keys[0])
		for _, k := range g.keys {
			w.fileOf[k] = w.groupFile[g]
		}
		section(g.variable, w.groupFile[g])
		collect(g.body, w.groupFile[g])
	}
}

// resourceFiles returns the files of the resources, each laid out as the
// style guide has it (see layout).
func (w *rootWriter) resourceFiles() (map[string][]byte, error) {
	type block struct{ address, text string }
	byFile := map[string][]block{}
	for _, r := range w.m.resources {
		if r.forEach != nil {
			continue
		}
		if r.body == nil {
			return nil, fmt.Errorf("%s: %s", r.address(), r.residue)
		}
		var meta []string
		if r.deploy != nil {
			meta = append(meta, fmt.Sprintf("count = var.%s ? 1 : 0", r.deploy.name))
		}
		text := resourceText(r.resourceType, r.label, meta, r.body)
		if r.resourceType == moduleType {
			text = moduleText(r.label, meta, r.body)
		}
		byFile[w.fileOf[r]] = append(byFile[w.fileOf[r]], block{r.address(), text})
	}
	for _, g := range w.m.forEach {
		meta := []string{"for_each = " + g.source()}
		byFile[w.groupFile[g]] = append(byFile[w.groupFile[g]], block{g.address(), resourceText(g.resourceType, g.label, meta, g.body)})
	}
	files := map[string][]byte{}
	for name, blocks := range byFile {
		sort.Slice(blocks, func(i, j int) bool { return blocks[i].address < blocks[j].address })
		texts := make([]string, len(blocks))
		for i, b := range blocks {
			texts[i] = b.text
		}
		src := []byte(strings.Join(texts, "\n"))
		out, ok := hclfile.Layout(src)
		if !ok {
			return nil, fmt.Errorf("%s doesn't parse:\n%s", name, src)
		}
		files[name] = out
	}
	return files, nil
}

// resourceText writes a resource block of type typ and label, with meta,
// its meta-arguments, and body.
func resourceText(typ, label string, meta []string, body *mergedBody) string {
	var b strings.Builder
	fmt.Fprintf(&b, "resource %q %q {\n", typ, label)
	for _, line := range meta {
		b.WriteString(line + "\n")
	}
	writeMergedBody(&b, body)
	b.WriteString("}\n")
	return b.String()
}

// writeMergedBody writes the arguments and nested blocks of body; a
// block some environments have more or fewer of becomes a dynamic block
// over its variable.
func writeMergedBody(b *strings.Builder, body *mergedBody) {
	for _, a := range body.attributes {
		fmt.Fprintf(b, "%s = %s\n", a.name, exprText(a.value))
	}
	for _, blk := range body.blocks {
		if blk.body != nil {
			b.WriteString("\n" + blk.typ + " {\n")
			writeMergedBody(b, blk.body)
			b.WriteString("}\n")
			continue
		}
		forEach := "var." + blk.variable.name
		if blk.single {
			forEach = fmt.Sprintf("var.%s == null ? [] : [var.%s]", blk.variable.name, blk.variable.name)
		}
		fmt.Fprintf(b, "\ndynamic %q {\nfor_each = %s\n\ncontent {\n", blk.typ, forEach)
		for _, f := range blk.fields {
			fmt.Fprintf(b, "%s = %s.value.%s\n", f, blk.typ, f)
		}
		b.WriteString("}\n}\n")
	}
}

// exprText is tokens, an expression, as text: without "${...}" around a
// template of a single expression, which stands for the expression.
func exprText(tokens hclwrite.Tokens) string {
	src := bytes.TrimSpace(tokens.Bytes())
	expr, diags := hclsyntax.ParseExpression(src, "", hcl.InitialPos)
	if diags.HasErrors() {
		return string(src)
	}
	// Each one, as in a tag's value inside an object: the outermost, from
	// the last, so that the offsets of the others hold.
	var wraps []*hclsyntax.TemplateWrapExpr
	_ = hclsyntax.VisitAll(expr, func(n hclsyntax.Node) hcl.Diagnostics {
		if wrap, ok := n.(*hclsyntax.TemplateWrapExpr); ok {
			for _, outer := range wraps {
				if outer.SrcRange.Start.Byte <= wrap.SrcRange.Start.Byte && wrap.SrcRange.End.Byte <= outer.SrcRange.End.Byte {
					return nil
				}
			}
			wraps = append(wraps, wrap)
		}
		return nil
	})
	sort.Slice(wraps, func(i, j int) bool { return wraps[i].SrcRange.Start.Byte > wraps[j].SrcRange.Start.Byte })
	out := slices.Clone(src)
	for _, wrap := range wraps {
		inner := wrap.Wrapped.Range()
		out = slices.Concat(out[:wrap.SrcRange.Start.Byte], src[inner.Start.Byte:inner.End.Byte], out[wrap.SrcRange.End.Byte:])
	}
	return string(out)
}

// copyVariables declares the variables of the roots that the merged
// resources read as they are, as the roots declare them, such as a
// secret's variable a resource of one environment sets. It fails on a
// local value or data source of the roots, which the merged root doesn't
// have.
func (w *rootWriter) copyVariables(files map[string][]byte) error {
	declared := map[string]bool{}
	for name := range w.used {
		declared["var."+name] = true
	}
	for _, l := range w.m.locals {
		declared["local."+l.name] = true
	}
	for _, d := range w.m.data {
		declared["data."+d] = true
	}
	for _, d := range w.m.rootData {
		declared["data."+d.address] = true
	}
	var missing []string
	read := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		f, err := hclfile.Parse(name, files[name])
		if err != nil {
			return fmt.Errorf("%s doesn't parse", name)
		}
		_ = hclsyntax.VisitAll(f.Syntax, func(n hclsyntax.Node) hcl.Diagnostics {
			expr, ok := n.(hclsyntax.Expression)
			if !ok {
				return nil
			}
			for _, t := range expr.Variables() {
				ref := referenceName(t)
				if ref == "" || declared[ref] || read[ref] {
					continue
				}
				read[ref] = true
				missing = append(missing, ref)
			}
			return nil
		})
	}
	sort.Strings(missing)
	var unknown []string
	for _, ref := range missing {
		name, ok := strings.CutPrefix(ref, "var.")
		if !ok {
			unknown = append(unknown, ref)
			continue
		}
		declaring := make([]*hclwrite.Block, len(w.m.roots))
		var found *hclwrite.Block
		same := true
		for e, root := range w.m.roots {
			if root.variables == nil {
				continue
			}
			for _, b := range root.variables.Edit().Body().Blocks() {
				if b.Type() == "variable" && len(b.Labels()) == 1 && b.Labels()[0] == name {
					declaring[e] = b
					break
				}
			}
			switch {
			case declaring[e] == nil:
			case found == nil:
				found = declaring[e]
			case strings.TrimSpace(string(declaring[e].BuildTokens(nil).Bytes())) != strings.TrimSpace(string(found.BuildTokens(nil).Bytes())):
				same = false
			}
		}
		if found == nil {
			unknown = append(unknown, ref)
			continue
		}
		w.used[name] = true
		if same {
			if slices.Contains(declaring, nil) && found.Body().GetAttribute("default") == nil {
				// Some environments don't set it, such as a secret of a
				// resource of one environment: null, where they don't.
				copied, err := hclfile.CopyBlock(found)
				if err != nil {
					return err
				}
				copied.Body().SetAttributeRaw("default", hclwrite.TokensForValue(cty.NullVal(cty.DynamicPseudoType)))
				found = copied
			}
			w.copied = append(w.copied, found)
			continue
		}
		// The roots declare it otherwise, such as with another default,
		// the ID of a resource of another root: each environment's value
		// is its own.
		w.extra = append(w.extra, declaredVariable(name, declaring, len(w.m.envs)))
	}
	if len(unknown) > 0 {
		return fmt.Errorf("the merged resources read %s, which the merged root doesn't declare", strings.Join(unknown, ", "))
	}
	return nil
}

// declaredVariable returns variable name, which the roots, by
// environment, declare as declaring has it (nil where a root doesn't)
// but not all alike, as a variable of the merged root: of the type and
// description of the first, sensitive if it is, with each environment's
// default as its value, and a default (see envVariable.setDefault) of
// envs environments.
func declaredVariable(name string, declaring []*hclwrite.Block, envs int) *envVariable {
	v := &envVariable{name: name, what: "value of " + name, ty: cty.DynamicPseudoType, values: map[int]hclwrite.Tokens{}}
	first := true
	for e, b := range declaring {
		if b == nil {
			continue
		}
		body := b.Body()
		if first {
			first = false
			if attr := body.GetAttribute("description"); attr != nil {
				if d, ok := literalValue(attr.Expr().BuildTokens(nil)); ok && d.Type() == cty.String {
					v.description = d.AsString()
				}
			}
			if attr := body.GetAttribute("type"); attr != nil {
				expr, diags := hclsyntax.ParseExpression(attr.Expr().BuildTokens(nil).Bytes(), "", hcl.InitialPos)
				if !diags.HasErrors() {
					if ty, diags := typeexpr.TypeConstraint(expr); !diags.HasErrors() {
						v.ty = ty
					}
				}
			}
			if attr := body.GetAttribute("sensitive"); attr != nil {
				if s, ok := literalValue(attr.Expr().BuildTokens(nil)); ok && s.Type() == cty.Bool && s.True() {
					v.sensitive = true
				}
			}
		}
		switch attr := body.GetAttribute("default"); {
		case v.sensitive, attr == nil:
			v.values[e] = nil
		default:
			v.values[e] = attr.Expr().BuildTokens(nil)
		}
	}
	v.setDefault(envs)
	return v
}

// referenceName returns what t refers to, as var.x, local.x or
// data.type.name, or "" for anything else.
func referenceName(t hcl.Traversal) string {
	parts := []string{t.RootName()}
	want := 2
	switch t.RootName() {
	case "var", "local":
	case "data":
		want = 3
	default:
		return ""
	}
	for _, step := range t[1:] {
		attr, ok := step.(hcl.TraverseAttr)
		if !ok || len(parts) == want {
			break
		}
		parts = append(parts, attr.Name)
	}
	if len(parts) < want {
		return ""
	}
	return strings.Join(parts, ".")
}

// localsFile writes locals.tf: naming_prefix and the scope's and region's
// IDs first, then the resources' names, under a comment naming the file
// of their resources.
func (w *rootWriter) localsFile() []byte {
	fileOfLocal := map[string]string{}
	for _, r := range w.m.resources {
		if r.body == nil {
			continue
		}
		for _, a := range r.body.attributes {
			if a.local != "" {
				fileOfLocal[a.local] = w.fileOf[r]
			}
		}
	}
	for _, g := range w.m.forEach {
		if g.local != "" {
			fileOfLocal[g.local] = w.groupFile[g]
		}
	}
	// The resources' names by file, each file's in the model's order.
	locals := slices.Clone(w.m.locals)
	sort.SliceStable(locals, func(i, j int) bool { return fileOfLocal[locals[i].name] < fileOfLocal[locals[j].name] })
	var b strings.Builder
	b.WriteString("locals {\n")
	section := ""
	for i, l := range locals {
		if f := fileOfLocal[l.name]; f != section {
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "# %s\n", strings.TrimSuffix(f, ".tf"))
			section = f
		}
		fmt.Fprintf(&b, "%s = %s\n", l.name, exprText(l.value))
	}
	b.WriteString("}\n")
	return formatHCL(b.String())
}

// dataFile writes data.tf: the data sources values read, such as the
// account's ID.
func (w *rootWriter) dataFile() []byte {
	var b strings.Builder
	b.WriteString(rootfile.DataFileHeader)
	written := map[string]bool{}
	for _, d := range w.m.rootData {
		typ, name, _ := strings.Cut(d.address, ".")
		fmt.Fprintf(&b, "\ndata %q %q {\n", typ, name)
		if d.deploy != nil {
			fmt.Fprintf(&b, "count = var.%s ? 1 : 0\n", d.deploy.name)
			if len(d.body.attributes) > 0 {
				b.WriteString("\n")
			}
		}
		writeMergedBody(&b, d.body)
		b.WriteString("}\n")
		written[d.address] = true
	}
	for _, d := range w.m.data {
		if written[d] {
			continue
		}
		typ, name, _ := strings.Cut(d, ".")
		fmt.Fprintf(&b, "\ndata %q %q {}\n", typ, name)
	}
	return formatHCL(b.String())
}

// importsFile writes imports.tf: an import block for each resource of the
// merged root, whose ID, or identity, each environment's imports.tfvars
// gives (see stackMerge.importKinds); for a resource some environments
// lack, or a forEach, with for_each over what the environment has. It returns nil for none.
func (w *rootWriter) importsFile() ([]byte, error) {
	return w.importsFileWithout(nil)
}

// importsFileWithout is importsFile without the import blocks of skip,
// resources of the merged root that aren't of a forEach.
func (w *rootWriter) importsFileWithout(skip map[*stackResource]bool) ([]byte, error) {
	hasID := func(r *stackResource) bool {
		for _, member := range r.members {
			if member != nil && member.id != "" {
				return true
			}
		}
		return false
	}
	kinds, err := w.m.importKinds()
	if err != nil {
		return nil, err
	}
	type block struct{ address, text string }
	var blocks []block
	for _, r := range w.m.resources {
		if r.resourceType != moduleType {
			continue
		}
		calls, err := moduleImports(r, kinds)
		if err != nil {
			return nil, err
		}
		for address, text := range calls {
			blocks = append(blocks, block{address, text})
		}
	}
	for _, r := range w.m.resources {
		if r.forEach != nil || !hasID(r) || skip[r] {
			continue
		}
		instance := string(hclwrite.TokensForTraversal(r.instance()).Bytes())
		kind, err := importKindOf(kinds, r.address(), instance)
		if err != nil {
			return nil, err
		}
		id := kind.value(string(hclwrite.TokensForValue(cty.StringVal(instance)).Bytes()))
		if r.deploy == nil {
			blocks = append(blocks, block{r.address(), fmt.Sprintf("import {\nto = %s\n%s\n}\n", instance, id)})
			continue
		}
		blocks = append(blocks, block{r.address(), fmt.Sprintf("import {\nfor_each = [for k in [0] : k if var.%s]\n\nto = %s.%s[each.value]\n%s\n}\n", r.deploy.name, r.resourceType, r.label, id)})
	}
	for _, g := range w.m.forEach {
		if !slices.ContainsFunc(g.keys, hasID) {
			continue
		}
		instances := make([]string, len(g.keys))
		for i, k := range g.keys {
			instances[i] = string(hclwrite.TokensForTraversal(k.instance()).Bytes())
		}
		kind, err := importKindOf(kinds, g.address(), instances...)
		if err != nil {
			return nil, err
		}
		format := string(hclwrite.TokensForValue(cty.StringVal(g.address() + "[%q]")).Bytes())
		blocks = append(blocks, block{g.address(), fmt.Sprintf("import {\nfor_each = %s\n\nto = %s[each.key]\n%s\n}\n", g.source(), g.address(), kind.value("format("+format+", each.key)"))})
	}
	if len(blocks) == 0 {
		return nil, nil
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].address < blocks[j].address })
	var b strings.Builder
	b.WriteString("# Each environment imports its resources on its first apply, with their IDs\n# from " + hclfile.ImportVarsPath("<env>") + ". Delete this file, and those, after it.\n")
	for _, blk := range blocks {
		b.WriteString("\n" + blk.text)
	}
	return formatHCL(b.String()), nil
}

// providerBlocks writes the provider block and returns it: the arguments
// the roots' providers share as they are, those that differ as variables
// (see extra), the region where the provider sets it (see
// MergeProfile.RegionArgument) as a variable, such as var.aws_region, and
// the default tags as the roots apply them (see defaultTagsText); and a second configuration of the provider for
// each alias the resources use, such as aws.without_default_tags, with
// the same arguments and no tags. The role a root's provider assumes is
// left out: each environment plans with its own credentials.
func (w *rootWriter) providerBlocks() ([]byte, error) {
	configs := make([]map[string]interface{}, len(w.m.roots))
	keys := map[string]bool{}
	for e, root := range w.m.roots {
		configs[e], _ = rootfile.WithoutCredentials(root.provider.Config)
		for k := range configs[e] {
			keys[k] = true
		}
	}
	var args strings.Builder
	if argument := w.m.profile.RegionArgument; argument != "" {
		region := ""
		for _, root := range w.m.roots {
			if r, ok := root.provider.Config[argument].(string); ok && r != "" {
				region = r
			} else if root.region != "" {
				region = root.region
			}
			if region != "" {
				break
			}
		}
		if region != "" {
			w.region = &envVariable{name: uniqueEnvName(w.used, w.m.profile.RegionLocal, ""), what: w.m.profile.RegionTitle + " of the stack", ty: cty.String, values: map[int]hclwrite.Tokens{}, hasDefault: true, defaultValue: hclwrite.TokensForValue(cty.StringVal(region))}
			for e := range w.m.envs {
				w.region.values[e] = w.region.defaultValue
			}
			fmt.Fprintf(&args, "%s = var.%s\n", argument, w.region.name)
		}
		delete(keys, argument)
	}
	for _, k := range []string{"assume_role", "default_tags", "default_labels", "alias"} {
		delete(keys, k)
	}
	var blocks strings.Builder
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		values := make([]hclwrite.Tokens, len(configs))
		var single *hclwrite.Body
		same := true
		for e, config := range configs {
			v, ok := config[k]
			if !ok {
				same = false
				continue
			}
			f := hclwrite.NewEmptyFile()
			if err := rootfile.WriteBody(f.Body(), map[string]interface{}{k: v}); err != nil {
				return nil, fmt.Errorf("provider %s: %w", w.p.Name, err)
			}
			if attr := f.Body().GetAttribute(k); attr != nil {
				values[e] = attr.Expr().BuildTokens(nil)
			} else {
				values[e] = hclwrite.Tokens{{Type: hclsyntax.TokenStringLit, Bytes: f.Bytes()}}
				single = f.Body()
			}
			if e > 0 && exprKey(values[e]) != exprKey(values[0]) {
				same = false
			}
		}
		switch {
		case same && single != nil:
			blocks.WriteString("\n" + string(single.BuildTokens(nil).Bytes()))
		case same:
			fmt.Fprintf(&args, "%s = %s\n", k, exprText(values[0]))
		case single != nil:
			return nil, fmt.Errorf("the provider's %s blocks differ between the environments", k)
		default:
			v := &envVariable{name: uniqueEnvName(w.used, k, ""), what: k + " of the " + w.p.Name + " provider", ty: cty.DynamicPseudoType, values: map[int]hclwrite.Tokens{}}
			for e, config := range configs {
				if raw, ok := config[k]; ok {
					if value, err := rootfile.CtyValue(raw); err == nil {
						v.ty = value.Type()
					}
					v.values[e] = values[e]
				}
			}
			v.setDefault(len(w.m.envs))
			w.extra = append(w.extra, v)
			w.sectionOf[v] = rootfile.ProvidersFileName
			fmt.Fprintf(&args, "%s = var.%s\n", k, v.name)
		}
	}
	tags := w.defaultTagsText()
	var out strings.Builder
	fmt.Fprintf(&out, "\nprovider %q {\n%s%s", w.p.Name, args.String(), blocks.String())
	switch {
	case tags == "":
	case slices.ContainsFunc(w.m.roots, func(r envRoot) bool { return r.defaultLabels }):
		// As the roots apply them, with an argument, as Google does its
		// labels.
		out.WriteString("default_labels = " + tags + "\n")
	default:
		out.WriteString("\ndefault_tags {\ntags = " + tags + "\n}\n")
	}
	out.WriteString("}\n")
	for _, alias := range w.aliases() {
		fmt.Fprintf(&out, "\nprovider %q {\nalias = %q\n%s%s}\n", w.p.Name, alias, args.String(), blocks.String())
	}
	return []byte(out.String()), nil
}

// aliases returns the aliases of the provider the resources use, such as
// without_default_tags for provider = aws.without_default_tags, sorted.
func (w *rootWriter) aliases() []string {
	found := map[string]bool{}
	check := func(b *mergedBody) {
		if b == nil {
			return
		}
		for _, a := range b.attributes {
			if a.name == "providers" {
				// A module call's, such as { aws = aws.without_default_tags }.
				if expr, diags := hclsyntax.ParseExpression(a.value.Bytes(), "", hcl.InitialPos); !diags.HasErrors() {
					for _, t := range expr.Variables() {
						if rel := t.SimpleSplit().Rel; len(rel) == 1 && t.RootName() == w.p.Name {
							if step, ok := rel[0].(hcl.TraverseAttr); ok {
								found[step.Name] = true
							}
						}
					}
				}
				continue
			}
			if a.name != "provider" {
				continue
			}
			if name, alias, ok := strings.Cut(exprText(a.value), "."); ok && name == w.p.Name && hclsyntax.ValidIdentifier(alias) {
				found[alias] = true
			}
		}
	}
	for _, r := range w.m.resources {
		check(r.body)
	}
	for _, g := range w.m.forEach {
		check(g.body)
	}
	return slices.Sorted(maps.Keys(found))
}

// defaultTagsText returns the tags of the provider's default_tags, as an
// expression, or "" where no root's provider applies any: a tag the same
// in every environment as it is, one that holds the environment's token
// or IDs as a template (Environment = var.env), and one that differs
// otherwise as a variable, <tag>_tag. The tags some environments only
// apply are a map variable, extra_default_tags, merged in.
func (w *rootWriter) defaultTagsText() string {
	keys := map[string]int{}
	for _, root := range w.m.roots {
		for k := range root.defaultTags {
			keys[k]++
		}
	}
	if len(keys) == 0 {
		return ""
	}
	var fields []string
	extra := map[int][]hclwrite.ObjectAttrTokens{}
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		name := k
		if !hclsyntax.ValidIdentifier(k) {
			name = string(hclwrite.TokensForValue(cty.StringVal(k)).Bytes())
		}
		if keys[k] < len(w.m.roots) {
			for e, root := range w.m.roots {
				if v, ok := root.defaultTags[k]; ok {
					extra[e] = append(extra[e], hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForValue(cty.StringVal(k)), Value: hclwrite.TokensForValue(cty.StringVal(v))})
				}
			}
			continue
		}
		values := make([]string, len(w.m.roots))
		normal := make([]string, len(w.m.roots))
		for e, root := range w.m.roots {
			values[e] = root.defaultTags[k]
			normal[e] = w.m.norms[e].value(values[e])
		}
		switch {
		case !slices.ContainsFunc(values, func(v string) bool { return v != values[0] }):
			fields = append(fields, name+" = "+string(hclwrite.TokensForValue(cty.StringVal(values[0])).Bytes()))
		case w.sameTemplate(normal):
			fields = append(fields, name+" = "+exprText(w.m.renderTemplate(hclwrite.TokensForValue(cty.StringVal(normal[0])))))
		default:
			v := &envVariable{name: uniqueEnvName(w.used, hclfile.Label(k)+"_tag", ""), what: k + " tag the provider applies to every resource", ty: cty.String, values: map[int]hclwrite.Tokens{}}
			for e := range w.m.roots {
				v.values[e] = hclwrite.TokensForValue(cty.StringVal(values[e]))
			}
			v.setDefault(len(w.m.envs))
			w.extra = append(w.extra, v)
			w.sectionOf[v] = rootfile.ProvidersFileName
			fields = append(fields, name+" = var."+v.name)
		}
	}
	tags := "{\n" + strings.Join(fields, "\n") + "\n}"
	if len(fields) == 0 {
		tags = ""
	}
	if len(extra) == 0 {
		return tags
	}
	v := &envVariable{name: uniqueEnvName(w.used, "extra_default_tags", ""), what: "tags the provider applies to every resource in some environments only", ty: cty.Map(cty.String), values: map[int]hclwrite.Tokens{}, hasDefault: true, defaultValue: hclwrite.TokensForObject(nil)}
	for e := range w.m.roots {
		v.values[e] = hclwrite.TokensForObject(extra[e])
	}
	w.extra = append(w.extra, v)
	w.sectionOf[v] = rootfile.ProvidersFileName
	if tags == "" {
		return "var." + v.name
	}
	return "merge(" + tags + ", var." + v.name + ")"
}

// sameTemplate reports whether normal, a value of each environment with
// placeholders, is the same in each, and holds a placeholder that the
// merged root can read: the environment's token, or the scope's or
// region's IDs where it has their locals.
func (w *rootWriter) sameTemplate(normal []string) bool {
	if slices.ContainsFunc(normal, func(v string) bool { return v != normal[0] }) {
		return false
	}
	locals := map[string]bool{}
	for _, l := range w.m.locals {
		locals[l.name] = true
	}
	switch {
	case strings.Contains(normal[0], accountPlaceholder) && !locals[w.m.profile.ScopeLocal]:
		return false
	case strings.Contains(normal[0], regionPlaceholder) && !locals[w.m.profile.RegionLocal]:
		return false
	case strings.Contains(normal[0], resourceGroupPlaceholder):
		return false
	}
	return strings.Contains(normal[0], envPlaceholder)
}

// terraformBlock writes the terraform block: required_version, from the
// roots' and raised to what the merged files use (see rootfile.FeaturesNeed), the
// provider's requirement, and the backend, its settings in each
// environment's backend.hcl, where the roots have one.
func (w *rootWriter) terraformBlock(files map[string][]byte) ([]byte, error) {
	var parsed []*hclfile.File
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if !strings.HasSuffix(name, ".tf") {
			continue
		}
		f, err := hclfile.Parse(name, files[name])
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, f)
	}
	need := rootfile.FeaturesNeed(parsed)
	for _, root := range w.m.roots {
		if root.requiredVersion == "" {
			continue
		}
		lower, err := rootfile.LowerBound(root.requiredVersion)
		if err != nil {
			continue
		}
		if v, err := version.NewVersion(strings.TrimPrefix(lower, ">= ")); err == nil && v.GreaterThan(need) {
			need = v
		}
	}
	f := hclwrite.NewEmptyFile()
	terraform := f.Body().AppendNewBlock("terraform", nil).Body()
	terraform.SetAttributeValue("required_version", cty.StringVal(fmt.Sprintf(">= %s, < %d.0", rootfile.ShortVersion(need), need.Segments()[0]+1)))
	requirement := map[string]cty.Value{"source": cty.StringVal(w.p.Source)}
	if w.p.Version != "" {
		requirement["version"] = cty.StringVal(w.p.Version)
	}
	terraform.AppendNewline()
	terraform.AppendNewBlock("required_providers", nil).Body().SetAttributeValue(w.p.Name, cty.ObjectVal(requirement))
	if typ := w.backendType(); typ != "" {
		terraform.AppendNewline()
		terraform.AppendNewBlock("backend", []string{typ})
	}
	return f.Bytes(), nil
}

// rootBackend returns the type and the arguments of the backend of
// environment env's root, in the order its backend.tf has them; "" for
// none.
func (w *rootWriter) rootBackend(env int) (string, []string, map[string]hclwrite.Tokens) {
	src := w.m.roots[env].backend
	if len(src) == 0 {
		return "", nil, nil
	}
	f, err := hclfile.Parse(hclfile.BackendFileName, src)
	if err != nil {
		return "", nil, nil
	}
	blocks := f.Edit().Body().Blocks()
	for i, b := range f.Syntax.Blocks {
		if b.Type != "terraform" || i >= len(blocks) {
			continue
		}
		inner := blocks[i].Body().Blocks()
		for j, backend := range b.Body.Blocks {
			if backend.Type != "backend" || len(backend.Labels) != 1 || j >= len(inner) {
				continue
			}
			names := slices.Collect(maps.Keys(backend.Body.Attributes))
			sort.Slice(names, func(x, y int) bool {
				return backend.Body.Attributes[names[x]].SrcRange.Start.Byte < backend.Body.Attributes[names[y]].SrcRange.Start.Byte
			})
			values := map[string]hclwrite.Tokens{}
			for _, name := range names {
				values[name] = inner[j].Body().GetAttribute(name).Expr().BuildTokens(nil)
			}
			return backend.Labels[0], names, values
		}
	}
	return "", nil, nil
}

// backendType is the type of the roots' backend, such as s3, or "" where
// no root has one.
func (w *rootWriter) backendType() string {
	for e := range w.m.roots {
		if typ, _, _ := w.rootBackend(e); typ != "" {
			return typ
		}
	}
	return ""
}

// backendSettings are the settings the roots' backends of type typ set,
// as the first root that has one sets them, for the backend.hcl of an
// environment whose root had none.
func (w *rootWriter) backendSettings(typ string) []string {
	for e := range w.m.roots {
		if t, names, _ := w.rootBackend(e); t == typ {
			return names
		}
	}
	return nil
}

// backendConfig writes environment env's backend.hcl: its root's backend
// settings; for a root without a backend where others have one, the
// settings to fill in, commented out; nil where no root has one.
func (w *rootWriter) backendConfig(env int) []byte {
	want := w.backendType()
	if want == "" {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# The %s backend's settings for the %s environment: terraform init\n# -backend-config=%s.\n", want, w.m.envs[env], hclfile.BackendConfigPath(w.m.envs[env]))
	typ, names, values := w.rootBackend(env)
	if typ != want {
		fmt.Fprintf(&b, "# The %s root had no %s backend: set these.\n", w.m.envs[env], want)
		for _, name := range w.backendSettings(want) {
			fmt.Fprintf(&b, "# %s = \"\"\n", name)
		}
		return []byte(b.String())
	}
	for _, name := range names {
		fmt.Fprintf(&b, "%s = %s\n", name, exprText(values[name]))
	}
	return formatHCL(b.String())
}

// variables returns the variables of the merged root, in the order
// variables.tf has them: env, the region, then the others by name.
func (w *rootWriter) variables() []*envVariable {
	var env *envVariable
	var rest []*envVariable
	for _, v := range append(slices.Clone(w.m.variables), w.extra...) {
		if v.name == envVariableName {
			if !w.noEnv {
				env = v
			}
			continue
		}
		rest = append(rest, v)
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].name < rest[j].name })
	var out []*envVariable
	if env != nil {
		out = append(out, env)
	}
	if w.region != nil {
		out = append(out, w.region)
	}
	return append(out, rest...)
}

// describe is the description of variable v.
func describe(v *envVariable) string {
	if v.description != "" {
		return v.description
	}
	if rest, ok := strings.CutPrefix(v.what, "whether "); ok {
		return "Whether " + rest + "."
	}
	return "The " + v.what + "."
}

// variablesFile writes variables.tf: env first, with a validation of the
// environments' tokens; the region; each variable the merged code reads,
// typed and described, with its default, if any, and sensitive where it
// holds a secret, null by default only where an environment doesn't set
// it; the variables the roots declared that the merged code reads as they
// are; and import_ids, which imports.tf reads.
func (w *rootWriter) variablesFile() ([]byte, error) {
	f := hclwrite.NewEmptyFile()
	body := f.Body()
	for i, v := range w.variables() {
		if i > 0 {
			body.AppendNewline()
		}
		block := body.AppendNewBlock("variable", []string{v.name}).Body()
		block.SetAttributeValue("description", cty.StringVal(describe(v)))
		ty, err := hclfile.OptionalTypeTokens(v.ty)
		if err != nil {
			return nil, fmt.Errorf("variable %s: %w", v.name, err)
		}
		block.SetAttributeRaw("type", ty)
		if v.hasDefault {
			value := v.defaultValue
			if value == nil {
				value = hclwrite.TokensForValue(cty.NullVal(cty.DynamicPseudoType))
			}
			block.SetAttributeRaw("default", value)
		}
		if v.sensitive {
			block.SetAttributeValue("sensitive", cty.True)
		}
		if v.name == envVariableName {
			tokens := make([]string, 0, len(w.m.norms))
			for _, n := range w.m.norms {
				if !slices.Contains(tokens, n.token) {
					tokens = append(tokens, n.token)
				}
			}
			values := make([]cty.Value, len(tokens))
			for j, t := range tokens {
				values[j] = cty.StringVal(t)
			}
			block.AppendNewline()
			validation := block.AppendNewBlock("validation", nil).Body()
			validation.SetAttributeRaw("condition", hclfile.ParseExpr(fmt.Sprintf("contains(%s, var.%s)", string(hclwrite.TokensForValue(cty.TupleVal(values)).Bytes()), envVariableName)))
			validation.SetAttributeValue("error_message", cty.StringVal(fmt.Sprintf("%s must be one of %s.", envVariableName, strings.Join(tokens, ", "))))
		}
	}
	for _, b := range w.copied {
		body.AppendNewline()
		body.AppendUnstructuredTokens(b.BuildTokens(nil))
	}
	body.AppendNewline()
	imports := body.AppendNewBlock("variable", []string{hclfile.ImportIDsVariableName}).Body()
	imports.SetAttributeValue("description", cty.StringVal("The import ID of each resource the environment imports, by its address: "+hclfile.ImportVarsPath("<env>")+" sets it for imports.tf."))
	imports.SetAttributeRaw("type", hclfile.ParseExpr("map(string)"))
	imports.SetAttributeRaw("default", hclwrite.TokensForObject(nil))
	if w.m.importsByIdentity() {
		body.AppendNewline()
		identities := body.AppendNewBlock("variable", []string{hclfile.ImportIdentitiesVariableName}).Body()
		identities.SetAttributeValue("description", cty.StringVal("The identity of each resource the environment imports by identity rather than by import ID, by its address: "+hclfile.ImportVarsPath("<env>")+" sets it for imports.tf."))
		identities.SetAttributeRaw("type", hclfile.ParseExpr("map(map(string))"))
		identities.SetAttributeRaw("default", hclwrite.TokensForObject(nil))
	}
	return formatHCL(string(f.Bytes())), nil
}

// tfvarsFile writes environment env's terraform.tfvars: env first, then
// the values its environment sets, where they differ from the defaults,
// under a comment naming the file of what they are for, with each
// feature's deploy_ variable first.
func (w *rootWriter) tfvarsFile(env int) []byte {
	sections := map[string][]*envVariable{}
	for _, v := range w.variables() {
		if v.name == envVariableName {
			continue
		}
		if _, ok := v.tfvars(env); ok {
			sections[w.sectionOf[v]] = append(sections[w.sectionOf[v]], v)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# The %s environment's values that differ from the defaults in\n# variables.tf. Secrets are never set here: see README.md.\n", w.m.envs[env])
	if !w.noEnv {
		fmt.Fprintf(&b, "%s = %s\n", envVariableName, string(hclwrite.TokensForValue(cty.StringVal(w.m.norms[env].token)).Bytes()))
	}
	order := slices.Sorted(maps.Keys(sections))
	for _, section := range order {
		vars := sections[section]
		sort.SliceStable(vars, func(i, j int) bool {
			di, dj := isFeatureFlag(vars[i].name), isFeatureFlag(vars[j].name)
			if di != dj {
				return di
			}
			return vars[i].name < vars[j].name
		})
		b.WriteString("\n")
		if section != "" {
			fmt.Fprintf(&b, "# %s\n", strings.TrimSuffix(section, ".tf"))
		}
		for _, v := range vars {
			value, _ := v.tfvars(env)
			fmt.Fprintf(&b, "%s = %s\n", v.name, listText(value))
		}
	}
	return formatHCL(b.String())
}

// listText is tokens, a value, as text, with a list of objects written one
// object after another rather than run together.
func listText(tokens hclwrite.Tokens) string {
	src := bytes.TrimSpace(tokens.Bytes())
	expr, diags := hclsyntax.ParseExpression(src, "", hcl.InitialPos)
	tuple, ok := expr.(*hclsyntax.TupleConsExpr)
	if diags.HasErrors() || !ok || len(tuple.Exprs) == 0 {
		return string(src)
	}
	var b strings.Builder
	b.WriteString("[\n")
	for _, e := range tuple.Exprs {
		if _, ok := e.(*hclsyntax.ObjectConsExpr); !ok {
			return string(src)
		}
		b.Write(e.Range().SliceBytes(src))
		b.WriteString(",\n")
	}
	b.WriteString("]")
	return b.String()
}

// importVarsFile writes environment env's imports.tfvars: the import ID
// of each of its resources, by its address in the merged root, and,
// where the merged root imports any by identity, the identity of each
// it imports so instead.
func (w *rootWriter) importVarsFile(env int) []byte {
	ids, identities := w.m.imports(env), w.m.identities(env)
	attrs := make([]hclwrite.ObjectAttrTokens, 0, len(ids))
	for _, address := range slices.Sorted(maps.Keys(ids)) {
		if identities[address] != nil {
			continue
		}
		attrs = append(attrs, hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForValue(cty.StringVal(address)), Value: hclwrite.TokensForValue(cty.StringVal(ids[address]))})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# The import ID of each resource of the %s environment, for imports.tf on\n# its first apply. Delete this file with imports.tf after it.\n", w.m.envs[env])
	fmt.Fprintf(&b, "%s = %s\n", hclfile.ImportIDsVariableName, string(hclwrite.TokensForObject(attrs).Bytes()))
	if w.m.importsByIdentity() {
		attrs := make([]hclwrite.ObjectAttrTokens, 0, len(identities))
		for _, address := range slices.Sorted(maps.Keys(identities)) {
			attrs = append(attrs, hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForValue(cty.StringVal(address)), Value: hclwrite.TokensForValue(rootfile.IdentityValue(identities[address]))})
		}
		fmt.Fprintf(&b, "%s = %s\n", hclfile.ImportIdentitiesVariableName, string(hclwrite.TokensForObject(attrs).Bytes()))
	}
	return formatHCL(b.String())
}

// formatHCL is src, HCL, formatted as terraform fmt does, with the blocks
// and objects that hold nothing on one line.
func formatHCL(src string) []byte {
	return hclfile.CollapseEmptyBlocks(hclwrite.Format([]byte(src)))
}

// readme writes the merged root's README.md: its environments, and how to
// plan and apply each.
func (w *rootWriter) readme() []byte {
	var b strings.Builder
	b.WriteString("# " + w.title() + "\n\n")
	fmt.Fprintf(&b, "Generated by infraharvest: one root whose code every environment shares (%s). What differs between them is in `%s/<env>/`: `%s` holds the values that differ from the defaults in `variables.tf`, `%s` the IDs `imports.tf` imports, and `%s` where the state is kept.\n\n", strings.Join(w.m.envs, ", "), hclfile.VarsDir, hclfile.EnvVarsFileName, hclfile.ImportVarsFileName, hclfile.BackendConfigFileName)
	regions := slices.ContainsFunc(w.m.roots, func(r envRoot) bool { return r.region != "" })
	header := "| Environment | `env` | " + w.m.profile.ScopeTitle + " |"
	rule := "|---|---|---|"
	if regions {
		header += " Region |"
		rule += "---|"
	}
	b.WriteString(header + "\n" + rule + "\n")
	for e, root := range w.m.roots {
		fmt.Fprintf(&b, "| %s | `%s` | %s |", w.m.envs[e], w.m.norms[e].token, root.scope)
		if regions {
			fmt.Fprintf(&b, " %s |", root.region)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n## Plan and apply an environment\n\n")
	env := w.m.envs[0]
	fmt.Fprintf(&b, "Run each environment with its own credentials: the provider block sets no role of its own. For %s, and likewise for the others:\n\n```sh\n", env)
	if w.backendType() != "" {
		fmt.Fprintf(&b, "terraform init -reconfigure -backend-config=%s\n", hclfile.BackendConfigPath(env))
	} else {
		fmt.Fprintf(&b, "terraform init\nterraform workspace select -or-create %s\n", env)
	}
	varFiles := varFileArgs(env, true)
	fmt.Fprintf(&b, "terraform plan %s\nterraform apply %s\n```\n\n", varFiles, varFiles)
	if w.backendType() != "" {
		b.WriteString("`-reconfigure` switches the working directory to the environment's state.")
	} else {
		fmt.Fprintf(&b, "Without a backend, each environment keeps its state in the workspace named after it, in `%s/<env>/`, which `infraharvest drift` reads.", tfrun.WorkspaceStateDir)
	}
	b.WriteString(" Plan each environment before its first apply: it should import every resource and change nothing else.\n\n")
	fmt.Fprintf(&b, "## After the first apply\n\nOnce every environment has been applied, delete `%s` and each `%s`, and leave `-var-file=%s` out of the commands.\n", rootfile.ImportsFileName, hclfile.ImportVarsPath("<env>"), hclfile.ImportVarsPath("<env>"))
	b.WriteString(w.arrivals())
	var secrets []string
	for _, v := range w.variables() {
		if v.sensitive {
			secrets = append(secrets, "`"+v.name+"`")
		}
	}
	for _, blk := range w.copied {
		if attr := blk.Body().GetAttribute("sensitive"); attr != nil && strings.TrimSpace(string(attr.Expr().BuildTokens(nil).Bytes())) == "true" {
			secrets = append(secrets, "`"+blk.Labels()[0]+"`")
		}
	}
	if len(secrets) > 0 {
		sort.Strings(secrets)
		fmt.Fprintf(&b, "\n## Secrets\n\nSet %s for each environment from your secret store, or with `TF_VAR_<name>`, never in a var file. infraharvest never writes secret values into the configuration.\n", strings.Join(secrets, ", "))
	}
	return []byte(b.String())
}

// title is the README's title: the stack's provider and region.
func (w *rootWriter) title() string {
	parts := []string{w.m.provider}
	if r := w.m.roots[0].region; r != "" {
		parts = append(parts, r)
	}
	return path.Join(parts...)
}

// arrivals lists what each environment's part of the merged root imports
// that other roots of the output hand over (see MergeRoot.Arrivals), at
// its address in the merged root, for the order to apply them in. It is
// empty if there is none.
func (w *rootWriter) arrivals() string {
	var rows strings.Builder
	for e, root := range w.m.roots {
		ids := w.m.imports(e)
		for _, a := range root.arrivals {
			at := a.To
			for _, address := range slices.Sorted(maps.Keys(ids)) {
				if ids[address] == a.ID && (strings.HasPrefix(address, a.Type+".") || strings.Contains(address, "."+a.Type+".")) {
					at = address
					break
				}
			}
			fmt.Fprintf(&rows, "| %s | `%s` | `%s` | `%s` |\n", w.m.envs[e], at, a.Root, a.From)
		}
	}
	if rows.Len() == 0 {
		return ""
	}
	return "\n## Moves\n\nThese resources come from other roots of the output, whose removed blocks make their state forget them. Apply the root each comes from first, then this root as the environment, which imports it: until then, both would manage it.\n\n| Environment | Imported | Comes from | Was |\n|---|---|---|---|\n" + rows.String()
}
