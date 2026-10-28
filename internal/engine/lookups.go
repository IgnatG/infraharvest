// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// lookup is a data source that reads a value no root manages, which the
// cloud or the provider defines: the tenant Azure signs in to, a
// CloudFront policy or KMS key AWS manages.
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

// lookupFor returns the data source that reads value, of the argument of
// a resource of resourceType, if one does, as the built-in catalogue of
// the type's provider says (see catalogue.Lookup), such as the tenant
// Azure signs in to for a tenant_id.
func lookupFor(resourceType, argument, value string) (lookup, bool) {
	found, ok := catalogue.ForType(resourceType).Lookup(resourceType, argument, value)
	if !ok {
		return lookup{}, false
	}
	return lookup{typ: found.Type, name: hclfile.Label(snakeCase(found.Label)), args: found.Arguments, attribute: found.Attribute}, true
}

// dataBlocks names the data sources of lookups, other than those in
// taken and data.tf, and keeps the blocks to add to data.tf.
type dataBlocks struct {
	path      string
	used      map[string]bool
	addresses map[string]string // by lookup key
	blocks    []*hclwrite.Block
}

func newDataBlocks(dir string, taken Names) (*dataBlocks, error) {
	d := &dataBlocks{path: filepath.Join(dir, DataFileName), used: map[string]bool{}, addresses: map[string]string{}}
	data, err := hclfile.LoadOptional(d.path)
	if err != nil {
		return nil, err
	}
	for name := range taken {
		if address, ok := strings.CutPrefix(name, "data."); ok {
			d.used[address] = true
		}
	}
	if data != nil {
		for _, b := range data.Syntax.Blocks {
			if b.Type == "data" && len(b.Labels) == 2 {
				d.used[b.Labels[0]+"."+b.Labels[1]] = true
			}
		}
	}
	return d, nil
}

// reference returns the reference to l's attribute, adding its data
// source the first time.
func (d *dataBlocks) reference(l lookup) hclwrite.Tokens {
	address, ok := d.addresses[l.key()]
	if !ok {
		base := l.typ + "." + l.name
		address = base
		for n := 2; d.used[address]; n++ {
			address = fmt.Sprintf("%s_%d", base, n)
		}
		d.used[address] = true
		d.addresses[l.key()] = address
		typ, name, _ := strings.Cut(address, ".")
		block := hclwrite.NewBlock("data", []string{typ, name})
		for _, arg := range l.args {
			block.Body().SetAttributeValue(arg[0], cty.StringVal(arg[1]))
		}
		d.blocks = append(d.blocks, block)
	}
	typ, name, _ := strings.Cut(address, ".")
	return hclwrite.TokensForTraversal(hcl.Traversal{
		hcl.TraverseRoot{Name: "data"}, hcl.TraverseAttr{Name: typ}, hcl.TraverseAttr{Name: name}, hcl.TraverseAttr{Name: l.attribute},
	})
}

// write adds the data sources to data.tf.
func (d *dataBlocks) write() error {
	return appendBlocks(d.path, dataFileHeader, d.blocks)
}

// addLookups replaces literals in generated.tf that a data source reads
// (see lookupFor) with references to it, such as tenant_id =
// data.azurerm_client_config.current.tenant_id, for the resources not in
// skip, and adds the data sources to data.tf, named other than those in
// taken and data.tf. It returns the resources it changed.
func addLookups(dir string, taken Names, skip map[string]int) (map[string]bool, error) {
	generated, err := hclfile.Load(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	data, err := newDataBlocks(dir, taken)
	if err != nil {
		return nil, err
	}
	edited := map[string]bool{}
	for _, r := range generated.Resources() {
		if skip[r.Address] > 0 {
			continue
		}
		typ := AddressType(r.Address)
		replace := func(argument, _, value string) (hclwrite.Tokens, bool) {
			l, ok := lookupFor(typ, argument, value)
			if !ok {
				return nil, false
			}
			return data.reference(l), true
		}
		if eachLiteral(r.Syntax.Body, r.Write.Body(), "", generated.Src, replace) {
			edited[r.Address] = true
		}
	}
	if len(edited) == 0 {
		return nil, nil
	}
	if err := generated.Save(); err != nil {
		return nil, err
	}
	return edited, data.write()
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
			if v, ok := hclfile.LiteralString(expr); ok {
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
