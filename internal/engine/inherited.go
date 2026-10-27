// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// omitInherited leaves out of the resources in generated.tf the arguments
// of names that they set to the value their provider block in providers.tf
// sets, where the type's schema has the argument optional and computed:
// the provider then gives it that value itself, as the Google provider
// does a resource's project and region. Resources that set another value,
// or name an aliased provider, keep theirs. It reports whether it changed
// generated.tf.
func omitInherited(ctx context.Context, tf Terraform, dir string, names []string) (bool, error) {
	defaults, err := providerValues(dir, names)
	if err != nil || len(defaults) == 0 {
		return false, err
	}
	generated, err := hclfile.Load(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return false, err
	}
	type candidate struct {
		r    hclfile.Resource
		name string
	}
	var candidates []candidate
	for _, r := range generated.Resources() {
		for name, value := range defaults[resourceProvider(r)] {
			if attr, ok := r.Syntax.Body.Attributes[name]; ok {
				if v, ok := constantString(attr.Expr); ok && v == value {
					candidates = append(candidates, candidate{r, name})
				}
			}
		}
	}
	if len(candidates) == 0 {
		return false, nil
	}
	schemas, err := tf.ProvidersSchema(ctx)
	if err != nil {
		return false, fmt.Errorf("terraform providers schema: %w", err)
	}
	changed := false
	for _, c := range candidates {
		s := schemaAttribute(schemas, AddressType(c.r.Address), []string{c.name})
		if s == nil || !s.Optional || !s.Computed || s.Required {
			continue
		}
		c.r.Write.Body().RemoveAttribute(c.name)
		changed = true
	}
	if !changed {
		return false, nil
	}
	return true, generated.Save()
}

// providerValues returns, by provider name, the arguments of names that
// the provider blocks in providers.tf set to a string, but for aliased
// blocks.
func providerValues(dir string, names []string) (map[string]map[string]string, error) {
	providers, err := hclfile.Load(filepath.Join(dir, ProvidersFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	values := map[string]map[string]string{}
	for _, b := range providers.Syntax.Blocks {
		if b.Type != "provider" || len(b.Labels) != 1 {
			continue
		}
		if _, aliased := b.Body.Attributes["alias"]; aliased {
			continue
		}
		for _, name := range names {
			attr, ok := b.Body.Attributes[name]
			if !ok {
				continue
			}
			if v, ok := constantString(attr.Expr); ok {
				if values[b.Labels[0]] == nil {
					values[b.Labels[0]] = map[string]string{}
				}
				values[b.Labels[0]][name] = v
			}
		}
	}
	return values, nil
}

// resourceProvider returns the name of the provider block that configures
// r: the one its provider argument names, or the one its type starts with,
// as Terraform finds it; "" for an aliased one.
func resourceProvider(r hclfile.Resource) string {
	if attr, ok := r.Syntax.Body.Attributes["provider"]; ok {
		traversal, diags := hcl.AbsTraversalForExpr(attr.Expr)
		if diags.HasErrors() || len(traversal) != 1 {
			return ""
		}
		return traversal.RootName()
	}
	provider, _, _ := strings.Cut(AddressType(r.Address), "_")
	return provider
}
