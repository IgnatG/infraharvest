// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/pkg/cfn"
)

// StackMapper returns the mapper the importer carries a stack's template
// into the roots with (see harvest.ProviderWithStackMapper): what it knows
// rather than guesses. The Terraform type of a CloudFormation type is the
// first of those --cfn-stack matches it with (cloudFormationTypes and the
// catalogue's cloud_control entries) that the provider's schema has; each
// name cfn.DefaultMapper gives a property, Ref or Fn::GetAtt is kept only
// when that type's schema has it, and is no longer a guess then. What the
// schema doesn't have, the mapper has no answer for, so nothing is written
// that terraform validate would reject.
func (p *Provider) StackMapper(schemas *tfjson.ProviderSchemas) cfn.Mapper {
	return newStackMapper(stackTypes(p.catalogue()), providerResourceSchemas(schemas, "aws"))
}

// providerResourceSchemas returns the resource schemas of the provider
// whose source ends in /name, or nil.
func providerResourceSchemas(schemas *tfjson.ProviderSchemas, name string) map[string]*tfjson.Schema {
	if schemas == nil {
		return nil
	}
	for source, s := range schemas.Schemas {
		if s != nil && (source == name || strings.HasSuffix(source, "/"+name)) {
			return s.ResourceSchemas
		}
	}
	return nil
}

// stackMapper is the mapper StackMapper returns.
type stackMapper struct {
	types    map[string][]string
	schemas  map[string]*tfjson.Schema
	fallback cfn.Mapper
}

func newStackMapper(types map[string][]string, schemas map[string]*tfjson.Schema) *stackMapper {
	return &stackMapper{types: types, schemas: schemas, fallback: cfn.DefaultMapper()}
}

// resolve returns the Terraform type of a CloudFormation type and its
// schema, or nil.
func (m *stackMapper) resolve(cfnType string) (string, *tfjson.SchemaBlock) {
	for _, t := range m.types[cfnType] {
		if s := m.schemas[t]; s != nil && s.Block != nil {
			return t, s.Block
		}
	}
	// A type the default mapper knows, rather than derives, that --cfn-stack
	// has no list for.
	if d, ok := m.fallback.ResourceType(cfnType); ok && !d.Guessed {
		if s := m.schemas[d.Name]; s != nil && s.Block != nil {
			return d.Name, s.Block
		}
	}
	return "", nil
}

func (m *stackMapper) ResourceType(cfnType string) (cfn.Mapped, bool) {
	typ, block := m.resolve(cfnType)
	if block == nil {
		return cfn.Mapped{}, false
	}
	return cfn.Mapped{Name: typ}, true
}

func (m *stackMapper) Property(cfnType string, path []string) (cfn.Property, bool) {
	_, block := m.resolve(cfnType)
	if block == nil || len(path) == 0 {
		return cfn.Property{}, false
	}
	p, ok := m.fallback.Property(cfnType, path)
	if !ok || p.Kind == cfn.KindOmit {
		return p, ok
	}
	// Down the nested blocks the path goes through; a value nested in an
	// argument, rather than a block, can't be checked, and keeps what the
	// default mapper says of it.
	for i := 1; i < len(path); i++ {
		parent, ok := m.fallback.Property(cfnType, path[:i])
		nested := block.NestedBlocks[parent.Name]
		if !ok || nested == nil || nested.Block == nil {
			return p, true
		}
		block = nested.Block
	}
	if a := block.Attributes[p.Name]; a != nil {
		if !a.Required && !a.Optional {
			// Computed only: not an argument.
			return cfn.Property{}, false
		}
		p.Guessed = false
		if p.Kind == cfn.KindBlock {
			p.Kind = cfn.KindAttribute
		}
		return p, true
	}
	if nested := block.NestedBlocks[p.Name]; nested != nil {
		p.Guessed = false
		if p.Kind == cfn.KindAttribute {
			p.Kind = cfn.KindBlock
		}
		return p, true
	}
	return cfn.Property{}, false
}

func (m *stackMapper) Ref(cfnType string) (cfn.Mapped, bool) {
	_, block := m.resolve(cfnType)
	if block == nil {
		return cfn.Mapped{}, false
	}
	if r, ok := m.fallback.Ref(cfnType); ok && hasAttribute(block, r.Name) {
		return cfn.Mapped{Name: r.Name}, true
	}
	// Ref returns the physical ID, which --cfn-stack matched with the
	// listed resource's ID, which is the id attribute of nearly every type.
	if hasAttribute(block, "id") {
		return cfn.Mapped{Name: "id"}, true
	}
	return cfn.Mapped{}, false
}

func (m *stackMapper) Attribute(cfnType, attribute string) (cfn.Mapped, bool) {
	_, block := m.resolve(cfnType)
	if block == nil {
		return cfn.Mapped{}, false
	}
	if a, ok := m.fallback.Attribute(cfnType, attribute); ok && hasAttribute(block, a.Name) {
		return cfn.Mapped{Name: a.Name}, true
	}
	return cfn.Mapped{}, false
}

// hasAttribute reports whether block has the attribute or block a path
// such as arn or endpoint[0].address starts with.
func hasAttribute(block *tfjson.SchemaBlock, path string) bool {
	name := path
	if i := strings.IndexAny(name, ".["); i >= 0 {
		name = name[:i]
	}
	return block.Attributes[name] != nil || block.NestedBlocks[name] != nil
}
