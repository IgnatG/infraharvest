// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

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
