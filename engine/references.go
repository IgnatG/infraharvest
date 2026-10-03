// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// referenceTarget is a resource attribute a literal can refer to instead.
type referenceTarget struct {
	address   string
	attribute string
}

func (t referenceTarget) traversal() hcl.Traversal {
	typ, name, _ := strings.Cut(t.address, ".")
	return hcl.Traversal{hcl.TraverseRoot{Name: typ}, hcl.TraverseAttr{Name: name}, hcl.TraverseAttr{Name: t.attribute}}
}

// addReferences replaces literals in generated.tf that are another
// resource's id or ARN, by its imported values, with a reference to that
// attribute: vpc_id = aws_vpc.main.id. A value counts if it identifies a
// resource on its own (see referenceable), or if the argument is named
// after the resource's type, like an S3 bucket name in bucket = "...".
// It doesn't make a resource refer to itself or two resources refer to
// each other, directly or not. It reports whether it changed anything.
func addReferences(dir string, values map[string]map[string]any) (bool, error) {
	index := referenceIndex(values)
	if len(index) == 0 {
		return false, nil
	}
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return false, err
	}
	g := dependencies{}
	changed := false
	for _, r := range generated.resources() {
		if referenceAttributes(r.address, r.syntax.Body, r.write.Body(), index, g) {
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	return true, generated.save()
}

// referenceIndex maps each id and ARN value to the attribute to refer to,
// leaving out values several resources share unless one of them is the
// others' parent (an S3 bucket, whose versioning and policy resources have
// the bucket's name as their id too).
func referenceIndex(values map[string]map[string]any) map[string]referenceTarget {
	owners := map[string][]string{}
	for address, attrs := range values {
		for _, key := range []string{"id", "arn"} {
			if v, ok := attrs[key].(string); ok && v != "" && !slices.Contains(owners[v], address) {
				owners[v] = append(owners[v], address)
			}
		}
	}
	index := map[string]referenceTarget{}
	for value, addresses := range owners {
		address := parentOf(addresses)
		if address == "" {
			continue
		}
		attrs := values[address]
		attribute := "id"
		switch {
		case attrs["arn"] == value:
			attribute = "arn"
		case attrs["name"] == value:
			attribute = "name"
		case attrs["bucket"] == value:
			attribute = "bucket"
		}
		index[value] = referenceTarget{address: address, attribute: attribute}
	}
	return index
}

// parentOf returns the one address, or the address whose type starts every
// other address's type; "" if there is none.
func parentOf(addresses []string) string {
	if len(addresses) == 1 {
		return addresses[0]
	}
	sort.Strings(addresses)
	for _, candidate := range addresses {
		parentType := resourceTypeOf(candidate)
		isParent := true
		for _, other := range addresses {
			if other != candidate && !strings.HasPrefix(resourceTypeOf(other), parentType+"_") {
				isParent = false
				break
			}
		}
		if isParent {
			return candidate
		}
	}
	return ""
}

func resourceTypeOf(address string) string {
	typ, _, _ := strings.Cut(address, ".")
	return typ
}

// referenceable reports whether a value identifies a resource on its own:
// an ARN, a URL (an SQS queue's id) or a cloud ID such as vpc-0abc1234,
// not a name another resource could share by chance.
func referenceable(value string) bool {
	return liftable.MatchString(value) || strings.HasPrefix(value, "https://")
}

// namedAfter reports whether argument is named after the last word of the
// referenced resource's type: bucket for aws_s3_bucket, role for
// aws_iam_role, rule for aws_cloudwatch_event_rule.
func namedAfter(argument, address string) bool {
	typ := resourceTypeOf(address)
	return argument == typ[strings.LastIndex(typ, "_")+1:]
}

// referenceAttributes replaces the literals in body, of the resource at
// address, and in its nested blocks. It records the references it adds in
// g and skips those that would close a cycle.
func referenceAttributes(address string, syntax *hclsyntax.Body, body *hclwrite.Body, index map[string]referenceTarget, g dependencies) bool {
	changed := false
	names := make([]string, 0, len(syntax.Attributes))
	for name := range syntax.Attributes {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return syntax.Attributes[names[i]].SrcRange.Start.Byte < syntax.Attributes[names[j]].SrcRange.Start.Byte
	})
	target := func(argument, value string) (referenceTarget, bool) {
		t, ok := index[value]
		if !ok || t.address == address || !(referenceable(value) || namedAfter(argument, t.address)) {
			return referenceTarget{}, false
		}
		return t, g.add(address, t.address)
	}
	for _, name := range names {
		switch expr := syntax.Attributes[name].Expr.(type) {
		case *hclsyntax.TemplateExpr:
			if v, ok := literalString(expr); ok {
				if t, ok := target(name, v); ok {
					body.SetAttributeTraversal(name, t.traversal())
					changed = true
				}
			}
		case *hclsyntax.TupleConsExpr:
			if tokens, ok := referenceTuple(name, expr, target); ok {
				body.SetAttributeRaw(name, tokens)
				changed = true
			}
		}
	}
	blocks := body.Blocks()
	for i, b := range syntax.Blocks {
		if i < len(blocks) && referenceAttributes(address, b.Body, blocks[i].Body(), index, g) {
			changed = true
		}
	}
	return changed
}

// referenceTuple rewrites a list of string literals with the elements that
// are references replaced, if any are and every element is a literal.
func referenceTuple(argument string, expr *hclsyntax.TupleConsExpr, target func(argument, value string) (referenceTarget, bool)) (hclwrite.Tokens, bool) {
	literals := make([]string, 0, len(expr.Exprs))
	for _, e := range expr.Exprs {
		tmpl, ok := e.(*hclsyntax.TemplateExpr)
		if !ok {
			return nil, false
		}
		v, ok := literalString(tmpl)
		if !ok {
			return nil, false
		}
		literals = append(literals, v)
	}
	elems := make([]hclwrite.Tokens, 0, len(literals))
	replaced := false
	for _, v := range literals {
		if t, ok := target(argument, v); ok {
			elems = append(elems, hclwrite.TokensForTraversal(t.traversal()))
			replaced = true
		} else {
			elems = append(elems, hclwrite.TokensForValue(cty.StringVal(v)))
		}
	}
	if !replaced {
		return nil, false
	}
	return hclwrite.TokensForTuple(elems), true
}

func literalString(expr *hclsyntax.TemplateExpr) (string, bool) {
	if !expr.IsStringLiteral() {
		return "", false
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.Type().Equals(cty.String) {
		return "", false
	}
	return v.AsString(), true
}

// dependencies are the references between resources, from referrer to
// referenced.
type dependencies map[string]map[string]bool

// add records that from refers to to, unless to already depends on from,
// directly or not, which would make a cycle. It reports whether it added
// the reference.
func (g dependencies) add(from, to string) bool {
	if g.reaches(to, from, map[string]bool{}) {
		return false
	}
	if g[from] == nil {
		g[from] = map[string]bool{}
	}
	g[from][to] = true
	return true
}

func (g dependencies) reaches(from, to string, seen map[string]bool) bool {
	if from == to {
		return true
	}
	if seen[from] {
		return false
	}
	seen[from] = true
	for next := range g[from] {
		if g.reaches(next, to, seen) {
			return true
		}
	}
	return false
}
