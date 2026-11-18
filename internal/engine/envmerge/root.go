// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
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

// buildMergedRoot returns the files of m's merged root, the one root
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
// It also returns the writer that wrote the files, which knows their
// variables.
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
			// Its source holds the stack's values: the reason, which the
			// report shows, says only where it fails.
			return nil, fmt.Errorf("%s doesn't parse: %s", name, firstParseError(name, src))
		}
		files[name] = out
	}
	return files, nil
}

// resourceText writes a resource block of type typ and label, with meta,
// its meta-arguments, and body.
//
// The merged root is rendered as text and parsed back, unlike the rest of
// the engine, which edits an hclwrite tree. It works and is tested, so it
// stays. New rendering code, here and elsewhere, uses hclwrite.
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
