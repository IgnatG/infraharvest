// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"slices"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// RootProvider returns provider name as the root in dir, which Generate
// wrote, configures it: its source and version constraint, from
// required_providers, and the arguments of its provider block without an
// alias, as Provider.Config holds them: strings, bools and lists of
// strings as Go values, other values as cty values, and nested blocks as
// maps, or lists of maps where they repeat. Its default tags and labels,
// which merging reads from the root's files, are left out. The root's
// files hold no credentials, so neither does its configuration.
func RootProvider(dir, name string) (Provider, error) {
	files, err := hclfile.ConfigFiles(dir)
	if err != nil {
		return Provider{}, err
	}
	p := Provider{Name: name}
	found := false
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			switch {
			case b.Type == "terraform":
				for _, nested := range b.Body.Blocks {
					if nested.Type != "required_providers" {
						continue
					}
					if attr, ok := nested.Body.Attributes[name]; ok {
						v, diags := attr.Expr.Value(nil)
						if diags.HasErrors() || !v.Type().IsObjectType() {
							return Provider{}, fmt.Errorf("%s: the requirement of provider %s isn't an object of literals", f.Path, name)
						}
						p.Source, p.Version = stringAttr(v, "source"), stringAttr(v, "version")
					}
				}
			case b.Type == "provider" && len(b.Labels) == 1 && b.Labels[0] == name:
				if _, alias := b.Body.Attributes["alias"]; alias {
					continue
				}
				config, err := bodyConfig(b.Body, "default_tags", "default_labels")
				if err != nil {
					return Provider{}, fmt.Errorf("%s: provider %s: %w", f.Path, name, err)
				}
				p.Config, found = config, true
			}
		}
	}
	if !found {
		return Provider{}, fmt.Errorf("%s: no provider %s block", dir, name)
	}
	return p, nil
}

// stringAttr returns the string attribute name of the object v, or "".
func stringAttr(v cty.Value, name string) string {
	if !v.Type().HasAttribute(name) {
		return ""
	}
	a := v.GetAttr(name)
	if a.IsNull() || !a.IsKnown() || a.Type() != cty.String {
		return ""
	}
	return a.AsString()
}

// bodyConfig returns body, of literal values, as Provider.Config holds a
// provider block's arguments (see RootProvider), without the arguments
// and blocks skip names.
func bodyConfig(body *hclsyntax.Body, skip ...string) (map[string]interface{}, error) {
	config := map[string]interface{}{}
	for name, attr := range body.Attributes {
		if slices.Contains(skip, name) {
			continue
		}
		v, diags := attr.Expr.Value(nil)
		if diags.HasErrors() {
			return nil, fmt.Errorf("%s isn't a literal: %s", name, diags.Error())
		}
		config[name] = configValue(v)
	}
	for _, b := range body.Blocks {
		if slices.Contains(skip, b.Type) {
			continue
		}
		nested, err := bodyConfig(b.Body)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", b.Type, err)
		}
		switch prev := config[b.Type].(type) {
		case map[string]interface{}:
			config[b.Type] = []map[string]interface{}{prev, nested}
		case []map[string]interface{}:
			config[b.Type] = append(prev, nested)
		default:
			config[b.Type] = nested
		}
	}
	return config, nil
}

// configValue returns v as Provider.Config holds it: a string, bool or
// list of strings as a Go value, else v itself.
func configValue(v cty.Value) interface{} {
	if v.IsNull() || !v.IsWhollyKnown() {
		return v
	}
	ty := v.Type()
	switch {
	case ty == cty.String:
		return v.AsString()
	case ty == cty.Bool:
		return v.True()
	case ty.IsListType() || ty.IsTupleType() || ty.IsSetType():
		strings := []string{}
		for it := v.ElementIterator(); it.Next(); {
			_, e := it.Element()
			if e.IsNull() || e.Type() != cty.String {
				return v
			}
			strings = append(strings, e.AsString())
		}
		return strings
	}
	return v
}
