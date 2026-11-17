// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// writeModuleProviders adds to providers.tf the blocks, and to
// versions.tf the sources, of the providers of configured that the
// trials' modules use (see adapters.Adapter.Providers), unless the root
// has them. Its version is left to the modules' constraints. The one the
// root's resource types are named after, such as google for a root of
// google-beta, also applies the root's default tags, tags (see
// shareDefaultTags).
func writeModuleProviders(ws *workspace, trials []*trial, configured map[string]rootfile.Provider, tags *DefaultTags) error {
	var names []string
	for _, t := range trials {
		for _, name := range t.adapter.Providers {
			if _, ok := configured[name]; ok && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	if err := requireProviders(ws, names, configured); err != nil {
		return err
	}
	return shareDefaultTags(ws, names, tags)
}

// shareDefaultTags makes the provider of names that tags.Provider's
// resource types are named after, such as google for google-beta, apply
// the tags the root's provider block applies through tags.Block too: a
// module creates with it the resources of those types that name no
// provider, which the tag lift left without the tags it moved into the
// provider (see applyTagLift). Another provider of names, such as
// google-beta for a root of google, applies none: modules create with it
// only resources that name it, such as the network module's network,
// which takes no labels.
func shareDefaultTags(ws *workspace, names []string, tags *DefaultTags) error {
	if tags == nil || tags.Block == "" {
		return nil
	}
	implied, _, _ := strings.Cut(tags.Provider, "-")
	if implied == tags.Provider || !slices.Contains(names, implied) {
		return nil
	}
	providers, root, err := loadProvider(ws, tags.Provider)
	if err != nil || root == nil {
		return err
	}
	var module *hclwrite.Body
	for _, b := range providers.Edit().Body().Blocks() {
		if b.Type() == "provider" && len(b.Labels()) == 1 && b.Labels()[0] == implied && b.Body().GetAttribute("alias") == nil {
			module = b.Body()
		}
	}
	if module == nil {
		return nil
	}
	if tags.Argument {
		applied := root.GetAttribute(tags.Block)
		if applied == nil || module.GetAttribute(tags.Block) != nil {
			return nil
		}
		module.SetAttributeRaw(tags.Block, applied.Expr().BuildTokens(nil))
		ws.Save(providers)
		return nil
	}
	applied := root.FirstMatchingBlock(tags.Block, nil)
	if applied == nil || module.FirstMatchingBlock(tags.Block, nil) != nil {
		return nil
	}
	copied, err := hclfile.CopyBlock(applied)
	if err != nil {
		return fmt.Errorf("provider %s: %w", implied, err)
	}
	module.AppendBlock(copied)
	ws.Save(providers)
	return nil
}

// requireProviders adds to providers.tf the blocks, and to versions.tf
// the sources, of the providers of configured that names names, unless
// the root has them: those of a module call (see writeModuleProviders)
// or of resources whose type another provider implies (see
// resourceProviders). It sets no version: pinModuleProviders pins each
// to the release init selected.
func requireProviders(ws *workspace, names []string, configured map[string]rootfile.Provider) error {
	if len(names) == 0 {
		return nil
	}
	names = slices.Sorted(slices.Values(names))
	providers, err := ws.Load(rootfile.ProvidersFileName)
	if err != nil {
		return err
	}
	versions, err := ws.Load(rootfile.VersionsFileName)
	if err != nil {
		return err
	}
	var required *hclwrite.Body
	for _, b := range versions.Edit().Body().Blocks() {
		if b.Type() != "terraform" {
			continue
		}
		for _, nested := range b.Body().Blocks() {
			if nested.Type() == "required_providers" {
				required = nested.Body()
			}
		}
	}
	if required == nil {
		return fmt.Errorf("%s has no required_providers", rootfile.VersionsFileName)
	}
	for _, name := range names {
		p := configured[name]
		if required.GetAttribute(name) == nil {
			required.SetAttributeValue(name, cty.ObjectVal(map[string]cty.Value{"source": cty.StringVal(p.Source)}))
		}
		if providers.Edit().Body().FirstMatchingBlock("provider", []string{name}) != nil {
			continue
		}
		config, _ := rootfile.WithoutCredentials(p.Config)
		providers.Edit().Body().AppendNewline()
		if err := rootfile.WriteBody(providers.Edit().Body().AppendNewBlock("provider", []string{name}).Body(), config); err != nil {
			return fmt.Errorf("provider %s: %w", name, err)
		}
	}
	ws.Save(providers)
	ws.Save(versions)
	return nil
}

// pinModuleProviders pins each provider of configured that versions.tf
// requires without a version to the major and minor version the lock file
// selected for it, as the root's own provider is pinned.
func pinModuleProviders(ws *workspace, configured map[string]rootfile.Provider) error {
	if len(configured) == 0 {
		return nil
	}
	if _, err := os.Stat(ws.path(tfrun.LockFileName)); err != nil {
		return nil //nolint:nilerr // no lock file yet, nothing to read
	}
	lock, err := hclfile.Load(ws.path(tfrun.LockFileName))
	if err != nil {
		return err
	}
	versions, err := ws.Load(rootfile.VersionsFileName)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(configured))
	for name := range configured {
		names = append(names, name)
	}
	sort.Strings(names)
	changed := false
	for _, b := range versions.Edit().Body().Blocks() {
		if b.Type() != "terraform" {
			continue
		}
		for _, required := range b.Body().Blocks() {
			if required.Type() != "required_providers" {
				continue
			}
			for _, name := range names {
				attr := required.Body().GetAttribute(name)
				if attr == nil {
					continue
				}
				requirement, ok := literalObject(attr.Expr().BuildTokens(nil))
				if !ok || requirement.Type().HasAttribute("version") {
					continue
				}
				selected := lockedVersion(lock, configured[name].Source)
				if selected == nil {
					continue
				}
				required.Body().SetAttributeValue(name, cty.ObjectVal(map[string]cty.Value{
					"source":  cty.StringVal(configured[name].Source),
					"version": cty.StringVal(rootfile.ProviderConstraint(selected)),
				}))
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	ws.Save(versions)
	return nil
}

// lockedVersion returns the version the lock file selected of the
// provider at source, such as hashicorp/google-beta, on whichever registry.
func lockedVersion(lock *hclfile.File, source string) *version.Version {
	suffix := "/" + strings.ToLower(source)
	for _, b := range lock.Syntax.Blocks {
		if b.Type != "provider" || len(b.Labels) != 1 || !strings.HasSuffix(strings.ToLower(b.Labels[0]), suffix) {
			continue
		}
		if v, err := version.NewVersion(hclfile.StringAttribute(b.Body, "version")); err == nil {
			return v
		}
	}
	return nil
}

// literalObject returns the value of an object expression without
// references, such as a provider requirement.
func literalObject(tokens hclwrite.Tokens) (cty.Value, bool) {
	expr, diags := hclsyntax.ParseExpression(tokens.Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() {
		return cty.NilVal, false
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() || !v.Type().IsObjectType() {
		return cty.NilVal, false
	}
	return v, true
}

// sharedProvider returns the provider meta-argument that the resources of
// a cluster which set one share, such as aws.without_default_tags (see
// applyTagLift), and those resources; nil if none sets one, or two set
// different ones. The call passes it to the module, so the others use it
// too: those that take no tags, such as a bucket's versioning, plan the
// same through either configuration; one the provider tags would lose
// the default tags, which the plan then rejects.
func sharedProvider(c adapters.Cluster) (hclwrite.Tokens, []adapters.Resource) {
	var shared hclwrite.Tokens
	var setting []adapters.Resource
	for _, r := range append([]adapters.Resource{c.Anchor}, c.Members...) {
		attr := r.Body.GetAttribute("provider")
		if attr == nil {
			continue
		}
		tokens := attr.Expr().BuildTokens(nil)
		if shared != nil && strings.TrimSpace(string(tokens.Bytes())) != strings.TrimSpace(string(shared.Bytes())) {
			return nil, nil
		}
		shared = tokens
		setting = append(setting, r)
	}
	return shared, setting
}

// setProvider sets the provider meta-argument of resources to provider, or
// removes it for nil.
func setProvider(resources []adapters.Resource, provider hclwrite.Tokens) {
	for _, r := range resources {
		if provider == nil {
			r.Body.RemoveAttribute("provider")
		} else {
			r.Body.SetAttributeRaw("provider", provider)
		}
	}
}
