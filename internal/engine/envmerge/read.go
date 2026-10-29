// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
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
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// MergeRoot is one environment's finished root of a stack, which
// MergeRoots merges with the other environments' into one root.
type MergeRoot struct {
	// Env is the environment's name, and Tokens the words that stand for
	// it inside names, such as prd for prod; by default its name.
	Env    string
	Tokens []string
	// Scope is the root's account, subscription or project ID, Region its
	// AWS region, and Group its Azure resource group, if any.
	Scope, Region, Group string
	// Dir holds the root's files, and Origin is the directory they were
	// generated in, which the sources of its local module calls are
	// relative to: Dir itself, or where the root was before its files
	// were set aside for merging (see Dir).
	Dir, Origin string
	// Provider is the root's provider, with its configuration without
	// credentials and its version constraint.
	Provider rootfile.Provider
	// Secrets are the root's secret variables, whose arguments its plan
	// gives placeholder values (see placeholders).
	Secrets []secret.Variable
	// Terraform returns a Terraform runner in a directory with the
	// environment's credentials, which plans the merged root as the
	// environment; nil for an environment the run didn't import, whose
	// root is as an earlier import generated it: the merged root isn't
	// planned as it (see MergedEnvironment.Unplanned). One environment at
	// least must have it.
	Terraform func(dir string) (tfrun.Terraform, error)
	// Isolated are the addresses, in the root, of the resources an
	// incremental import added since the stack was merged: each stays a
	// resource of its environment, matched with no other environment's,
	// so that what the merged root already has keeps its addresses.
	Isolated []string
	// Labels are, by the address of a block of Isolated, the label it has
	// in the merged root, where an earlier merge gave it one: kept apart
	// again, it keeps it, rather than one from its label in the root.
	Labels map[string]string
	// Gone are the addresses, in the root, of the resources an incremental
	// import found no longer in the cloud: the merged root keeps them as
	// the root has them, but the environment's plan can't import them, so
	// its gate plans without their import blocks, and lets the plan create
	// them again, as the root's next plan would. A resource of a for_each
	// the merged root makes, whose import block imports every key, is
	// imported all the same.
	Gone []string
	// Arrivals are the resources the root imports that other roots hand
	// over (see engine.Result.Arrived), which the merged root's README
	// names, as the roots they come from are applied first.
	Arrivals []rootfile.Arrival
	// Data has, by data source type, the values of the attributes, by
	// name, of the data sources that read the environment's account and
	// region, as its credentials give them (see UnmergeEnv.Data): the
	// locals the root reads them through, as engine.Generate writes them
	// (see engine.ScopeLocals), are put in as values, as literal locals
	// are. MergeRoots fills it from MergeProfile.EnvironmentData where it
	// is nil.
	Data map[string]map[string]string
}

// readEnvRoot reads the root r as merging reads it (see envRoot): its
// resource blocks and module calls, with the locals they read put in
// where the locals are values; its data sources; the IDs its import
// blocks import, by address; the file each block is in; its
// required_version, its provider's default tags, its backend and its
// variables. Local module sources are made relative to target, the
// merged root's directory. It returns why the root can't be merged, if
// it can't: it has moved or removed blocks, or outputs, which the merged
// root doesn't carry.
func readEnvRoot(r MergeRoot, target string) (envRoot, string, error) {
	root := envRoot{env: r.Env, tokens: r.Tokens, scope: r.Scope, region: r.Region, group: r.Group, provider: r.Provider, importIDs: map[string]string{}, identities: map[string]map[string]string{}, files: map[string]string{}, arrivals: r.Arrivals}
	files, err := hclfile.ConfigFiles(r.Dir)
	if err != nil {
		return root, "", err
	}
	locals := map[string]hclsyntax.Expression{}
	var blocks strings.Builder
	type dataBlock struct {
		block *hclsyntax.Block
		text  string
	}
	var dataBlocks []dataBlock
	var providerBlock *hclsyntax.Block
	for _, f := range files {
		name := filepath.Base(f.Path)
		for _, b := range f.Syntax.Blocks {
			text := string(b.Range().SliceBytes(f.Src)) + "\n\n"
			switch b.Type {
			case "resource", "module":
				address := strings.Join(append([]string{b.Type}, b.Labels...), ".")
				if b.Type == "resource" && len(b.Labels) == 2 {
					address = b.Labels[0] + "." + b.Labels[1]
				}
				root.files[address] = name
				blocks.WriteString(text)
			case "data":
				dataBlocks = append(dataBlocks, dataBlock{b, text})
			case "locals":
				for n, attr := range b.Body.Attributes {
					locals[n] = attr.Expr
				}
			case "import":
				address, id, identity, ok := importOf(b)
				if !ok {
					return root, fmt.Sprintf("%s: an import block of %s has no literal ID or identity", r.Env, name), nil
				}
				root.importIDs[address] = id
				if identity != nil {
					root.identities[address] = identity
				}
			case "terraform":
				if attr, ok := b.Body.Attributes["required_version"]; ok {
					if v, diags := attr.Expr.Value(nil); !diags.HasErrors() && v.Type() == cty.String && v.IsKnown() && !v.IsNull() {
						root.requiredVersion = v.AsString()
					}
				}
			case "provider":
				if len(b.Labels) == 1 && b.Labels[0] == r.Provider.Name {
					if _, alias := b.Body.Attributes["alias"]; !alias {
						providerBlock = b
					}
				}
			case "moved", "removed":
				return root, fmt.Sprintf("%s: the root moves or removes resources (%s), which a merged root doesn't carry", r.Env, rootfile.MovesFileName), nil
			case "output":
				return root, fmt.Sprintf("%s: the root has outputs (%s), which a merged root doesn't carry", r.Env, name), nil
			}
		}
	}
	syntax := make([]*hclsyntax.Block, len(dataBlocks))
	for i, d := range dataBlocks {
		syntax[i] = d.block
	}
	envData := unmergeData(syntax, UnmergeEnv{Name: r.Env, Data: r.Data})
	values := localValues(locals, envData)
	if root.file, err = inlinedFile(rootfile.GeneratedFileName, blocks.String(), values, r, target); err != nil {
		return root, "", err
	}
	// The data sources the environment's own values give are left out
	// where nothing reads them once their locals are put in: the merged
	// root reads its environments' account and region as it does (see
	// stackMerge.render).
	read := map[string]bool{}
	for _, t := range hclfile.Traversals(root.file.Syntax) {
		read[dataAddress(t)] = true
	}
	for _, d := range dataBlocks {
		for _, t := range hclfile.Traversals(d.block.Body) {
			read[dataAddress(t)] = true
		}
	}
	var data strings.Builder
	for _, d := range dataBlocks {
		if len(d.block.Labels) == 2 && givesData(envData, d.block.Labels[0], d.block.Labels[1]) && !read[d.block.Labels[0]+"."+d.block.Labels[1]] {
			continue
		}
		data.WriteString(d.text)
	}
	if data.Len() > 0 {
		if root.data, err = inlinedFile(rootfile.DataFileName, data.String(), values, r, target); err != nil {
			return root, "", err
		}
	}
	if providerBlock != nil {
		root.defaultTags, root.defaultLabels = defaultTagsIn(providerBlock, values)
	}
	if root.backend, err = os.ReadFile(filepath.Join(r.Dir, hclfile.BackendFileName)); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return root, "", err
		}
		root.backend = nil
	}
	if root.variables, err = hclfile.LoadOptional(filepath.Join(r.Dir, hclfile.VariablesFileName)); err != nil {
		return root, "", err
	}
	return root, "", nil
}

// importOf returns the address an import block imports into, as
// addressText writes it, and its ID, where the ID is a literal, or, for
// a block that imports by identity, the identity and its ID (see
// importIDOf).
func importOf(b *hclsyntax.Block) (address, id string, identity map[string]string, ok bool) {
	to, ok := b.Body.Attributes["to"]
	if !ok {
		return "", "", nil, false
	}
	traversal, diags := hcl.AbsTraversalForExpr(to.Expr)
	if diags.HasErrors() {
		return "", "", nil, false
	}
	if id, ok = hclfile.ImportIDOf(b.Body); !ok {
		return "", "", nil, false
	}
	if _, hasID := b.Body.Attributes["id"]; !hasID {
		identity, _ = hclfile.IdentityOf(b.Body)
	}
	return addressText(traversal), id, identity, true
}

// addressText is an address as Terraform writes it in a plan, such as
// aws_sqs_queue.this["orders"] or module.logs.aws_s3_bucket.this[0].
func addressText(t hcl.Traversal) string {
	return strings.TrimSpace(string(hclwrite.TokensForTraversal(t).Bytes()))
}

// dataAddress returns the address, type.name, of the data source t
// refers to, or "" where it refers to none.
func dataAddress(t hcl.Traversal) string {
	if t.RootName() != "data" || len(t) < 3 {
		return ""
	}
	typ, ok := t[1].(hcl.TraverseAttr)
	name, ok2 := t[2].(hcl.TraverseAttr)
	if !ok || !ok2 {
		return ""
	}
	return typ.Name + "." + name.Name
}

// givesData reports whether data, as unmergeData returns it, has the data
// source typ.name.
func givesData(data cty.Value, typ, name string) bool {
	if !data.Type().IsObjectType() || !data.Type().HasAttribute(typ) {
		return false
	}
	byName := data.GetAttr(typ)
	return byName.Type().IsObjectType() && byName.Type().HasAttribute(name)
}

// localValues returns the values of locals that are values: literals,
// or expressions of other such locals only, and of the data sources data
// gives (see unmergeData), such as the environment's account ID.
func localValues(locals map[string]hclsyntax.Expression, data cty.Value) map[string]cty.Value {
	values := map[string]cty.Value{}
	for changed := true; changed; {
		changed = false
		for _, name := range slices.Sorted(maps.Keys(locals)) {
			if _, done := values[name]; done {
				continue
			}
			expr := locals[name]
			known := true
			for _, t := range expr.Variables() {
				if address := dataAddress(t); address != "" {
					typ, dataName, _ := strings.Cut(address, ".")
					if !givesData(data, typ, dataName) {
						known = false
						break
					}
					continue
				}
				if t.RootName() != "local" || len(t) < 2 {
					known = false
					break
				}
				attr, ok := t[1].(hcl.TraverseAttr)
				if !ok {
					known = false
					break
				}
				if _, ok := values[attr.Name]; !ok {
					known = false
					break
				}
			}
			if !known {
				continue
			}
			ctx := &hcl.EvalContext{Variables: map[string]cty.Value{"local": cty.ObjectVal(values), "data": data}}
			v, diags := expr.Value(ctx)
			if diags.HasErrors() || !v.IsWhollyKnown() {
				continue
			}
			values[name] = v
			changed = true
		}
	}
	return values
}

// inlinedFile parses src, blocks of root r, with each reference to a
// local of values made its value, a merge of objects that leaves made
// the object it gives (see mergedObject), and the sources of local
// module calls made relative to target.
func inlinedFile(name, src string, values map[string]cty.Value, r MergeRoot, target string) (*hclfile.File, error) {
	parsed, diags := hclfile.ParseDiagnostics(name, []byte(src))
	if diags.HasErrors() {
		return nil, fmt.Errorf("%s: %s: %w", r.Dir, name, diags)
	}
	f := parsed.Edit()
	var inline func(body *hclwrite.Body)
	inline = func(body *hclwrite.Body) {
		for attrName, attr := range body.Attributes() {
			tokens := attr.Expr().BuildTokens(nil)
			replaced := replaceLocals(tokens, values)
			if folded, ok := mergedObject(replaced); ok {
				replaced = folded
			}
			if exprKey(replaced) != exprKey(tokens) {
				body.SetAttributeRaw(attrName, replaced)
			}
		}
		for _, b := range body.Blocks() {
			inline(b.Body())
		}
	}
	for _, b := range f.Body().Blocks() {
		inline(b.Body())
		if b.Type() != "module" {
			continue
		}
		source := b.Body().GetAttribute("source")
		if source == nil {
			continue
		}
		v, ok := literalValue(source.Expr().BuildTokens(nil))
		if !ok || v.Type() != cty.String || !isLocalSource(v.AsString()) {
			continue
		}
		origin := r.Origin
		if origin == "" {
			origin = r.Dir
		}
		abs := filepath.Join(origin, filepath.FromSlash(v.AsString()))
		rel, err := filepath.Rel(target, abs)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, "../") {
			rel = "./" + rel
		}
		b.Body().SetAttributeValue("source", cty.StringVal(rel))
	}
	return hclfile.Parse(name, hclwrite.Format(f.Bytes()))
}

// isLocalSource reports whether a module source is a directory's.
func isLocalSource(source string) bool {
	return strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../")
}

// replaceLocals returns tokens with each reference to a local of values,
// local.<name>, made the local's value. Inside a template, a string's
// value joins the template's text, so that
// "arn:aws:sns:${local.aws_region}:${local.aws_account_id}:alerts", as
// engine.Generate writes a root's own account and region (see
// engine.ScopeLocals), reads as the literal it stands for.
func replaceLocals(tokens hclwrite.Tokens, values map[string]cty.Value) hclwrite.Tokens {
	if len(values) == 0 {
		return tokens
	}
	out := make(hclwrite.Tokens, 0, len(tokens))
	// joined is the text token of out the template's text joins, if the
	// last of out is one replaceLocals made.
	var joined *hclwrite.Token
	heredoc := false
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		switch t.Type {
		case hclsyntax.TokenOHeredoc:
			heredoc = true
		case hclsyntax.TokenCHeredoc:
			heredoc = false
		}
		if s, ok := interpolatedLocal(tokens, i, values); ok {
			text := hclsyntax.TokenQuotedLit
			if heredoc {
				text = hclsyntax.TokenStringLit
			}
			switch last := len(out) - 1; {
			case joined != nil:
				joined.Bytes = append(joined.Bytes, s...)
			case last >= 0 && out[last].Type == text:
				c := *out[last]
				c.Bytes = append(slices.Clone(c.Bytes), s...)
				out[last] = &c
				joined = &c
			default:
				joined = &hclwrite.Token{Type: text, Bytes: []byte(s)}
				out = append(out, joined)
			}
			i += 4
			continue
		}
		if joined != nil && t.Type == joined.Type {
			joined.Bytes = append(joined.Bytes, t.Bytes...)
			continue
		}
		joined = nil
		if t.Type == hclsyntax.TokenIdent && string(t.Bytes) == "local" && i+2 < len(tokens) && tokens[i+1].Type == hclsyntax.TokenDot && tokens[i+2].Type == hclsyntax.TokenIdent && (i == 0 || tokens[i-1].Type != hclsyntax.TokenDot) {
			if v, ok := values[string(tokens[i+2].Bytes)]; ok {
				for j, r := range hclwrite.TokensForValue(v) {
					c := *r
					if j == 0 {
						c.SpacesBefore = t.SpacesBefore
					}
					out = append(out, &c)
				}
				i += 2
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

// interpolatedLocal returns the value of the local of values that tokens
// interpolate at i, ${local.<name>}, where it is a string a template's
// text can hold as it is: with no quote, backslash, line break or
// template sequence.
func interpolatedLocal(tokens hclwrite.Tokens, i int, values map[string]cty.Value) (string, bool) {
	if i+4 >= len(tokens) || tokens[i].Type != hclsyntax.TokenTemplateInterp || tokens[i+1].Type != hclsyntax.TokenIdent || string(tokens[i+1].Bytes) != "local" ||
		tokens[i+2].Type != hclsyntax.TokenDot || tokens[i+3].Type != hclsyntax.TokenIdent || tokens[i+4].Type != hclsyntax.TokenTemplateSeqEnd {
		return "", false
	}
	v, ok := values[string(tokens[i+3].Bytes)]
	if !ok || v.IsNull() || !v.IsKnown() || !v.Type().Equals(cty.String) {
		return "", false
	}
	s := v.AsString()
	if strings.ContainsAny(s, "\"\\\r\n") || strings.Contains(s, "${") || strings.Contains(s, "%{") {
		return "", false
	}
	return s, true
}

// mergedObject returns tokens, an expression, as the object it gives where
// it is a merge of object literals only, such as merge({ env = "dev" }, {
// owner = "alice" }) once the local of a tag lift is put in (see the
// engine's referTags): false otherwise. A root lifts the tags its
// resources share only where two or more have them, so the same resource
// reads alike in a root that lifted its tags and one that didn't, as when
// an environment gains a resource the others lack; else its tags couldn't
// merge, and it would be each environment's.
func mergedObject(tokens hclwrite.Tokens) (hclwrite.Tokens, bool) {
	expr, diags := hclsyntax.ParseExpression(tokens.Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, false
	}
	call, ok := expr.(*hclsyntax.FunctionCallExpr)
	if !ok || call.Name != "merge" || call.ExpandFinal || len(call.Args) == 0 {
		return nil, false
	}
	attrs := map[string]cty.Value{}
	for _, arg := range call.Args {
		if _, ok := arg.(*hclsyntax.ObjectConsExpr); !ok || len(arg.Variables()) > 0 {
			return nil, false
		}
		v, diags := arg.Value(nil)
		if diags.HasErrors() || !v.IsWhollyKnown() || v.IsNull() || !v.Type().IsObjectType() {
			return nil, false
		}
		for it := v.ElementIterator(); it.Next(); {
			k, value := it.Element()
			attrs[k.AsString()] = value
		}
	}
	return hclwrite.TokensForValue(cty.ObjectVal(attrs)), true
}

// defaultTagsIn returns the tags a provider block applies to every
// resource: those of its default_tags block (AWS) or of its
// default_labels argument (Google), where they are values once the
// locals of values are put in; nil for none. labels says they are the
// argument's.
func defaultTagsIn(b *hclsyntax.Block, values map[string]cty.Value) (tags map[string]string, labels bool) {
	var expr hclsyntax.Expression
	if attr, ok := b.Body.Attributes["default_labels"]; ok {
		expr, labels = attr.Expr, true
	}
	for _, inner := range b.Body.Blocks {
		if attr, ok := inner.Body.Attributes["tags"]; ok && inner.Type == "default_tags" {
			expr, labels = attr.Expr, false
		}
	}
	if expr == nil {
		return nil, false
	}
	v, diags := expr.Value(&hcl.EvalContext{Variables: map[string]cty.Value{"local": cty.ObjectVal(values)}})
	if diags.HasErrors() || !v.IsWhollyKnown() || v.IsNull() || (!v.Type().IsMapType() && !v.Type().IsObjectType()) {
		return nil, false
	}
	tags = map[string]string{}
	for it := v.ElementIterator(); it.Next(); {
		k, value := it.Element()
		if value.Type() != cty.String || value.IsNull() {
			return nil, false
		}
		tags[k.AsString()] = value.AsString()
	}
	return tags, labels
}

// stackBlocks lists the resource blocks and module calls of f, as merging
// matches them: a module call's address is module.<name>.
func stackBlocks(f *hclfile.File) []hclfile.Resource {
	var out []hclfile.Resource
	blocks := f.Edit().Body().Blocks()
	for i, b := range f.Syntax.Blocks {
		if i >= len(blocks) {
			break
		}
		switch {
		case b.Type == "resource" && len(b.Labels) == 2:
			out = append(out, hclfile.Resource{Address: b.Labels[0] + "." + b.Labels[1], Write: blocks[i], Syntax: b})
		case b.Type == "module" && len(b.Labels) == 1:
			out = append(out, hclfile.Resource{Address: "module." + b.Labels[0], Write: blocks[i], Syntax: b})
		}
	}
	return out
}

// dataBlocksOf lists the data blocks of f, each with its address,
// type.name.
func dataBlocksOf(f *hclfile.File) []hclfile.Resource {
	if f == nil {
		return nil
	}
	var out []hclfile.Resource
	blocks := f.Edit().Body().Blocks()
	for i, b := range f.Syntax.Blocks {
		if i < len(blocks) && b.Type == "data" && len(b.Labels) == 2 {
			out = append(out, hclfile.Resource{Address: b.Labels[0] + "." + b.Labels[1], Write: blocks[i], Syntax: b})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}
