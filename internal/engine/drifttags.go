// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	tfjson "github.com/hashicorp/terraform-json"
)

// providerTags are the provider settings that give every resource of the
// provider tags, as default_tags does the AWS provider's, and
// default_labels the Google provider's: the setting, whether it is a
// block holding the tags in the resources' own attribute, or an argument
// holding them; the resources' own attribute; and the attribute that
// holds a resource's own tags together with the provider's.
var providerTags = []struct {
	setting string
	block   bool
	own     string
	merged  string
}{
	{"default_tags", true, "tags", "tags_all"},
	{"default_labels", false, "labels", "terraform_labels"},
}

// sharedSource is where a root's code keeps tags, or labels, several
// resources get: a local the provider's default tags, or the resources'
// own tags, refer to, or a map literal in the provider block.
type sharedSource struct {
	// address names it in a report: local.tags, or
	// provider.aws.default_tags.
	address string
	// attribute is the resources' attribute it gives them: tags or labels.
	attribute string
	// local is the local's name, or "" for the provider's literal.
	local string
	// provider is the label of the provider block whose default tags it
	// is, setting the setting, and merged the resources' attribute that
	// holds their tags with the provider's; all "" if no provider applies
	// it.
	provider, setting, merged string
	// values are its tags.
	values map[string]string
	// refs are the root's resources whose attribute refers to the local.
	refs []tagRef
}

// tagRef is a resource whose tags refer to a local: as the local, or
// merged over it, as merge(local.tags, { Name = "x" }) does.
type tagRef struct {
	address string
	// overrides are the keys the resource sets over the local's.
	overrides map[string]bool
	// opaque says the resource merges something else over the local,
	// whose keys can't be told.
	opaque bool
}

// sharedTags is what a change of shared tags holds: the change of each
// resource it covers, as each would be accepted on its own.
type sharedTags struct {
	source  *sharedSource
	key     string
	members []*DriftChange
	// dropped says why the edit of the shared tags was left out, once it
	// is: its members are then accepted, and reported, on their own.
	dropped string
}

// sharedTagSources returns where cfg keeps tags several resources get,
// sorted by address: the locals the provider's default tags, or the
// tags of two resources or more, refer to, if they are map literals of
// strings that nothing else refers to; and the map literals of the
// providers' default tags.
func sharedTagSources(cfg *driftConfig) []*sharedSource {
	byLocal := map[string]*sharedSource{}
	unsound := map[string]bool{}
	var sources []*sharedSource
	local := func(name, attribute string) *sharedSource {
		s := byLocal[name]
		if s == nil {
			s = &sharedSource{address: "local." + name, attribute: attribute, local: name}
			byLocal[name] = s
		}
		if s.attribute != attribute {
			unsound[name] = true
		}
		return s
	}
	for _, name := range sortedKeys(cfg.files) {
		for _, b := range cfg.files[name].syntax.Blocks {
			switch {
			case b.Type == "provider" && len(b.Labels) == 1:
				_, aliased := b.Body.Attributes["alias"]
				for _, pt := range providerTags {
					expr := providerTagsExpr(b, pt.setting, pt.block, pt.own)
					if expr == nil {
						continue
					}
					if n := localName(expr); n != "" {
						s := local(n, pt.own)
						if aliased || s.provider != "" {
							// Another configuration of a provider applies it
							// too: which resources get it can't be told.
							unsound[n] = true
							continue
						}
						s.provider, s.setting, s.merged = b.Labels[0], pt.setting, pt.merged
						continue
					}
					obj, ok := expr.(*hclsyntax.ObjectConsExpr)
					if aliased || !ok {
						continue
					}
					values, ok := stringMap(obj)
					if !ok || len(values) == 0 {
						continue
					}
					sources = append(sources, &sharedSource{address: "provider." + b.Labels[0] + "." + pt.setting, attribute: pt.own, provider: b.Labels[0], setting: pt.setting, merged: pt.merged, values: values})
				}
			case b.Type == "resource" && len(b.Labels) == 2:
				for _, attribute := range []string{"tags", "labels"} {
					attr, ok := b.Body.Attributes[attribute]
					if !ok {
						continue
					}
					if n, ref := tagReference(attr.Expr); n != "" {
						ref.address = b.Labels[0] + "." + b.Labels[1]
						s := local(n, attribute)
						s.refs = append(s.refs, ref)
					}
				}
			}
		}
	}
	for _, name := range sortedKeys(byLocal) {
		s := byLocal[name]
		def, ok := cfg.locals[name]
		if unsound[name] || !ok || (s.provider == "" && len(s.refs) < 2) {
			continue
		}
		uses := len(s.refs)
		if s.provider != "" {
			uses++
		}
		if cfg.uses[name] != uses {
			// Something else refers to it, such as a module call or an
			// output: an edit of it reaches further than the resources.
			continue
		}
		obj, ok := def.attr.Expr.(*hclsyntax.ObjectConsExpr)
		if !ok {
			continue
		}
		values, ok := stringMap(obj)
		if !ok || len(values) == 0 {
			continue
		}
		s.values = values
		sources = append(sources, s)
	}
	sort.SliceStable(sources, func(i, j int) bool { return sources[i].address < sources[j].address })
	return sources
}

// providerTagsExpr returns the expression of the provider block b's
// setting: an argument, or the attribute own of a block.
func providerTagsExpr(b *hclsyntax.Block, setting string, block bool, own string) hclsyntax.Expression {
	if !block {
		if attr, ok := b.Body.Attributes[setting]; ok {
			return attr.Expr
		}
		return nil
	}
	for _, nb := range b.Body.Blocks {
		if nb.Type == setting {
			if attr, ok := nb.Body.Attributes[own]; ok {
				return attr.Expr
			}
		}
	}
	return nil
}

// localName returns N if expr is local.N, else "".
func localName(expr hclsyntax.Expression) string {
	x, ok := expr.(*hclsyntax.ScopeTraversalExpr)
	if !ok || len(x.Traversal) != 2 || x.Traversal.RootName() != "local" {
		return ""
	}
	if step, ok := x.Traversal[1].(hcl.TraverseAttr); ok {
		return step.Name
	}
	return ""
}

// tagReference returns the local a resource's tags, expr, refer to, as
// local.N or merge(local.N, ...), and how; "" for anything else.
func tagReference(expr hclsyntax.Expression) (string, tagRef) {
	if n := localName(expr); n != "" {
		return n, tagRef{overrides: map[string]bool{}}
	}
	call, ok := expr.(*hclsyntax.FunctionCallExpr)
	if !ok || call.Name != "merge" || len(call.Args) == 0 || call.ExpandFinal {
		return "", tagRef{}
	}
	n := localName(call.Args[0])
	if n == "" {
		return "", tagRef{}
	}
	ref := tagRef{overrides: map[string]bool{}}
	for _, arg := range call.Args[1:] {
		obj, ok := arg.(*hclsyntax.ObjectConsExpr)
		if !ok {
			ref.opaque = true
			continue
		}
		for _, item := range obj.Items {
			k := keyName(item.KeyExpr)
			if k == "" {
				ref.opaque = true
			}
			ref.overrides[k] = true
		}
	}
	return n, ref
}

// object returns the map literal s is in cfg, and its file.
func (s *sharedSource) object(cfg *driftConfig) (*hclsyntax.ObjectConsExpr, string, bool) {
	if s.local != "" {
		def, ok := cfg.locals[s.local]
		if !ok {
			return nil, "", false
		}
		obj, ok := def.attr.Expr.(*hclsyntax.ObjectConsExpr)
		return obj, def.file, ok
	}
	for _, name := range sortedKeys(cfg.files) {
		for _, b := range cfg.files[name].syntax.Blocks {
			if b.Type != "provider" || len(b.Labels) != 1 || b.Labels[0] != s.provider {
				continue
			}
			if _, ok := b.Body.Attributes["alias"]; ok {
				continue
			}
			for _, pt := range providerTags {
				if pt.setting != s.setting {
					continue
				}
				obj, ok := providerTagsExpr(b, pt.setting, pt.block, pt.own).(*hclsyntax.ObjectConsExpr)
				return obj, name, ok
			}
		}
	}
	return nil, "", false
}

// covers returns the resources, by address, that get the key k from s,
// and false if that can't be told: those whose tags refer to the local,
// but for those that set the key themselves; and, where the provider
// applies s, those whose state, before, has the provider's value of the
// key and not one of their own. instances are the addresses of each
// resource block's resources.
func (s *sharedSource) covers(k string, before map[string]map[string]any, instances map[string][]string) ([]string, bool) {
	covered := map[string]bool{}
	for _, ref := range s.refs {
		if ref.opaque {
			return nil, false
		}
		if ref.overrides[k] {
			continue
		}
		for _, a := range instances[ref.address] {
			covered[a] = true
		}
	}
	if s.provider != "" {
		for address, values := range before {
			merged, _ := values[s.merged].(map[string]any)
			own, _ := values[s.attribute].(map[string]any)
			if v, ok := merged[k].(string); !ok || v != s.values[k] {
				continue
			}
			if _, ok := own[k]; ok {
				continue
			}
			covered[address] = true
		}
	}
	return sortedKeys(covered), true
}

// priorValues returns each managed resource's values as the state had
// them before the refresh: the refresh-only plan's for those it found
// changed, the plan of the code's for the others, by address.
func priorValues(refresh, code *tfjson.Plan) map[string]map[string]any {
	values := map[string]map[string]any{}
	for _, p := range []*tfjson.Plan{code, refresh} {
		if p == nil {
			continue
		}
		rcs := p.ResourceChanges
		if p == refresh {
			rcs = p.ResourceDrift
		}
		for _, rc := range rcs {
			if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil {
				continue
			}
			if m, ok := rc.Change.Before.(map[string]any); ok {
				values[rc.Address] = m
			}
		}
	}
	return values
}

// shareTags folds the changes of a key of shared tags (see
// sharedTagSources) into one change of where the code keeps them, when
// every resource that gets the key from there changed it in the cloud to
// the same value, or removed it, and the next apply would revert each:
// accepting it edits the shared tags once. The change's Covers are the
// resources. Its members, the resources' own changes, are what is
// accepted instead if the plan rejects the shared edit (see unshare).
// The other changes, and those of the keys only some resources changed,
// stay as they are, each accepted at its resource. It also tells editor,
// for the resources the provider's default tags give a key, that one
// can't leave the key out.
func shareTags(changes []DriftChange, contents map[string][]byte, refresh, code *tfjson.Plan, editor *driftEditor) ([]DriftChange, error) {
	cfg, err := parseDriftConfig(contents)
	if err != nil {
		return nil, err
	}
	shared := sharedTagSources(cfg)
	if len(shared) == 0 {
		return changes, nil
	}
	before := priorValues(refresh, code)
	instances := map[string][]string{}
	for _, address := range sortedKeys(before) {
		if !strings.HasPrefix(address, "module.") {
			block := trimInstanceKey(address)
			instances[block] = append(instances[block], address)
		}
	}
	index := map[string]int{}
	for i, c := range changes {
		index[c.Address+" "+c.Attribute] = i
	}
	used := map[int]bool{}
	var folded []DriftChange
	for _, s := range shared {
		for _, k := range sortedKeys(s.values) {
			attribute := s.attribute + "." + k
			if s.provider != "" {
				for address, values := range before {
					merged, _ := values[s.merged].(map[string]any)
					own, _ := values[s.attribute].(map[string]any)
					_, owned := own[k]
					if v, ok := merged[k].(string); ok && v == s.values[k] && !owned {
						editor.providerTagged[address+" "+attribute] = fmt.Sprintf("the provider's %s, from %s, give every resource %s, and a resource can't leave one of them out: remove the key from %s if no resource should have it, else edit by hand", s.setting, s.address, k, s.address)
					}
				}
			}
			covered, ok := s.covers(k, before, instances)
			if !ok || len(covered) == 0 {
				continue
			}
			var members []int
			for _, address := range covered {
				i, ok := index[address+" "+attribute]
				if !ok || used[i] {
					members = nil
					break
				}
				c := changes[i]
				if c.Kind != DriftChanged || c.Outcome != DriftReverts || c.sensitive || (len(members) > 0 && !reflect.DeepEqual(c.value, changes[members[0]].value)) {
					members = nil
					break
				}
				members = append(members, i)
			}
			if len(members) == 0 {
				continue
			}
			first := changes[members[0]]
			c := DriftChange{
				Address: s.address, Attribute: attribute, Kind: DriftChanged, Outcome: DriftReverts,
				Before: redactDriftValue(s.values[k], false, editor.forms), After: first.After,
				Covers: covered, value: first.value,
				shared: &sharedTags{source: s, key: k},
			}
			for _, i := range members {
				used[i] = true
				m := changes[i]
				c.shared.members = append(c.shared.members, &m)
			}
			folded = append(folded, c)
		}
	}
	if len(folded) == 0 {
		return changes, nil
	}
	out := make([]DriftChange, 0, len(changes)-len(used)+len(folded))
	for i, c := range changes {
		if !used[i] {
			out = append(out, c)
		}
	}
	out = append(out, folded...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Address != out[j].Address {
			return out[i].Address < out[j].Address
		}
		return out[i].Attribute < out[j].Attribute
	})
	return out, nil
}

// locateShared finds c's key in the shared tags it is of, and the edit
// that takes the cloud's value: the key's value replaced, or the key
// removed if the cloud has none.
func (e *driftEditor) locateShared(cfg *driftConfig, c *DriftChange) Source {
	s := c.shared.source
	obj, file, ok := s.object(cfg)
	if !ok {
		return Source{Kind: SourceSharedTags, Reason: s.address + " is no longer a map literal: edit it by hand"}
	}
	w := &driftWalk{e: e, cfg: cfg, c: c, file: file, f: cfg.files[file]}
	for j := range obj.Items {
		item := &obj.Items[j]
		if keyName(item.KeyExpr) != c.shared.key {
			continue
		}
		rng := item.ValueExpr.Range()
		src := Source{File: file, Range: hcl.RangeBetween(item.KeyExpr.Range(), rng), Kind: SourceSharedTags}
		if c.value == nil {
			if src.Edit = w.removeItem(obj, item); src.Edit == nil {
				return w.manual(SourceSharedTags, src.Range, "remove the key from "+s.address+" by hand")
			}
			return src
		}
		text, err := w.valueText(c.value, nil, nil)
		if err != nil {
			return w.manual(SourceSharedTags, src.Range, "the cloud's value can't be written: edit it by hand")
		}
		src.Edit = &SourceEdit{File: file, Start: rng.Start.Byte, End: rng.End.Byte, Text: text}
		return src
	}
	return w.manual(SourceSharedTags, obj.Range(), s.address+" no longer has the key: edit it by hand")
}

// unshare leaves out the edit of c, a change of shared tags, for why,
// and returns its members, accepted as c is, to be edited each at its
// resource instead.
func unshare(c *DriftChange, why string) []*DriftChange {
	c.shared.dropped = why
	for _, m := range c.shared.members {
		m.Decision = c.Decision
	}
	return c.shared.members
}

// unsharedChanges returns changes, with each change of shared tags whose
// edit was left out replaced by its members, sorted by address and
// attribute.
func unsharedChanges(changes []DriftChange) []DriftChange {
	var out []DriftChange
	expanded := false
	for _, c := range changes {
		if c.shared == nil || c.shared.dropped == "" {
			out = append(out, c)
			continue
		}
		expanded = true
		for _, m := range c.shared.members {
			mc := *m
			if mc.Outcome == DriftWritten {
				mc.Reason = "written at the resource, as editing " + c.Address + " for every resource it covers wasn't taken: " + c.shared.dropped
			}
			out = append(out, mc)
		}
	}
	if expanded {
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Address != out[j].Address {
				return out[i].Address < out[j].Address
			}
			return out[i].Attribute < out[j].Attribute
		})
	}
	return out
}
