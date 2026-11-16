// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// initKey returns what terraform init installs for the configuration
// files, by path in the root with forward slashes, as a string that is the
// same for two configurations that need the same of it: the providers the
// files require, with their sources and versions, and those their
// resources, data sources and provider blocks imply. It returns false for
// a configuration it can't tell that of: one with a module call, which
// init installs, or a file that doesn't parse. Only the files in the
// root's directory count, as Terraform reads only those; the backend
// doesn't, as the merge always inits without it.
func initKey(files map[string][]byte) (string, bool) {
	required := map[string]string{}
	implied := map[string]bool{}
	for name, src := range files {
		if !strings.HasSuffix(name, ".tf") || strings.Contains(name, "/") {
			continue
		}
		f, diags := hclsyntax.ParseConfig(src, name, hcl.InitialPos)
		if diags.HasErrors() {
			return "", false
		}
		for _, b := range f.Body.(*hclsyntax.Body).Blocks {
			switch b.Type {
			case "module":
				return "", false
			case "terraform":
				for _, inner := range b.Body.Blocks {
					if inner.Type != "required_providers" {
						continue
					}
					for local, attr := range inner.Body.Attributes {
						v, diags := attr.Expr.Value(nil)
						if diags.HasErrors() {
							return "", false
						}
						required[local] = requirementText(v)
					}
				}
			case "provider":
				if len(b.Labels) == 1 {
					implied[b.Labels[0]] = true
				}
			case "resource", "data", "ephemeral":
				if len(b.Labels) != 2 {
					continue
				}
				if attr, ok := b.Body.Attributes["provider"]; ok {
					refs := attr.Expr.Variables()
					if len(refs) != 1 {
						return "", false
					}
					implied[refs[0].RootName()] = true
					continue
				}
				local, _, _ := strings.Cut(b.Labels[0], "_")
				implied[local] = true
			}
		}
	}
	var parts []string
	for _, local := range slices.Sorted(maps.Keys(required)) {
		parts = append(parts, local+"="+required[local])
	}
	for _, local := range slices.Sorted(maps.Keys(implied)) {
		if _, ok := required[local]; !ok {
			parts = append(parts, local)
		}
	}
	return strings.Join(parts, ";"), true
}

// requirementText returns a provider requirement, the value of an entry
// of required_providers, as text: its source and version, or the value
// itself where it is a version string alone.
func requirementText(v cty.Value) string {
	if !v.IsKnown() || v.IsNull() {
		return "?"
	}
	if v.Type() == cty.String {
		return "version " + v.AsString()
	}
	if !v.Type().IsObjectType() {
		return v.GoString()
	}
	var parts []string
	for _, key := range []string{"source", "version", "configuration_aliases"} {
		if v.Type().HasAttribute(key) {
			parts = append(parts, fmt.Sprintf("%s %s", key, v.GetAttr(key).GoString()))
		}
	}
	return strings.Join(parts, " ")
}
