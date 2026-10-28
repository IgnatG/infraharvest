// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// outputIndex matches an output with an index into it: public_subnets[0].
var outputIndex = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)\[([0-9]+)\]$`)

// outputLists returns the length of each list output of the trials'
// module calls whose elements are all the outputs of members, by
// reference: module.vpc.public_subnets is 2 long when the module's two
// public subnets are its public_subnets[0] and public_subnets[1].
func outputLists(trials []*trial) map[string]int {
	indices := map[string]map[int]bool{}
	for _, t := range trials {
		for _, outputs := range t.call.Outputs {
			for _, output := range outputs {
				m := outputIndex.FindStringSubmatch(output)
				if m == nil {
					continue
				}
				i, err := strconv.Atoi(m[2])
				if err != nil {
					continue
				}
				key := "module." + t.name + "." + m[1]
				if indices[key] == nil {
					indices[key] = map[int]bool{}
				}
				indices[key][i] = true
			}
		}
	}
	lists := map[string]int{}
	for key, set := range indices {
		complete := true
		for i := range len(set) {
			complete = complete && set[i]
		}
		if complete {
			lists[key] = len(set)
		}
	}
	return lists
}

// collapseOutputLists replaces, in generated.tf, each list of every
// element of a list output of lists (see outputLists), such as
// [module.vpc.public_subnets[1], module.vpc.public_subnets[0]], with the
// output: module.vpc.public_subnets. A list in another order is replaced
// only where the argument is a set, as the schemas, which it gets once it
// needs them, say. Blocks that only name the output's subnets (see
// catalogue.BlocksAsList) become the argument that takes them. It reports
// whether it changed anything.
func collapseOutputLists(dir string, lists map[string]int, getSchemas func() (*tfjson.ProviderSchemas, error)) (bool, error) {
	generated, err := hclfile.Load(filepath.Join(dir, rootfile.GeneratedFileName))
	if err != nil {
		return false, err
	}
	var schemas *tfjson.ProviderSchemas
	fetched := false
	isSet := func(resourceType string, path []string) (bool, error) {
		if !fetched {
			fetched = true
			if schemas, err = getSchemas(); err != nil {
				return false, err
			}
		}
		attr := tfrun.SchemaAttribute(schemas, resourceType, path)
		return attr != nil && attr.AttributeType.IsSetType(), nil
	}
	changed := false
	blocks := generated.Edit().Body().Blocks()
	for i, b := range generated.Syntax.Blocks {
		if i >= len(blocks) || len(b.Labels) == 0 || (b.Type != "resource" && b.Type != "module") {
			continue
		}
		resourceType := ""
		if b.Type == "resource" {
			resourceType = b.Labels[0]
		}
		c, err := collapseIn(b.Body, blocks[i].Body(), resourceType, nil, lists, isSet)
		if err != nil {
			return false, err
		}
		if m, ok := catalogue.ForType(resourceType).BlocksAsList(resourceType); ok && collapseMappings(b.Body, blocks[i].Body(), m, lists) {
			c = true
		}
		changed = changed || c
	}
	if !changed {
		return false, nil
	}
	return true, generated.Save()
}

// collapseIn collapses the lists of body, at path in resourceType's schema
// ("" for a module call, whose inputs have none), and of its nested blocks.
func collapseIn(syntax *hclsyntax.Body, body *hclwrite.Body, resourceType string, path []string, lists map[string]int, isSet func(string, []string) (bool, error)) (bool, error) {
	changed := false
	for _, name := range attributeNames(syntax) {
		tuple, ok := syntax.Attributes[name].Expr.(*hclsyntax.TupleConsExpr)
		if !ok {
			continue
		}
		var elems []hcl.Traversal
		for _, e := range tuple.Exprs {
			if t, ok := e.(*hclsyntax.ScopeTraversalExpr); ok {
				elems = append(elems, t.Traversal)
			}
		}
		if len(elems) != len(tuple.Exprs) {
			continue
		}
		output, inOrder, ok := wholeOutput(elems, lists)
		if !ok {
			continue
		}
		if !inOrder {
			if resourceType == "" {
				continue
			}
			set, err := isSet(resourceType, append(append([]string(nil), path...), name))
			if err != nil {
				return false, err
			}
			if !set {
				continue
			}
		}
		body.SetAttributeTraversal(name, output)
		changed = true
	}
	blocks := body.Blocks()
	for i, b := range syntax.Blocks {
		if i >= len(blocks) {
			continue
		}
		c, err := collapseIn(b.Body, blocks[i].Body(), resourceType, append(append([]string(nil), path...), b.Type), lists, isSet)
		if err != nil {
			return false, err
		}
		changed = changed || c
	}
	return changed, nil
}

// collapseMappings replaces body's blocks of m, if each only sets m's
// attribute to an element of one list output and together they name all
// of it, with m's argument set to the output.
func collapseMappings(syntax *hclsyntax.Body, body *hclwrite.Body, m catalogue.BlocksAsList, lists map[string]int) bool {
	if _, ok := syntax.Attributes[m.Argument]; ok {
		return false
	}
	var elems []hcl.Traversal
	var mappings []*hclwrite.Block
	blocks := body.Blocks()
	for i, b := range syntax.Blocks {
		if b.Type != m.Block {
			continue
		}
		attr, ok := b.Body.Attributes[m.Attribute]
		if !ok || len(b.Body.Attributes) != 1 || len(b.Body.Blocks) != 0 || i >= len(blocks) {
			return false
		}
		t, ok := attr.Expr.(*hclsyntax.ScopeTraversalExpr)
		if !ok {
			return false
		}
		elems = append(elems, t.Traversal)
		mappings = append(mappings, blocks[i])
	}
	output, _, ok := wholeOutput(elems, lists)
	if !ok {
		return false
	}
	for _, b := range mappings {
		body.RemoveBlock(b)
	}
	body.SetAttributeTraversal(m.Argument, output)
	return true
}

// wholeOutput returns the output of lists that elems, module.<call>.<output>[i]
// each, are every element of, once each, and whether they are in its
// order.
func wholeOutput(elems []hcl.Traversal, lists map[string]int) (hcl.Traversal, bool, bool) {
	if len(elems) == 0 {
		return nil, false, false
	}
	var key string
	seen := map[int]bool{}
	inOrder := true
	for i, t := range elems {
		if len(t) != 4 || t.RootName() != "module" {
			return nil, false, false
		}
		call, ok1 := t[1].(hcl.TraverseAttr)
		output, ok2 := t[2].(hcl.TraverseAttr)
		index, ok3 := t[3].(hcl.TraverseIndex)
		if !ok1 || !ok2 || !ok3 || !index.Key.Type().Equals(cty.Number) || !index.Key.IsKnown() || index.Key.IsNull() {
			return nil, false, false
		}
		n, accuracy := index.Key.AsBigFloat().Int64()
		if accuracy != 0 || n < 0 {
			return nil, false, false
		}
		k := "module." + call.Name + "." + output.Name
		if i == 0 {
			key = k
		}
		if k != key || seen[int(n)] {
			return nil, false, false
		}
		seen[int(n)] = true
		inOrder = inOrder && int(n) == i
	}
	if lists[key] != len(elems) {
		return nil, false, false
	}
	for i := range len(elems) {
		if !seen[i] {
			return nil, false, false
		}
	}
	return elems[0][:3], inOrder, true
}
