// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/zclconf/go-cty/cty"
)

// LocalsFileName holds the values lifted out of the generated
// configuration.
const LocalsFileName = "locals.tf"

// DefaultTags describes how a provider applies tags to every resource it
// manages, such as the AWS provider's default_tags.
type DefaultTags struct {
	// Provider is the provider block's name, e.g. aws.
	Provider string
	// Attribute is the resources' tags attribute, e.g. tags.
	Attribute string
	// Block is the provider block that applies tags to every resource,
	// e.g. default_tags, with the tags in its Attribute.
	Block string
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

// liftable matches the values worth naming: identifiers and resource names
// of the clouds (vpc-0abc12de, arn:aws:..., /subscriptions/...,
// projects/...), not words and settings such as "Enabled" or "tcp".
var liftable = regexp.MustCompile(`^(arn:|/subscriptions/|projects/)|^[a-z][a-z0-9]*-[0-9a-f]{8,}$`)

// applyTagLift moves the tags every resource with tags shares into
// local.tags, which the provider applies through dt.Block, and leaves each
// resource only its other tags. AWS records each resource's full tag set
// in tags_all, so whether that changes the plan depends on the provider:
// postProcess keeps the lift only if the plan doesn't change (see verify).
// It reports whether it changed anything. With dt.Applied, it only leaves
// those tags out of the resources: the provider applies them already (see
// applyDefaultTags).
func applyTagLift(dir string, dt DefaultTags) (bool, error) {
	generatedPath := filepath.Join(dir, GeneratedFileName)
	generated, err := loadHCL(generatedPath)
	if err != nil {
		return false, err
	}
	if dt.Applied != nil {
		if !removeTags(generated, dt.Attribute, dt.Applied) {
			return false, nil
		}
		return true, generated.save()
	}
	common, tagged := sharedTags(generated, dt)
	if len(common) == 0 || tagged < 2 {
		return false, nil
	}
	providers, provider, err := loadProvider(dir, dt.Provider)
	if err != nil || provider == nil {
		return false, err
	}
	removeTags(generated, dt.Attribute, common)
	if err := generated.save(); err != nil {
		return false, err
	}
	return true, setDefaultTags(dir, providers, provider, dt, common)
}

// applyDefaultTags makes the provider in dir apply dt.Applied to every
// resource, as the root an incremental import adds to does.
func applyDefaultTags(dir string, dt DefaultTags) error {
	providers, provider, err := loadProvider(dir, dt.Provider)
	if err != nil {
		return err
	}
	if provider == nil {
		return fmt.Errorf("%s has no provider %q to apply default tags", ProvidersFileName, dt.Provider)
	}
	return setDefaultTags(dir, providers, provider, dt, dt.Applied)
}

// loadProvider loads providers.tf and finds the block of the provider
// named name, or nil.
func loadProvider(dir, name string) (*hclFile, *hclwrite.Body, error) {
	providers, err := loadHCL(filepath.Join(dir, ProvidersFileName))
	if err != nil {
		return nil, nil, err
	}
	for _, b := range providers.file.Body().Blocks() {
		if b.Type() == "provider" && len(b.Labels()) == 1 && b.Labels()[0] == name {
			return providers, b.Body(), nil
		}
	}
	return providers, nil, nil
}

// setDefaultTags makes provider, in providers, apply tags through local.tags
// (dt.Attribute) and dt.Block.
func setDefaultTags(dir string, providers *hclFile, provider *hclwrite.Body, dt DefaultTags, tags map[string]string) error {
	provider.AppendNewBlock(dt.Block, nil).Body().SetAttributeTraversal(dt.Attribute, hcl.Traversal{
		hcl.TraverseRoot{Name: "local"}, hcl.TraverseAttr{Name: dt.Attribute},
	})
	if err := providers.save(); err != nil {
		return err
	}
	return setLocals(dir, map[string]cty.Value{dt.Attribute: mapValue(tags)})
}

// removeTags leaves out of each resource's literal attribute the tags in
// remove, with the same values, and reports whether it changed anything.
func removeTags(f *hclFile, attribute string, remove map[string]string) bool {
	changed := false
	for _, r := range f.resources() {
		attr, ok := r.syntax.Body.Attributes[attribute]
		if !ok {
			continue
		}
		tags, ok := stringMap(attr.Expr)
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
			r.write.Body().RemoveAttribute(attribute)
		} else {
			r.write.Body().SetAttributeValue(attribute, mapValue(tags))
		}
	}
	return changed
}

// sharedTags returns the tags, as key and value, that every resource with
// a literal dt.Attribute has, without reserved ones, and how many such
// resources there are. It returns nothing if any resource's tags aren't a
// literal map of strings.
func sharedTags(f *hclFile, dt DefaultTags) (map[string]string, int) {
	var common map[string]string
	tagged := 0
	for _, r := range f.resources() {
		attr, ok := r.syntax.Body.Attributes[dt.Attribute]
		if !ok {
			continue
		}
		tags, ok := stringMap(attr.Expr)
		if !ok {
			return nil, 0
		}
		tagged++
		if common == nil {
			common = map[string]string{}
			for k, v := range tags {
				if dt.ReservedPrefix != "" && strings.HasPrefix(k, dt.ReservedPrefix) {
					continue
				}
				common[k] = v
			}
			continue
		}
		for k, v := range common {
			if tags[k] != v {
				delete(common, k)
			}
		}
	}
	return common, tagged
}

// stringMap evaluates a literal map of strings; null is an empty map.
func stringMap(expr hclsyntax.Expression) (map[string]string, bool) {
	v, diags := expr.Value(nil)
	if diags.HasErrors() || !v.IsWhollyKnown() {
		return nil, false
	}
	m := map[string]string{}
	if v.IsNull() {
		return m, true
	}
	if !v.Type().IsMapType() && !v.Type().IsObjectType() {
		return nil, false
	}
	for it := v.ElementIterator(); it.Next(); {
		k, val := it.Element()
		if val.IsNull() || val.Type() != cty.String {
			return nil, false
		}
		m[k.AsString()] = val.AsString()
	}
	return m, true
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

// liftLiterals moves identifier-like string values (see liftable) that
// the generated configuration repeats at least minRepeats times into
// locals named after the argument that holds them most, and makes every
// use refer to the local, named other than the locals in taken. The values
// stay the same, so the plan does too. It reports whether it lifted
// anything.
func liftLiterals(dir string, taken Names) (bool, error) {
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
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
			if diags.HasErrors() || !liftable.MatchString(v.AsString()) {
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
	collect(generated.syntax, generated.file.Body())

	locals, err := readLocals(dir)
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
		name := Label(best)
		for n := 2; hasLocal(locals, name) || taken["local."+name]; n++ {
			name = fmt.Sprintf("%s_%d", Label(best), n)
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
	if err := generated.save(); err != nil {
		return false, err
	}
	return true, writeLocals(dir, locals)
}

func hasLocal(locals map[string]cty.Value, name string) bool {
	_, ok := locals[name]
	return ok
}

// setLocals adds values to locals.tf, replacing locals of the same name.
func setLocals(dir string, values map[string]cty.Value) error {
	locals, err := readLocals(dir)
	if err != nil {
		return err
	}
	maps.Copy(locals, values)
	return writeLocals(dir, locals)
}

// readLocals returns the literal locals in locals.tf, if any.
func readLocals(dir string) (map[string]cty.Value, error) {
	locals := map[string]cty.Value{}
	f, err := loadHCL(filepath.Join(dir, LocalsFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return locals, nil
	}
	if err != nil {
		return nil, err
	}
	for _, b := range f.syntax.Blocks {
		if b.Type != "locals" {
			continue
		}
		for name, attr := range b.Body.Attributes {
			v, diags := attr.Expr.Value(nil)
			if diags.HasErrors() {
				return nil, fmt.Errorf("%s: local.%s isn't a literal", LocalsFileName, name)
			}
			locals[name] = v
		}
	}
	return locals, nil
}

// writeLocals writes locals.tf with one locals block, in name order.
func writeLocals(dir string, locals map[string]cty.Value) error {
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
	return os.WriteFile(filepath.Join(dir, LocalsFileName), hclwrite.Format(f.Bytes()), 0o644)
}

// fileBackup holds files' contents to restore; nil for files that didn't
// exist.
type fileBackup struct {
	dir   string
	files map[string][]byte
}

func backupFiles(dir string, names ...string) (*fileBackup, error) {
	b := &fileBackup{dir: dir, files: map[string][]byte{}}
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(dir, name))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			b.files[name] = nil
		case err != nil:
			return nil, err
		default:
			b.files[name] = content
		}
	}
	return b, nil
}

func (b *fileBackup) restore() error {
	for name, content := range b.files {
		path := filepath.Join(b.dir, name)
		if content == nil {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return err
		}
	}
	return nil
}
