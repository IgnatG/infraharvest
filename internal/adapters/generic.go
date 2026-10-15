// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// Generic returns an adapter for a module of the organisation's own, read
// from its configuration in dir, for calls of source at version ("" for a
// git or local source). It matches the module by its signature: its
// resources, one of each type, are the cluster, built around the one the
// others refer to. Where an argument of the module's resource is
// var.<name>, the call sets the variable to the imported resource's
// value; where it refers to another of the module's resources, the
// imported resource must refer to the matching one; any other expression
// is the module's own, which the plan check judges. Map declines a
// cluster with an argument or block the module doesn't set, a resource
// type the module has and the cluster doesn't, or a variable without a
// default it has nothing for. Generic fails for a module it can't match
// that way: with count, for_each, dynamic blocks, nested modules, or two
// resources of a type.
func Generic(source, version, dir string) (Adapter, error) {
	m, err := readModule(dir)
	if err != nil {
		return Adapter{}, fmt.Errorf("%s: %w", source, err)
	}
	a := Adapter{Source: source, Version: version, Anchor: m.anchor.typ}
	for _, r := range m.resources {
		if r != m.anchor {
			a.Members = append(a.Members, r.typ)
		}
	}
	sort.Strings(a.Members)
	for name := range m.variables {
		a.Inputs = append(a.Inputs, name)
	}
	sort.Strings(a.Inputs)
	for name := range m.outputs {
		a.Outputs = append(a.Outputs, name)
	}
	sort.Strings(a.Outputs)
	a.Map = m.mapCluster
	return a, nil
}

// module is a module's configuration, as Generic matches it.
type module struct {
	resources []*moduleResource
	anchor    *moduleResource
	// variables have whether each has a default.
	variables map[string]bool
	// outputs have the resource attribute each output is, if it is one.
	outputs map[string]outputOf
}

type moduleResource struct {
	typ, name string
	body      *hclsyntax.Body
}

type outputOf struct {
	resource *moduleResource
	attr     string
}

func readModule(dir string) (*module, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("no .tf files")
	}
	sort.Strings(files)
	m := &module{variables: map[string]bool{}, outputs: map[string]outputOf{}}
	type output struct {
		name string
		expr hclsyntax.Expression
	}
	var outputs []output
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		f, diags := hclsyntax.ParseConfig(src, filepath.Base(file), hcl.InitialPos)
		if diags.HasErrors() {
			return nil, diags
		}
		for _, b := range f.Body.(*hclsyntax.Body).Blocks {
			switch {
			case b.Type == "module":
				return nil, fmt.Errorf("it calls module %s", strings.Join(b.Labels, "."))
			case b.Type == "resource" && len(b.Labels) == 2:
				if err := matchable(b.Body, b.Labels[0]+"."+b.Labels[1]); err != nil {
					return nil, err
				}
				if slices.ContainsFunc(m.resources, func(r *moduleResource) bool { return r.typ == b.Labels[0] }) {
					return nil, fmt.Errorf("it has two %s resources", b.Labels[0])
				}
				m.resources = append(m.resources, &moduleResource{typ: b.Labels[0], name: b.Labels[1], body: b.Body})
			case b.Type == "variable" && len(b.Labels) == 1:
				_, hasDefault := b.Body.Attributes["default"]
				m.variables[b.Labels[0]] = hasDefault
			case b.Type == "output" && len(b.Labels) == 1:
				if attr, ok := b.Body.Attributes["value"]; ok {
					outputs = append(outputs, output{b.Labels[0], attr.Expr})
				}
			}
		}
	}
	if len(m.resources) == 0 {
		return nil, errors.New("it has no resources")
	}
	for _, o := range outputs {
		if r, attr, ok := m.attributeOf(o.expr); ok {
			m.outputs[o.name] = outputOf{r, attr}
		}
	}
	// The anchor is the resource every other one refers to.
	for _, candidate := range m.resources {
		if !slices.ContainsFunc(m.resources, func(r *moduleResource) bool { return r != candidate && !m.refersTo(r.body, candidate) }) {
			m.anchor = candidate
			break
		}
	}
	if m.anchor == nil {
		return nil, errors.New("no resource of it is one all its others refer to")
	}
	return m, nil
}

// matchable fails for a resource body Generic can't match.
func matchable(body *hclsyntax.Body, address string) error {
	for _, name := range []string{"count", "for_each"} {
		if _, ok := body.Attributes[name]; ok {
			return fmt.Errorf("%s has %s", address, name)
		}
	}
	seen := map[string]bool{}
	for _, b := range body.Blocks {
		switch b.Type {
		case "lifecycle", "provisioner", "connection":
			continue
		case "dynamic":
			return fmt.Errorf("%s has dynamic %s blocks", address, strings.Join(b.Labels, ""))
		}
		if seen[b.Type] {
			return fmt.Errorf("%s has two %s blocks", address, b.Type)
		}
		seen[b.Type] = true
		if err := matchable(b.Body, address+"."+b.Type); err != nil {
			return err
		}
	}
	return nil
}

// resourceOf returns the module's resource a traversal starts with.
func (m *module) resourceOf(t hcl.Traversal) *moduleResource {
	if len(t) < 2 {
		return nil
	}
	step, ok := t[1].(hcl.TraverseAttr)
	if !ok {
		return nil
	}
	for _, r := range m.resources {
		if r.typ == t.RootName() && r.name == step.Name {
			return r
		}
	}
	return nil
}

// attributeOf returns the resource and attribute an expression is, such
// as aws_s3_bucket.this.arn.
func (m *module) attributeOf(expr hclsyntax.Expression) (*moduleResource, string, bool) {
	t, ok := expr.(*hclsyntax.ScopeTraversalExpr)
	if !ok || len(t.Traversal) != 3 {
		return nil, "", false
	}
	attr, ok := t.Traversal[2].(hcl.TraverseAttr)
	r := m.resourceOf(t.Traversal)
	if !ok || r == nil {
		return nil, "", false
	}
	return r, attr.Name, true
}

// refersTo reports whether body, or its blocks, refer to r.
func (m *module) refersTo(body *hclsyntax.Body, r *moduleResource) bool {
	for _, attr := range body.Attributes {
		for _, t := range attr.Expr.Variables() {
			if m.resourceOf(t) == r {
				return true
			}
		}
	}
	return slices.ContainsFunc(body.Blocks, func(b *hclsyntax.Block) bool { return m.refersTo(b.Body, r) })
}

// variableOf returns the variable an expression is exactly, such as
// var.name.
func variableOf(expr hclsyntax.Expression) (string, bool) {
	t, ok := expr.(*hclsyntax.ScopeTraversalExpr)
	if !ok || len(t.Traversal) != 2 || t.Traversal.RootName() != "var" {
		return "", false
	}
	step, ok := t.Traversal[1].(hcl.TraverseAttr)
	return step.Name, ok
}

// mapping is a cluster's resources matched to the module's.
type mapping struct {
	m        *module
	matched  map[*moduleResource]Resource
	values   map[string]hclwrite.Tokens
	setFrom  map[string]string // the argument each variable's value is from
	readers  []*Reader
	unsetVar map[string]bool
}

func (m *module) mapCluster(c Cluster) (*Call, error) {
	byType := map[string]Resource{c.Anchor.Type: c.Anchor}
	for _, r := range c.Members {
		byType[r.Type] = r
	}
	mp := &mapping{m: m, matched: map[*moduleResource]Resource{}, values: map[string]hclwrite.Tokens{}, setFrom: map[string]string{}, unsetVar: map[string]bool{}}
	for _, r := range m.resources {
		imported, ok := byType[r.typ]
		if !ok {
			return nil, Declinef("%s has no %s, and the module makes one", c.Anchor.Address(), r.typ)
		}
		mp.matched[r] = imported
	}
	call := &Call{Addresses: map[string]string{}, Outputs: map[string]map[string]string{}}
	for _, r := range m.resources {
		imported := mp.matched[r]
		reader := Read(imported.Address(), imported.Body)
		mp.readers = append(mp.readers, reader)
		if err := mp.read(r.body, reader, imported.Address()); err != nil {
			return nil, err
		}
		call.Addresses[imported.Address()] = r.typ + "." + r.name
	}
	for _, reader := range mp.readers {
		if err := reader.Done(); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(m.variables))
	for name := range m.variables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value, ok := mp.values[name]
		switch {
		case ok:
			call.Set(name, value)
		case m.variables[name]:
			// Its default.
		case mp.unsetVar[name]:
			call.Set(name, hclwrite.TokensForValue(cty.NullVal(cty.DynamicPseudoType)))
		default:
			return nil, Declinef("the module's variable %s has no default, and nothing imported sets it", name)
		}
	}
	for output, of := range m.outputs {
		imported := mp.matched[of.resource]
		if call.Outputs[imported.Address()] == nil {
			call.Outputs[imported.Address()] = map[string]string{}
		}
		if current, taken := call.Outputs[imported.Address()][of.attr]; !taken || output < current {
			call.Outputs[imported.Address()][of.attr] = output
		}
	}
	return call, nil
}

// read matches the module resource's body, or one of its blocks, to the
// imported one's, read by reader.
func (mp *mapping) read(body *hclsyntax.Body, reader *Reader, path string) error {
	names := make([]string, 0, len(body.Attributes))
	for name := range body.Attributes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		expr := body.Attributes[name].Expr
		if variable, ok := variableOf(expr); ok {
			value, set := reader.Attr(name)
			if !set {
				mp.unsetVar[variable] = true
				continue
			}
			if current, taken := mp.values[variable]; taken && !sameTokens(current, value) {
				return Declinef("%s and %s.%s differ, and the module sets both from var.%s", mp.setFrom[variable], path, name, variable)
			}
			mp.values[variable] = value
			mp.setFrom[variable] = path + "." + name
			continue
		}
		if r, _, ok := mp.m.attributeOf(expr); ok {
			if !reader.RefersTo(name, mp.matched[r]) {
				return Declinef("%s.%s doesn't refer to %s, as the module's does", path, name, mp.matched[r].Address())
			}
			continue
		}
		// The module's own value: the plan check judges it.
		reader.Ignore(name)
	}
	for _, b := range body.Blocks {
		if b.Type == "lifecycle" || b.Type == "provisioner" || b.Type == "connection" {
			continue
		}
		inner, err := reader.Block(b.Type)
		if err != nil {
			return err
		}
		if inner == nil {
			// The module sets a block the import hasn't: the plan check
			// judges it.
			continue
		}
		if err := mp.read(b.Body, inner, path+"."+b.Type); err != nil {
			return err
		}
	}
	return nil
}
