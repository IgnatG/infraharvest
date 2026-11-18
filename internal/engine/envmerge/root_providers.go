// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

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
// roots' and raised to what the merged files use (see
// rootfile.FeaturesNeed), the provider's requirement, and the backend, its
// settings in each environment's backend.hcl, where the roots have one.
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
