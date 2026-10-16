// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// lookup is a data source that reads a value no root manages, which the
// cloud or the provider defines, such as the tenant Azure signs in to.
type lookup struct {
	// typ and name are the data source's type and name, name unless
	// taken.
	typ, name string
	// args are its arguments, as name and value.
	args [][2]string
	// attribute is the data source's attribute that has the value.
	attribute string
}

func (l lookup) key() string {
	key := l.typ
	for _, a := range l.args {
		key += "\x00" + a[0] + "=" + a[1]
	}
	return key
}

// uuid matches a UUID, such as an Azure tenant's ID.
var uuid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// lookupFor returns the data source that reads value, of the argument of
// a resource of resourceType, if one does.
func lookupFor(resourceType, argument, value string) (lookup, bool) {
	// The tenant the provider signs in to; the plan checks it is the
	// resource's.
	if strings.HasPrefix(resourceType, "azurerm_") && argument == "tenant_id" && uuid.MatchString(value) {
		return lookup{typ: "azurerm_client_config", name: "current", attribute: "tenant_id"}, true
	}
	return lookup{}, false
}

// addLookups replaces literals in generated.tf that a data source reads
// (see lookupFor) with references to it, such as tenant_id =
// data.azurerm_client_config.current.tenant_id, for the resources not in
// skip, and adds the data sources to data.tf, named other than those in
// taken and data.tf. It returns the resources it changed.
func addLookups(dir string, taken Names, skip map[string]int) (map[string]bool, error) {
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	dataPath := filepath.Join(dir, DataFileName)
	data, err := loadOptionalHCL(dataPath)
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	for name := range taken {
		if address, ok := strings.CutPrefix(name, "data."); ok {
			used[address] = true
		}
	}
	if data != nil {
		for _, b := range data.syntax.Blocks {
			if b.Type == "data" && len(b.Labels) == 2 {
				used[b.Labels[0]+"."+b.Labels[1]] = true
			}
		}
	}
	addresses := map[string]string{} // by key
	var blocks []*hclwrite.Block
	address := func(l lookup) string {
		if a, ok := addresses[l.key()]; ok {
			return a
		}
		base := l.typ + "." + l.name
		a := base
		for n := 2; used[a]; n++ {
			a = fmt.Sprintf("%s_%d", base, n)
		}
		used[a] = true
		addresses[l.key()] = a
		typ, name, _ := strings.Cut(a, ".")
		block := hclwrite.NewBlock("data", []string{typ, name})
		for _, arg := range l.args {
			block.Body().SetAttributeValue(arg[0], cty.StringVal(arg[1]))
		}
		blocks = append(blocks, block)
		return a
	}
	edited := map[string]bool{}
	for _, r := range generated.resources() {
		if skip[r.address] > 0 {
			continue
		}
		typ := resourceTypeOf(r.address)
		replace := func(argument, _, value string) (hclwrite.Tokens, bool) {
			l, ok := lookupFor(typ, argument, value)
			if !ok {
				return nil, false
			}
			dataType, name, _ := strings.Cut(address(l), ".")
			return hclwrite.TokensForTraversal(hcl.Traversal{
				hcl.TraverseRoot{Name: "data"}, hcl.TraverseAttr{Name: dataType}, hcl.TraverseAttr{Name: name}, hcl.TraverseAttr{Name: l.attribute},
			}), true
		}
		if eachLiteral(r.syntax.Body, r.write.Body(), "", generated.src, replace) {
			edited[r.address] = true
		}
	}
	if len(edited) == 0 {
		return nil, nil
	}
	if err := generated.save(); err != nil {
		return nil, err
	}
	return edited, appendBlocks(dataPath, dataFileHeader, blocks)
}

// eachLiteral replaces the string literals of body, in nested block block
// ("" at the top), its lists and its nested blocks, with what replace
// returns for them, given the argument that holds each, and reports
// whether it replaced any.
func eachLiteral(syntax *hclsyntax.Body, body *hclwrite.Body, block string, src []byte, replace func(argument, block, value string) (hclwrite.Tokens, bool)) bool {
	changed := false
	for _, name := range attributeNames(syntax) {
		switch expr := syntax.Attributes[name].Expr.(type) {
		case *hclsyntax.TemplateExpr:
			if v, ok := literalString(expr); ok {
				if tokens, ok := replace(name, block, v); ok {
					body.SetAttributeRaw(name, tokens)
					changed = true
				}
			}
		case *hclsyntax.TupleConsExpr:
			inList := func(argument, value string) (hclwrite.Tokens, bool) { return replace(argument, block, value) }
			if tokens, ok := referenceTuple(name, expr, inList, src); ok {
				body.SetAttributeRaw(name, tokens)
				changed = true
			}
		}
	}
	blocks := body.Blocks()
	for i, b := range syntax.Blocks {
		if i < len(blocks) && eachLiteral(b.Body, blocks[i].Body(), b.Type, src, replace) {
			changed = true
		}
	}
	return changed
}
