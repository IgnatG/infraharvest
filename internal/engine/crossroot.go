// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// RootResource is a resource another root of the same import manages.
type RootResource struct {
	Type, ID string
	// Name is the resource's name in that root, and Root that root's
	// directory, relative to the output directory.
	Name, Root string
}

// ARN is where the ARNs of a resource type whose IDs aren't ARNs name its
// resources: the ARN's service, and the resource type its resource part
// starts with, "" if the part is the ID alone.
type ARN struct {
	Service, ResourceType string
}

// rootValue is the value of an attribute of a resource another root
// manages.
type rootValue struct {
	resource  RootResource
	attribute string
}

func (v rootValue) address() string {
	return v.resource.Type + "." + v.resource.Name
}

// rootValues find the resources of other roots that literals name: by
// their IDs, their ARNs or other values (see byNaming).
type rootValues struct {
	byID   map[string][]RootResource
	byType map[string][]RootResource
	arns   map[string]ARN
	// types are the keys of byType, sorted.
	types []string
}

func newRootValues(others []RootResource, arns map[string]ARN) rootValues {
	v := rootValues{byID: map[string][]RootResource{}, byType: map[string][]RootResource{}, arns: arns}
	for _, r := range others {
		v.byID[r.ID] = append(v.byID[r.ID], r)
		v.byType[r.Type] = append(v.byType[r.Type], r)
	}
	v.types = slices.Sorted(maps.Keys(v.byType))
	return v
}

// match returns the attribute of a resource of another root that value,
// of argument, is: its ID, where the value identifies it on its own (see
// referenceable) or the argument names its type; its ARN; or another
// value that names it, such as a load balancer's DNS name (see
// byNaming). A value of several resources is none of them, unless one is
// the others' parent.
func (v rootValues) match(argument, value string) (rootValue, bool) {
	var found []rootValue
	for _, r := range v.byID[value] {
		attribute := "name"
		switch {
		case strings.HasPrefix(r.ID, "arn:"):
			attribute = "arn"
		case referenceable(r.ID):
			attribute = "id"
		case !namesType(argument, r.Type+"."+r.Name):
			continue
		}
		found = append(found, rootValue{resource: r, attribute: attribute})
	}
	if len(found) == 0 && strings.HasPrefix(value, "arn:") {
		found = v.byARN(value)
	}
	if len(found) == 0 {
		found = v.byNaming(value)
	}
	return onlyValue(found)
}

// byNaming returns the resources value names other than by their IDs, as
// the built-in catalogue of each type's provider says (see
// catalogue.NamedBy), such as a load balancer by its DNS name, a bucket
// by its regional domain name or a Google resource by its self link.
func (v rootValues) byNaming(value string) []rootValue {
	var found []rootValue
	for _, typ := range v.types {
		for _, n := range catalogue.ForType(typ).NamedBy(typ, value) {
			for _, r := range v.byType[typ] {
				if n.Names(r.ID) {
					found = append(found, rootValue{resource: r, attribute: n.Attribute})
				}
			}
		}
	}
	return found
}

// rootLookup returns the data source that reads v wherever the root is,
// if one does, as the built-in catalogue of its type's provider says (see
// catalogue.RootLookup), such as a Google network, which is global, by
// its project and name, as the regional roots' subnetworks and routers
// name it.
func rootLookup(v rootValue) (lookup, bool) {
	found, ok := catalogue.ForType(v.resource.Type).RootLookup(v.resource.Type, v.attribute, v.resource.ID)
	if !ok {
		return lookup{}, false
	}
	name := v.resource.Name
	if name == "" {
		name = found.Label
	}
	return lookup{typ: found.Type, name: Label(name), args: found.Arguments, attribute: found.Attribute}, true
}

// byARN returns the resources whose ARN value is: by the catalogue's ARN
// of their type, the ARN's resource part ends in the ID, as a role's ARN
// ends in its name, after its path.
func (v rootValues) byARN(value string) []rootValue {
	parts := strings.SplitN(value, ":", 6)
	if len(parts) != 6 {
		return nil
	}
	service, resource := parts[2], parts[5]
	var found []rootValue
	for typ, arn := range v.arns {
		if arn.Service != service {
			continue
		}
		rest := resource
		if arn.ResourceType != "" {
			var ok bool
			if rest, ok = strings.CutPrefix(resource, arn.ResourceType+"/"); !ok {
				if rest, ok = strings.CutPrefix(resource, arn.ResourceType+":"); !ok {
					continue
				}
			}
		}
		last := rest[strings.LastIndexAny(rest, "/:")+1:]
		for _, r := range v.byType[typ] {
			if r.ID == rest || r.ID == last {
				found = append(found, rootValue{resource: r, attribute: "arn"})
			}
		}
	}
	return found
}

// onlyValue returns the one value of found, or the one of the others'
// parent (see parentOf).
func onlyValue(found []rootValue) (rootValue, bool) {
	switch len(found) {
	case 0:
		return rootValue{}, false
	case 1:
		return found[0], true
	}
	addresses := make([]string, 0, len(found))
	for _, f := range found {
		addresses = append(addresses, f.address())
	}
	parent := parentOf(addresses)
	for _, f := range found {
		if f.address() == parent {
			return f, true
		}
	}
	return rootValue{}, false
}

// attributeWords say what an attribute of a resource of another root is,
// for its variable's description.
var attributeWords = map[string]string{"arn": "The ARN", "id": "The ID", "name": "The name", "dns_name": "The DNS name", "bucket_regional_domain_name": "The regional domain name", "self_link": "The self link"}

// addRootVariables replaces literals in generated.tf that are the ID, ARN,
// self link or DNS name of a resource another root manages (see
// rootValues.match), in the resources skip leaves. A value a data source
// reads wherever the root is, as a Google network's self link (see
// rootLookup), refers to the data source, added to data.tf; at level 1 in
// skip, and for any other value, it refers to a variable, which
// variables.tf declares with the literal as its default and where it comes
// from as its description: role_arn = var.app_role_arn. That is for what
// no data source can read, such as a regional resource in the global
// root, whose provider is in another region. Variables and data sources
// are named other than those in taken, variables.tf and data.tf. It
// returns the resources it changed.
func addRootVariables(dir string, others []RootResource, arns map[string]ARN, taken Names, skip map[string]int) (map[string]bool, error) {
	if len(others) == 0 {
		return nil, nil
	}
	others = slices.Clone(others)
	sort.Slice(others, func(i, j int) bool {
		a, b := others[i], others[j]
		return a.Type+"\x00"+a.Name+"\x00"+a.ID < b.Type+"\x00"+b.Name+"\x00"+b.ID
	})
	values := newRootValues(others, arns)
	generated, err := hclfile.Load(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, VariablesFileName)
	variables, err := hclfile.LoadOptional(path)
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	for name := range taken {
		if v, ok := strings.CutPrefix(name, "var."); ok {
			used[v] = true
		}
	}
	if variables != nil {
		for _, b := range variables.Syntax.Blocks {
			if b.Type == "variable" && len(b.Labels) == 1 {
				used[b.Labels[0]] = true
			}
		}
	}
	names := map[string]string{} // by value and what it is
	var blocks []*hclwrite.Block
	variable := func(v rootValue, value string) string {
		key := v.address() + "." + v.attribute + "\x00" + value
		if name, ok := names[key]; ok {
			return name
		}
		label := v.resource.Name
		if label == "" {
			label = v.resource.ID
		}
		label = Label(label)
		if word := v.resource.Type[strings.LastIndex(v.resource.Type, "_")+1:]; !slices.Contains(strings.Split(label, "_"), word) && !strings.HasPrefix(v.attribute, word+"_") {
			label += "_" + word
		}
		base := label + "_" + v.attribute
		name := base
		for n := 2; used[name]; n++ {
			name = fmt.Sprintf("%s_%d", base, n)
		}
		used[name] = true
		names[key] = name
		block := hclwrite.NewBlock("variable", []string{name})
		block.Body().SetAttributeValue("description", cty.StringVal(fmt.Sprintf(
			"%s of %s, which the root %s manages. The default is its value when this root was imported.",
			attributeWords[v.attribute], v.address(), v.resource.Root)))
		block.Body().SetAttributeRaw("type", hclwrite.TokensForIdentifier("string"))
		block.Body().SetAttributeValue("default", cty.StringVal(value))
		blocks = append(blocks, block)
		return name
	}
	data, err := newDataBlocks(dir, taken)
	if err != nil {
		return nil, err
	}
	edited := map[string]bool{}
	for _, r := range generated.Resources() {
		level := skip[r.Address]
		if level >= 2 {
			continue
		}
		replace := func(argument, block, value string) (hclwrite.Tokens, bool) {
			v, ok := values.match(effectiveArgument(argument, block), value)
			if !ok {
				return nil, false
			}
			// A data source first; a variable where its value differs.
			if l, ok := rootLookup(v); ok && level == 0 {
				return data.reference(l), true
			}
			return hclwrite.TokensForTraversal(hcl.Traversal{hcl.TraverseRoot{Name: "var"}, hcl.TraverseAttr{Name: variable(v, value)}}), true
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
	if err := data.write(); err != nil {
		return nil, err
	}
	return edited, appendBlocks(path, "", blocks)
}
