// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// envBody is one environment's body of a resource, or of one of its
// nested blocks.
type envBody struct {
	env  int
	body *hclwrite.Body
}

// bodyMerger merges a resource's bodies across environments (see
// mergeBody), counting the arguments, and those that differ.
type bodyMerger struct {
	m                *stackMerge
	r                *stackResource
	total, differing int
	reason           string // why the bodies don't merge; "" if they do
}

func (b *bodyMerger) refuse(reason string) {
	if b.reason == "" {
		b.reason = reason
	}
}

// mergeBody merges the bodies of r's resources: arguments with the same
// value, once references are renamed to the merged root's resources, stay
// as they are; values the same once normalised become templates (see
// stackMerge.template); arguments set in some environments only, and
// other values that differ, variables (null where an environment doesn't
// set it); and nested blocks some environments have more or fewer of,
// dynamic blocks. It returns why the bodies don't merge where they
// don't: more than half of the arguments differ; a value that differs
// reads anything but variables, which only secrets do, and those become
// sensitive variables; a sensitive argument's literal value differs; or
// a dynamic block would hold blocks or references. A reference to a
// resource some environments lack is kept where it is set if every
// environment that has that resource sets it, and no other does.
func (m *stackMerge) mergeBody(r *stackResource) (*mergedBody, string) {
	var in []envBody
	for e, member := range r.members {
		if member != nil {
			in = append(in, envBody{e, member.block.Write.Body()})
		}
	}
	b := &bodyMerger{m: m, r: r}
	body := b.body(in, nil, hclfile.CallName(r.resourceType+"."+r.label))
	switch {
	case b.reason != "":
		return nil, b.reason
	case 2*b.differing > b.total:
		return nil, fmt.Sprintf("%d of its %d arguments differ between the environments", b.differing, b.total)
	}
	return body, ""
}

// body merges bodies in, at path in the resource's schema; prefix
// starts the names of the variables it makes.
func (b *bodyMerger) body(in []envBody, path []string, prefix string) *mergedBody {
	out := &mergedBody{}
	for _, name := range attributeUnion(in) {
		if a := b.attribute(in, path, prefix, name); a != nil {
			out.attributes = append(out.attributes, a)
		}
	}
	for _, typ := range blockTypeUnion(in) {
		counts := make([]int, len(in))
		same := true
		for i, x := range in {
			counts[i] = len(blocksOf(x.body, typ))
			same = same && counts[i] == counts[0]
		}
		if !same {
			if blk := b.dynamic(in, path, prefix, typ); blk != nil {
				out.blocks = append(out.blocks, blk)
			}
			continue
		}
		for j := range counts[0] {
			inner := make([]envBody, len(in))
			for i, x := range in {
				inner[i] = envBody{x.env, blocksOf(x.body, typ)[j].Body()}
			}
			segment := typ
			if counts[0] > 1 {
				segment = fmt.Sprintf("%s_%d", typ, j)
			}
			out.blocks = append(out.blocks, &mergedBlock{typ: typ, body: b.body(inner, append(slices.Clone(path), typ), joinName(prefix, segment))})
		}
	}
	return out
}

func joinName(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "_" + name
}

// attribute merges the argument name of bodies in, or returns nil where
// they don't merge.
func (b *bodyMerger) attribute(in []envBody, path []string, prefix, name string) *mergedAttribute {
	values := make([]hclwrite.Tokens, len(in))
	original := make([]hclwrite.Tokens, len(in))
	present := 0
	for i, x := range in {
		if attr := x.body.GetAttribute(name); attr != nil {
			original[i] = attr.Expr().BuildTokens(nil)
			values[i] = b.m.renamed(x.env, original[i])
			present++
		}
	}
	b.total++
	if present == len(in) && sameExprs(values) {
		return &mergedAttribute{name: name, kind: sameValue, value: values[0]}
	}
	if b.r.resourceType == moduleType && len(path) == 0 && slices.Contains(moduleMetaArguments, name) {
		// Another module, or another release of it: the call is each
		// environment's own.
		b.refuse(fmt.Sprintf("its %s differs between the environments", name))
		return nil
	}
	if present == len(in) {
		if normal, ok := b.m.template(in, values); ok {
			return &mergedAttribute{name: name, kind: templateValue, normal: normal}
		}
	}
	b.differing++
	schemaPath := append(slices.Clone(path), name)
	at := strings.Join(schemaPath, ".")
	resources, variables, other := false, false, false
	for _, v := range values {
		if v != nil {
			res, vars, oth := exprReads(v)
			resources, variables, other = resources || res, variables || vars, other || oth
		}
	}
	switch {
	case resources:
		return b.reference(in, values, original, at)
	case other:
		b.refuse(fmt.Sprintf("its %s reads different values in different environments", at))
		return nil
	case !variables && b.m.sensitive(b.r.resourceType, schemaPath):
		b.refuse(fmt.Sprintf("its sensitive %s differs between the environments", at))
		return nil
	}
	if !variables && present == len(in) {
		if value, ok := b.m.envCondition(in, original); ok {
			return &mergedAttribute{name: name, kind: conditionValue, value: value}
		}
	}
	v := &envVariable{
		name:         joinName(prefix, name),
		resourceType: b.r.resourceType,
		what:         at + " of " + b.r.resourceType + "." + b.r.label,
		ty:           tfrun.AttributeTypeOrAny(b.m.schemas, b.r.resourceType, schemaPath),
		values:       map[int]hclwrite.Tokens{},
		sensitive:    variables,
	}
	for i, x := range in {
		switch {
		case !variables && original[i] != nil:
			// A var file, or the variable's default, holds no function
			// calls, such as the jsonencode of a policy.
			literal, ok := literalTokens(original[i])
			if !ok {
				b.refuse(fmt.Sprintf("its %s differs between the environments in a value a var file can't hold", at))
				return nil
			}
			v.values[x.env] = literal
		case !variables:
			v.values[x.env] = original[i]
		case original[i] != nil:
			// A secret: set, but not known.
			v.values[x.env] = nil
		}
	}
	return &mergedAttribute{name: name, kind: variableValue, variable: v}
}

// enumLike matches a string that looks like a value of an enum, such as
// ACTIVE, PAY_PER_REQUEST or Enabled, rather than a free setting.
var enumLike = regexp.MustCompile(`^([A-Z][A-Z0-9]*(_[A-Z0-9]+)*|[A-Z][a-z]+)$`)

// envCondition returns values, those of bodies in, as a condition on
// var.env, where they are literal booleans, or literal strings that look
// like an enum's (see enumLike), of two values, one of which one
// environment alone has: var.env == "<its token>" for true there and
// false elsewhere (!= for false there), else var.env == "<its token>" ?
// <its value> : <the others'>. Of two environments, that one is the one
// with true, for booleans, else the last.
func (m *stackMerge) envCondition(in []envBody, values []hclwrite.Tokens) (hclwrite.Tokens, bool) {
	if len(in) < 2 {
		return nil, false
	}
	literals := make([]cty.Value, len(values))
	for i, v := range values {
		lit, ok := literalValue(v)
		if !ok || (lit.Type() != cty.Bool && (lit.Type() != cty.String || !enumLike.MatchString(lit.AsString()))) {
			return nil, false
		}
		if i > 0 && !lit.Type().Equals(literals[0].Type()) {
			return nil, false
		}
		literals[i] = lit
	}
	counts := map[string]int{}
	for _, v := range values {
		counts[exprKey(v)]++
	}
	var alone []int
	for i, v := range values {
		if counts[exprKey(v)] == 1 {
			alone = append(alone, i)
		}
	}
	if len(counts) != 2 || len(alone) == 0 {
		return nil, false
	}
	odd := alone[len(alone)-1]
	isBool := literals[odd].Type() == cty.Bool
	if isBool && len(alone) == 2 && literals[alone[0]].True() {
		odd = alone[0]
	}
	other := 0
	if other == odd {
		other = 1
	}
	token := m.norms[in[odd].env].token
	if isBool {
		op := "=="
		if literals[odd].False() {
			op = "!="
		}
		return hclfile.ParseExpr(fmt.Sprintf("var.%s %s %q", envVariableName, op, token)), true
	}
	return hclfile.ParseExpr(fmt.Sprintf("var.%s == %q ? %s : %s", envVariableName, token, strings.TrimSpace(string(values[odd].Bytes())), strings.TrimSpace(string(values[other].Bytes())))), true
}

// literalValue returns the value of tokens, an expression, where it is a
// literal that isn't null.
func literalValue(tokens hclwrite.Tokens) (cty.Value, bool) {
	expr, diags := hclsyntax.ParseExpression(tokens.Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() || len(expr.Variables()) > 0 {
		return cty.NilVal, false
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() || !v.IsKnown() || v.IsNull() {
		return cty.NilVal, false
	}
	return v, true
}

// reference merges values, an argument at that refers to resources: the
// same where set, refusing it unless the environments that set it are
// those of in that have the resources.
func (b *bodyMerger) reference(in []envBody, values, original []hclwrite.Tokens, at string) *mergedAttribute {
	first := -1
	set := map[int]bool{}
	for i, v := range values {
		if v == nil {
			continue
		}
		switch {
		case first < 0:
			first = i
		case exprKey(v) != exprKey(values[first]):
			b.refuse(fmt.Sprintf("its %s refers to different resources in different environments", at))
			return nil
		}
		set[in[i].env] = true
	}
	targets := b.m.targets(in[first].env, original[first])
	if len(targets) == 0 {
		b.refuse(fmt.Sprintf("its %s refers to resources outside the stack", at))
		return nil
	}
	for _, t := range targets {
		for _, x := range in {
			if (t.members[x.env] != nil) != set[x.env] {
				b.refuse(fmt.Sprintf("its %s refers to %s, which some environments lack, and is set where they do", at, t.resourceType+"."+t.label))
				return nil
			}
		}
	}
	return &mergedAttribute{name: at[strings.LastIndexByte(at, '.')+1:], kind: referenceValue, value: values[first], target: targets[0]}
}

// dynamic merges the blocks of type typ of bodies in, which some have
// more or fewer of, into a dynamic block over a variable: an object, or
// null, for blocks no body has more than one of; else a list of objects.
// It returns nil where the blocks hold blocks or read anything, which the
// variable can't carry.
func (b *bodyMerger) dynamic(in []envBody, path []string, prefix, typ string) *mergedBlock {
	schemaPath := append(slices.Clone(path), typ)
	at := strings.Join(schemaPath, ".")
	most := 0
	names := map[string]bool{}
	for _, x := range in {
		blocks := blocksOf(x.body, typ)
		most = max(most, len(blocks))
		for _, blk := range blocks {
			if len(blk.Body().Blocks()) > 0 {
				b.refuse(fmt.Sprintf("its %s blocks differ between the environments and hold blocks", at))
				return nil
			}
			for name, attr := range blk.Body().Attributes() {
				names[name] = true
				if res, vars, other := exprReads(attr.Expr().BuildTokens(nil)); res || vars || other {
					b.refuse(fmt.Sprintf("its %s blocks differ between the environments and read values", at))
					return nil
				}
			}
		}
	}
	fields := slices.Sorted(maps.Keys(names))
	b.total += len(fields)
	b.differing += len(fields)
	types := map[string]cty.Type{}
	for _, f := range fields {
		types[f] = tfrun.AttributeTypeOrAny(b.m.schemas, b.r.resourceType, append(slices.Clone(schemaPath), f))
	}
	ty := cty.Object(types)
	single := most <= 1
	if !single {
		ty = cty.List(ty)
	}
	v := &envVariable{
		name:         joinName(prefix, typ),
		resourceType: b.r.resourceType,
		what:         at + " blocks of " + b.r.resourceType + "." + b.r.label,
		ty:           ty,
		values:       map[int]hclwrite.Tokens{},
	}
	for _, x := range in {
		blocks := blocksOf(x.body, typ)
		objects := make([]hclwrite.Tokens, len(blocks))
		for i, blk := range blocks {
			objects[i] = objectTokens(blk.Body())
		}
		switch {
		case single && len(objects) == 0:
			v.values[x.env] = nil
		case single:
			v.values[x.env] = objects[0]
		default:
			v.values[x.env] = hclwrite.TokensForTuple(objects)
		}
	}
	return &mergedBlock{typ: typ, variable: v, single: single, fields: fields}
}

// blocksKey is a key of the nested blocks of body, in environment env,
// with references renamed: the same for the same blocks.
func (m *stackMerge) blocksKey(env int, body *hclwrite.Body) string {
	var b strings.Builder
	for _, blk := range body.Blocks() {
		b.WriteString(blk.Type())
		b.WriteString("{")
		for _, name := range slices.Sorted(maps.Keys(blk.Body().Attributes())) {
			fmt.Fprintf(&b, "%s=%s;", name, exprKey(m.renamed(env, blk.Body().GetAttribute(name).Expr().BuildTokens(nil))))
		}
		b.WriteString(m.blocksKey(env, blk.Body()))
		b.WriteString("}")
	}
	return b.String()
}

// constantBody is body, in environment env, as a merged body whose
// values are written as they are, with references renamed.
func (m *stackMerge) constantBody(env int, body *hclwrite.Body) *mergedBody {
	out := &mergedBody{}
	for _, name := range slices.Sorted(maps.Keys(body.Attributes())) {
		out.attributes = append(out.attributes, &mergedAttribute{name: name, kind: sameValue, value: m.renamed(env, body.GetAttribute(name).Expr().BuildTokens(nil))})
	}
	for _, blk := range body.Blocks() {
		out.blocks = append(out.blocks, &mergedBlock{typ: blk.Type(), body: m.constantBody(env, blk.Body())})
	}
	return out
}

// template returns values, those of bodies in, with placeholders (see
// normaliser.value), and true, where that makes them the same and each
// environment's value is the template with its own token, scope and
// group.
func (m *stackMerge) template(in []envBody, values []hclwrite.Tokens) (hclwrite.Tokens, bool) {
	normal := mapLiterals(values[0], m.norms[in[0].env].value)
	for i, x := range in {
		if exprKey(mapLiterals(values[i], m.norms[x.env].value)) != exprKey(normal) {
			return nil, false
		}
		if exprKey(mapLiterals(normal, m.norms[x.env].denormalise)) != exprKey(values[i]) {
			return nil, false
		}
	}
	return normal, true
}

// renamed returns tokens, an expression of environment env, with each
// reference to a resource of env's root made to the merged root's
// instance of it (see stackResource.instance).
func (m *stackMerge) renamed(env int, tokens hclwrite.Tokens) hclwrite.Tokens {
	return renameTokens(tokens, func(address string) (hclwrite.Tokens, bool) {
		r, ok := m.index[env][address]
		if !ok || r.stack == nil {
			return nil, false
		}
		return hclwrite.TokensForTraversal(r.stack.instance()), true
	})
}

// targets returns the merged root's resources tokens, an expression of
// environment env, refers to.
func (m *stackMerge) targets(env int, tokens hclwrite.Tokens) []*stackResource {
	expr, diags := hclsyntax.ParseExpression(tokens.Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() {
		return nil
	}
	var targets []*stackResource
	for _, t := range expr.Variables() {
		if r, ok := m.index[env][stackAddress(t)]; ok && r.stack != nil {
			targets = append(targets, r.stack)
		}
	}
	return targets
}

// sensitive reports whether the provider marks the attribute at path of
// resourceType sensitive.
func (m *stackMerge) sensitive(resourceType string, path []string) bool {
	attr := tfrun.SchemaAttribute(m.schemas, resourceType, path)
	return attr != nil && attr.Sensitive
}
