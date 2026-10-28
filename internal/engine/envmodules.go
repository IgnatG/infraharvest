// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// moduleType is what merging takes for the type of a module call, whose
// address is module.<name>: calls are matched across environments as
// resources are, by the import IDs of the resources in them, then by
// name, and the merged root writes each as one call, counted by a
// deploy_ variable where some environments lack it (see
// stackMerge.features), its differing arguments variables.
const moduleType = "module"

// moduleMetaArguments are the arguments of a module call that every
// environment that has it must give the same value, which the merged
// root writes first: what the call calls.
var moduleMetaArguments = []string{"source", "version"}

// moduleImportIDs returns the import IDs, or identities, of ids, by
// address, of the resources in the module call name, by their addresses
// in it: aws_s3_bucket.this[0] for module.logs.aws_s3_bucket.this[0].
func moduleImportIDs[V any](ids map[string]V, name string) map[string]V {
	out := map[string]V{}
	prefix := "module." + name + "."
	for address, id := range ids {
		if rest, ok := strings.CutPrefix(address, prefix); ok {
			out[rest] = id
		}
	}
	return out
}

// stackAddress returns the resource or module call a traversal refers
// to, as merging addresses them (see stackBlocks), or "".
func stackAddress(t hcl.Traversal) string {
	if t.RootName() == moduleType && len(t) >= 2 {
		if attr, ok := t[1].(hcl.TraverseAttr); ok {
			return moduleType + "." + attr.Name
		}
		return ""
	}
	return resourceAddress(t)
}

// stackReferences returns the resources and module calls body refers to,
// by address, sorted.
func stackReferences(body *hclsyntax.Body) []string {
	var addresses []string
	var collect func(*hclsyntax.Body)
	collect = func(b *hclsyntax.Body) {
		for _, attr := range b.Attributes {
			for _, t := range attr.Expr.Variables() {
				if a := stackAddress(t); a != "" {
					addresses = append(addresses, a)
				}
			}
		}
		for _, inner := range b.Blocks {
			collect(inner.Body)
		}
	}
	collect(body)
	sort.Strings(addresses)
	return slices.Compact(addresses)
}

// moduleText writes the module call label, with meta, its meta-arguments,
// and body: what it calls first, then meta, then its other arguments.
func moduleText(label string, meta []string, body *mergedBody) string {
	var b strings.Builder
	fmt.Fprintf(&b, "module %q {\n", label)
	rest := &mergedBody{blocks: body.blocks}
	for _, name := range moduleMetaArguments {
		for _, a := range body.attributes {
			if a.name == name {
				fmt.Fprintf(&b, "%s = %s\n", a.name, exprText(a.value))
			}
		}
	}
	for _, a := range body.attributes {
		if !slices.Contains(moduleMetaArguments, a.name) {
			rest.attributes = append(rest.attributes, a)
		}
	}
	if len(meta) > 0 {
		b.WriteString("\n")
	}
	for _, line := range meta {
		b.WriteString(line + "\n")
	}
	if len(rest.attributes) > 0 || len(rest.blocks) > 0 {
		b.WriteString("\n")
	}
	writeMergedBody(&b, rest)
	b.WriteString("}\n")
	return b.String()
}

// moduleImports returns the import blocks of the resources in r, a module
// call of the merged root, by address: a plain one for a resource every
// environment that has the call has, where no deploy_ variable counts
// the call; else one each environment that has it imports, through
// for_each on its import IDs (see forEachImport). Each imports its
// resource by ID or by identity, as kinds says (see
// stackMerge.importKinds).
func moduleImports(r *stackResource, kinds map[string]importBy) (map[string]string, error) {
	call := addressText(r.instance())
	envs := map[string]int{}
	having := 0
	for _, member := range r.members {
		if member == nil {
			continue
		}
		having++
		for rest := range member.moduleIDs {
			envs[rest]++
		}
	}
	blocks := map[string]string{}
	for _, rest := range slices.Sorted(maps.Keys(envs)) {
		address := call + "." + rest
		kind, err := importKindOf(kinds, address, address)
		if err != nil {
			return nil, err
		}
		id := kind.value(string(hclwrite.TokensForValue(cty.StringVal(address)).Bytes()))
		if r.deploy == nil && envs[rest] == having {
			blocks[address] = fmt.Sprintf("import {\nto = %s\n%s\n}\n", address, id)
			continue
		}
		forEach, to, ok := forEachImport(address, kind)
		if !ok {
			return nil, fmt.Errorf("%s: only some environments import %s, and its address has no instance key to import it by", r.address(), address)
		}
		blocks[address] = fmt.Sprintf("import {\nfor_each = %s\n\nto = %s\n%s\n}\n", forEach, to, id)
	}
	return blocks, nil
}

// forEachImport returns, for address, the for_each of an import block
// that imports it only where the environment's import IDs, or
// identities, as kind says, have it, and its to, with the last instance
// key of address made each.value; false where address has no instance
// key.
func forEachImport(address string, kind importBy) (forEach, to string, ok bool) {
	open := strings.LastIndexByte(address, '[')
	if open < 0 {
		return "", "", false
	}
	closing := strings.IndexByte(address[open:], ']')
	if closing < 0 {
		return "", "", false
	}
	end := open + closing
	key := address[open+1 : end]
	forEach = fmt.Sprintf("[for k in [%s] : k if contains(keys(var.%s), %s)]", key, kind.variable, string(hclwrite.TokensForValue(cty.StringVal(address)).Bytes()))
	return forEach, address[:open] + "[each.value]" + address[end+1:], true
}

// moduleInstance returns, of byInstance, the merged root's resources and
// module calls by their instances' addresses, the one address, a plan's,
// is of or is in: module.logs for module.logs.aws_s3_bucket.this[0].
func moduleInstance(byInstance map[string]*stackResource, address string) *stackResource {
	if r, ok := byInstance[address]; ok {
		return r
	}
	for i := len(address) - 1; i > 0; i-- {
		if address[i] != '.' {
			continue
		}
		if r, ok := byInstance[address[:i]]; ok && r.resourceType == moduleType {
			return r
		}
	}
	return nil
}
