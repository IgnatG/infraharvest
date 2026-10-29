// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// DefaultTags describes how a provider applies tags to every resource it
// manages, such as the AWS provider's default_tags.
type DefaultTags struct {
	// Provider is the provider block's name, e.g. aws.
	Provider string
	// Attribute is the resources' tags attribute, e.g. tags.
	Attribute string
	// Block is the provider block that applies tags to every resource,
	// e.g. default_tags, with the tags in its Attribute. Empty if the
	// provider has none, as azurerm: each resource's tags then refer to
	// the local instead (see referTags).
	Block string
	// Argument says Block is an argument of the provider block that holds
	// the tags, as the Google provider's default_labels, rather than a
	// block holding them in Attribute, as the AWS provider's default_tags.
	Argument bool `json:",omitempty"`
	// ReservedPrefix starts the keys of tags the cloud sets itself, which
	// can't be applied through the provider, e.g. aws:.
	ReservedPrefix string
	// Applied, if set, are tags the provider already applies, such as in
	// the root an incremental import adds to: Generate applies them too,
	// and leaves them out of the resources instead of lifting others.
	Applied map[string]string `json:",omitempty"`
}

// minRepeats is how often a value must appear to be lifted into a local.
const minRepeats = 3

// applyTagLift moves the tags most tagged resources share (see
// commonTags) into local.tags, and makes the resources that have them,
// with these or other values, use it. With dt.Block, the provider applies
// them, and each resource keeps only its other tags and its own values of
// the shared ones, which win over the provider's. The resources that
// would get tags they don't have, such as untagged ones, use a second
// configuration of the provider, without dt.Block (see addAliasProvider):
// AWS records each resource's full tag set in tags_all, so the tags must
// not change. Without dt.Block, the resources that have the shared tags
// set local.tags, merged with their other tags (see referTags); so do
// they with dt.Block where the plan rejects the provider's (see
// tidying.tagLift). postProcess keeps the lift only if the plan doesn't
// change (see tidying.run). schemas, if set, tell which resources the
// provider tags although they set no tags. It reports whether it changed
// anything. With dt.Applied, it only leaves those tags out of the
// resources, or makes the resources refer to them: the root has them
// already (see applyDefaultTags).
func applyTagLift(ws *workspace, dt DefaultTags, schemas *tfjson.ProviderSchemas) (bool, error) {
	generated, err := ws.Load(rootfile.GeneratedFileName)
	if err != nil {
		return false, err
	}
	if dt.Applied != nil {
		if dt.Block == "" {
			if !referTags(generated, dt.Attribute, dt.Applied) {
				return false, nil
			}
			ws.Save(generated)
			return true, nil
		}
		return applyAppliedTags(ws, generated, dt, schemas)
	}
	resources, ok := taggedResources(generated, dt, schemas)
	if !ok {
		return false, nil
	}
	common := commonTags(resources, dt.ReservedPrefix)
	if len(common) == 0 {
		return false, nil
	}
	uncovered := map[string]bool{}
	for _, r := range resources {
		for k := range common {
			if _, ok := r.tags[k]; !ok {
				uncovered[r.address] = true
			}
		}
	}
	covered := len(resources) - len(uncovered)
	if covered < 2 {
		return false, nil
	}
	if dt.Block == "" {
		referTags(generated, dt.Attribute, common)
		ws.Save(generated)
		return true, setLocals(ws, map[string]cty.Value{dt.Attribute: mapValue(common)})
	}
	providers, provider, err := loadProvider(ws, dt.Provider)
	if err != nil || provider == nil {
		return false, err
	}
	if len(uncovered) > 0 {
		alias := aliasFor(dt)
		if err := addAliasProvider(providers, provider, dt, alias); err != nil {
			return false, err
		}
		for _, r := range generated.Resources() {
			if uncovered[r.Address] {
				r.Write.Body().SetAttributeTraversal("provider", hcl.Traversal{hcl.TraverseRoot{Name: dt.Provider}, hcl.TraverseAttr{Name: alias}})
			}
		}
	}
	removeTags(generated, dt.Attribute, common, uncovered)
	ws.Save(generated)
	return true, setDefaultTags(ws, providers, provider, dt, common)
}

// applyAppliedTags is applyTagLift for the root an incremental import adds
// to, whose provider applies dt.Applied through dt.Block: the resources
// that have those tags leave them out, and those that would get tags they
// don't have, such as untagged ones, keep theirs and use the second
// configuration of the provider, without dt.Block, as the root's own do
// (see addRootAlias).
func applyAppliedTags(ws *workspace, generated *hclfile.File, dt DefaultTags, schemas *tfjson.ProviderSchemas) (bool, error) {
	resources, ok := taggedResources(generated, dt, schemas)
	if !ok {
		return false, nil
	}
	uncovered := map[string]bool{}
	for _, r := range resources {
		// A value of its own wins over the provider's.
		for k := range dt.Applied {
			if _, ok := r.tags[k]; !ok {
				uncovered[r.address] = true
			}
		}
	}
	changed := removeTags(generated, dt.Attribute, dt.Applied, uncovered)
	if len(uncovered) > 0 {
		providers, provider, err := loadProvider(ws, dt.Provider)
		if err != nil {
			return false, err
		}
		if provider == nil {
			return false, fmt.Errorf("%s has no provider %q to apply default tags", rootfile.ProvidersFileName, dt.Provider)
		}
		alias := aliasFor(dt)
		if !hasAlias(providers, dt.Provider, alias) {
			if err := addAliasProvider(providers, provider, dt, alias); err != nil {
				return false, err
			}
			ws.Save(providers)
		}
		for _, r := range generated.Resources() {
			if uncovered[r.Address] {
				r.Write.Body().SetAttributeTraversal("provider", hcl.Traversal{hcl.TraverseRoot{Name: dt.Provider}, hcl.TraverseAttr{Name: alias}})
			}
		}
		changed = true
	}
	if !changed {
		return false, nil
	}
	ws.Save(generated)
	return true, nil
}

// aliasFor is the alias of the second configuration of dt's provider, for
// the resources without the tags dt.Block applies.
func aliasFor(dt DefaultTags) string {
	return "without_" + dt.Block
}

// hasAlias reports whether providers configures the provider name with
// alias.
func hasAlias(providers *hclfile.File, name, alias string) bool {
	for _, b := range providers.Syntax.Blocks {
		if b.Type != "provider" || len(b.Labels) != 1 || b.Labels[0] != name {
			continue
		}
		if attr, ok := b.Body.Attributes["alias"]; ok {
			if v, diags := attr.Expr.Value(nil); !diags.HasErrors() && v.Type() == cty.String && v.AsString() == alias {
				return true
			}
		}
	}
	return false
}

// addRootAlias adds to the providers.tf of ws's root the second
// configuration of dt's provider (see addAliasProvider) if the resources an
// incremental import staged in staging use it and root has none, and
// reports whether it did.
func addRootAlias(staging string, ws *workspace, dt DefaultTags) (bool, error) {
	if dt.Block == "" {
		return false, nil
	}
	generated, err := hclfile.Load(filepath.Join(staging, rootfile.GeneratedFileName))
	if err != nil {
		return false, err
	}
	alias := aliasFor(dt)
	used := false
	for _, r := range generated.Resources() {
		if p, ok := r.Syntax.Body.Attributes["provider"]; ok {
			traversal, diags := hcl.AbsTraversalForExpr(p.Expr)
			if !diags.HasErrors() && len(traversal) == 2 && traversal.RootName() == dt.Provider {
				if a, ok := traversal[1].(hcl.TraverseAttr); ok && a.Name == alias {
					used = true
					break
				}
			}
		}
	}
	if !used {
		return false, nil
	}
	providers, provider, err := loadProvider(ws, dt.Provider)
	if err != nil || hasAlias(providers, dt.Provider, alias) {
		return false, err
	}
	if provider == nil {
		return false, fmt.Errorf("%s has no provider %q", ws.path(rootfile.ProvidersFileName), dt.Provider)
	}
	if err := addAliasProvider(providers, provider, dt, alias); err != nil {
		return false, err
	}
	ws.Save(providers)
	return true, nil
}

// taggedResource is a resource the provider applies its tags to, with its
// literal tags: none if it sets none.
type taggedResource struct {
	address string
	tags    map[string]string
}

// taggedResources returns the resources of f that set a literal
// dt.Attribute, and, with dt.Block and schemas, those that don't although
// the provider tags them: types with dt.Attribute and the attribute that
// holds the tags the provider adds (tags_all for AWS, terraform_labels
// for Google). Resources of another provider, or another configuration of
// it, such as google-beta's, get none of the provider's tags. It returns
// false if a resource's tags aren't a literal map of strings.
func taggedResources(f *hclfile.File, dt DefaultTags, schemas *tfjson.ProviderSchemas) ([]taggedResource, bool) {
	var list []taggedResource
	for _, r := range f.Resources() {
		if p, ok := r.Syntax.Body.Attributes["provider"]; ok {
			traversal, diags := hcl.AbsTraversalForExpr(p.Expr)
			if diags.HasErrors() || len(traversal) != 1 || traversal.RootName() != dt.Provider {
				continue
			}
		}
		attr, ok := r.Syntax.Body.Attributes[dt.Attribute]
		if !ok {
			if dt.Block != "" && receivesTags(schemas, AddressType(r.Address), dt.Attribute) {
				list = append(list, taggedResource{address: r.Address, tags: map[string]string{}})
			}
			continue
		}
		tags, ok := hclfile.StringMap(attr.Expr)
		if !ok {
			return nil, false
		}
		list = append(list, taggedResource{address: r.Address, tags: tags})
	}
	return list, true
}

// receivesTags reports whether the schemas of typ have attribute and one
// that holds the tags its provider adds to it.
func receivesTags(schemas *tfjson.ProviderSchemas, typ, attribute string) bool {
	if tfrun.SchemaAttribute(schemas, typ, []string{attribute}) == nil {
		return false
	}
	for _, all := range []string{attribute + "_all", "terraform_" + attribute, "effective_" + attribute} {
		if tfrun.SchemaAttribute(schemas, typ, []string{all}) != nil {
			return true
		}
	}
	return false
}

// commonTags returns the tags, key and value, that more than half of the
// resources with tags of their own have, and at least two, without those
// whose key starts with reserved, which the cloud sets.
func commonTags(resources []taggedResource, reserved string) map[string]string {
	counts := map[[2]string]int{}
	voters := 0
	for _, r := range resources {
		own := 0
		for k, v := range r.tags {
			if reserved != "" && strings.HasPrefix(k, reserved) {
				continue
			}
			counts[[2]string{k, v}]++
			own++
		}
		if own > 0 {
			voters++
		}
	}
	common := map[string]string{}
	for kv, n := range counts {
		if n >= 2 && 2*n > voters {
			common[kv[0]] = kv[1]
		}
	}
	return common
}

// applyDefaultTags makes the provider of ws apply dt.Applied to every
// resource, as the root an incremental import adds to does; without
// dt.Block, it only defines the local the resources refer to.
func applyDefaultTags(ws *workspace, dt DefaultTags) error {
	if dt.Block == "" {
		return setLocals(ws, map[string]cty.Value{dt.Attribute: mapValue(dt.Applied)})
	}
	providers, provider, err := loadProvider(ws, dt.Provider)
	if err != nil {
		return err
	}
	if provider == nil {
		return fmt.Errorf("%s has no provider %q to apply default tags", rootfile.ProvidersFileName, dt.Provider)
	}
	return setDefaultTags(ws, providers, provider, dt, dt.Applied)
}

// loadProvider loads providers.tf and finds the default configuration of
// the provider named name, the block without an alias, or nil.
func loadProvider(ws *workspace, name string) (*hclfile.File, *hclwrite.Body, error) {
	providers, err := ws.Load(rootfile.ProvidersFileName)
	if err != nil {
		return nil, nil, err
	}
	for _, b := range providers.Edit().Body().Blocks() {
		if b.Type() == "provider" && len(b.Labels()) == 1 && b.Labels()[0] == name && b.Body().GetAttribute("alias") == nil {
			return providers, b.Body(), nil
		}
	}
	return providers, nil, nil
}

// setDefaultTags makes provider, in providers, apply tags through local.tags
// (dt.Attribute) and dt.Block.
func setDefaultTags(ws *workspace, providers *hclfile.File, provider *hclwrite.Body, dt DefaultTags, tags map[string]string) error {
	local := hcl.Traversal{hcl.TraverseRoot{Name: "local"}, hcl.TraverseAttr{Name: dt.Attribute}}
	if dt.Argument {
		provider.SetAttributeTraversal(dt.Block, local)
	} else {
		provider.AppendNewBlock(dt.Block, nil).Body().SetAttributeTraversal(dt.Attribute, local)
	}
	ws.Save(providers)
	return setLocals(ws, map[string]cty.Value{dt.Attribute: mapValue(tags)})
}

// removeTags leaves out of each resource's literal attribute the tags in
// remove, with the same values, but for the resources in skip, and reports
// whether it changed anything.
func removeTags(f *hclfile.File, attribute string, remove map[string]string, skip map[string]bool) bool {
	changed := false
	for _, r := range f.Resources() {
		attr, ok := r.Syntax.Body.Attributes[attribute]
		if !ok || skip[r.Address] {
			continue
		}
		tags, ok := hclfile.StringMap(attr.Expr)
		if !ok {
			continue
		}
		n := len(tags)
		for k, v := range remove {
			if value, ok := tags[k]; ok && value == v {
				delete(tags, k)
			}
		}
		if len(tags) == n {
			continue
		}
		changed = true
		if len(tags) == 0 {
			r.Write.Body().RemoveAttribute(attribute)
		} else {
			r.Write.Body().SetAttributeValue(attribute, mapValue(tags))
		}
	}
	return changed
}

// referTags makes each resource whose literal attribute has every key of
// shared refer to local.<attribute> for them: attribute = local.tags, or
// merge(local.tags, {...}) with its other tags and its own values of the
// shared ones, which merge puts over the local's. It reports whether it
// changed anything.
func referTags(f *hclfile.File, attribute string, shared map[string]string) bool {
	local := hclwrite.TokensForTraversal(hcl.Traversal{hcl.TraverseRoot{Name: "local"}, hcl.TraverseAttr{Name: attribute}})
	changed := false
	for _, r := range f.Resources() {
		attr, ok := r.Syntax.Body.Attributes[attribute]
		if !ok {
			continue
		}
		tags, ok := hclfile.StringMap(attr.Expr)
		if !ok || len(tags) == 0 {
			continue
		}
		rest := maps.Clone(tags)
		for k, v := range shared {
			value, ok := tags[k]
			if !ok {
				rest = nil
				break
			}
			if value == v {
				delete(rest, k)
			}
		}
		if rest == nil {
			continue
		}
		changed = true
		if len(rest) == 0 {
			r.Write.Body().SetAttributeRaw(attribute, local)
		} else {
			r.Write.Body().SetAttributeRaw(attribute, hclwrite.TokensForFunctionCall("merge", local, hclwrite.TokensForValue(mapValue(rest))))
		}
	}
	return changed
}

// addAliasProvider adds to providers a configuration of the provider
// whose block is provider, named alias, with the same arguments, but for
// dt.Block: for the resources that don't have the tags it applies.
func addAliasProvider(providers *hclfile.File, provider *hclwrite.Body, dt DefaultTags, alias string) error {
	block := hclwrite.NewBlock("provider", []string{dt.Provider})
	body := block.Body()
	body.SetAttributeValue("alias", cty.StringVal(alias))
	attributes := provider.Attributes()
	for _, name := range slices.Sorted(maps.Keys(attributes)) {
		if name == "alias" || (dt.Argument && name == dt.Block) {
			continue
		}
		body.SetAttributeRaw(name, attributes[name].Expr().BuildTokens(nil))
	}
	for _, b := range provider.Blocks() {
		if !dt.Argument && b.Type() == dt.Block {
			continue
		}
		copied, err := hclfile.CopyBlock(b)
		if err != nil {
			return fmt.Errorf("provider %s: %w", dt.Provider, err)
		}
		body.AppendBlock(copied)
	}
	file := providers.Edit().Body()
	file.AppendNewline()
	file.AppendUnstructuredTokens(hclwrite.Tokens{{Type: hclsyntax.TokenComment, Bytes: []byte(fmt.Sprintf("# For the resources without the tags that %s applies to the others.\n", dt.Block))}})
	file.AppendBlock(block)
	return nil
}

func mapValue(m map[string]string) cty.Value {
	if len(m) == 0 {
		return cty.MapValEmpty(cty.String)
	}
	vals := make(map[string]cty.Value, len(m))
	for k, v := range m {
		vals[k] = cty.StringVal(v)
	}
	return cty.MapVal(vals)
}

// liftLiterals moves identifier-like string values (see Traits.liftable),
// but the well-known IDs of t (see Traits.WellKnownIDs), that the
// generated configuration repeats at least minRepeats times into locals
// named after the argument that holds them most, and makes every use
// refer to the local, named other than the locals in taken. The values
// stay the same, so the plan does too. It reports whether it lifted
// anything.
func liftLiterals(ws *workspace, taken Names, t Traits) (bool, error) {
	generated, err := ws.Load(rootfile.GeneratedFileName)
	if err != nil {
		return false, err
	}
	type use struct {
		attr string
		body *hclwrite.Body
	}
	uses := map[string][]use{}
	var order []string // values in file order, for stable names
	var collect func(syntax *hclsyntax.Body, body *hclwrite.Body)
	collect = func(syntax *hclsyntax.Body, body *hclwrite.Body) {
		names := make([]string, 0, len(syntax.Attributes))
		for name := range syntax.Attributes {
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool {
			return syntax.Attributes[names[i]].SrcRange.Start.Byte < syntax.Attributes[names[j]].SrcRange.Start.Byte
		})
		for _, name := range names {
			tmpl, ok := syntax.Attributes[name].Expr.(*hclsyntax.TemplateExpr)
			if !ok || !tmpl.IsStringLiteral() {
				continue
			}
			v, diags := tmpl.Value(nil)
			if diags.HasErrors() || !t.liftable(v.AsString()) || t.wellKnown(v.AsString()) {
				continue
			}
			if _, seen := uses[v.AsString()]; !seen {
				order = append(order, v.AsString())
			}
			uses[v.AsString()] = append(uses[v.AsString()], use{name, body})
		}
		blocks := body.Blocks()
		for i, b := range syntax.Blocks {
			if i < len(blocks) {
				collect(b.Body, blocks[i].Body())
			}
		}
	}
	collect(generated.Syntax, generated.Edit().Body())

	locals, err := readLocals(ws)
	if err != nil {
		return false, err
	}
	lifted := false
	for _, value := range order {
		if len(uses[value]) < minRepeats {
			continue
		}
		counts := map[string]int{}
		best := ""
		for _, u := range uses[value] {
			counts[u.attr]++
			if counts[u.attr] > counts[best] || (counts[u.attr] == counts[best] && u.attr < best) {
				best = u.attr
			}
		}
		name := hclfile.Label(best)
		for n := 2; hasLocal(locals, name) || taken["local."+name]; n++ {
			name = fmt.Sprintf("%s_%d", hclfile.Label(best), n)
		}
		locals[name] = cty.StringVal(value)
		for _, u := range uses[value] {
			u.body.SetAttributeTraversal(u.attr, hcl.Traversal{hcl.TraverseRoot{Name: "local"}, hcl.TraverseAttr{Name: name}})
		}
		lifted = true
	}
	if !lifted {
		return false, nil
	}
	ws.Save(generated)
	writeLocals(ws, locals)
	return true, nil
}

func hasLocal(locals map[string]cty.Value, name string) bool {
	_, ok := locals[name]
	return ok
}

// setLocals adds values to locals.tf, replacing locals of the same name.
func setLocals(ws *workspace, values map[string]cty.Value) error {
	locals, err := readLocals(ws)
	if err != nil {
		return err
	}
	maps.Copy(locals, values)
	writeLocals(ws, locals)
	return nil
}

// readLocals returns the literal locals in locals.tf, if any.
func readLocals(ws *workspace) (map[string]cty.Value, error) {
	locals := map[string]cty.Value{}
	f, err := ws.Load(rootfile.LocalsFileName)
	if errors.Is(err, fs.ErrNotExist) {
		return locals, nil
	}
	if err != nil {
		return nil, err
	}
	for _, b := range f.Syntax.Blocks {
		if b.Type != "locals" {
			continue
		}
		for name, attr := range b.Body.Attributes {
			v, diags := attr.Expr.Value(nil)
			if diags.HasErrors() {
				return nil, fmt.Errorf("%s: local.%s isn't a literal", rootfile.LocalsFileName, name)
			}
			locals[name] = v
		}
	}
	return locals, nil
}

// writeLocals writes locals.tf with one locals block, in name order.
func writeLocals(ws *workspace, locals map[string]cty.Value) {
	names := make([]string, 0, len(locals))
	for name := range locals {
		names = append(names, name)
	}
	sort.Strings(names)
	f := hclwrite.NewEmptyFile()
	body := f.Body().AppendNewBlock("locals", nil).Body()
	for _, name := range names {
		body.SetAttributeValue(name, locals[name])
	}
	ws.Write(rootfile.LocalsFileName, hclwrite.Format(f.Bytes()))
}
