// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// valueLeaf is one attribute that differs between two values: its path,
// as changedLeaves writes it, the name its values go by (see entryKey),
// both values, whether the second is known only after apply, and whether
// either is sensitive.
type valueLeaf struct {
	path          string
	key           string
	before, after any
	unknown       bool
	sensitive     bool
}

// valueLeaves returns the attributes that differ between before and
// after, as changedLeaves does, with their values and whether the masks
// mark them sensitive, sorted by path.
func valueLeaves(before, after, afterUnknown, beforeSensitive, afterSensitive any) []valueLeaf {
	var leaves []valueLeaf
	diffValues("", "", before, after, afterUnknown, beforeSensitive, afterSensitive, false, &leaves)
	sort.Slice(leaves, func(i, j int) bool { return leaves[i].path < leaves[j].path })
	return leaves
}

func diffValues(path, key string, before, after, unknown, bs, as any, sensitive bool, out *[]valueLeaf) {
	sensitive = sensitive || marked(bs) || marked(as)
	if u, ok := unknown.(bool); ok && u {
		*out = append(*out, valueLeaf{path: path, key: key, before: before, unknown: true, sensitive: sensitive})
		return
	}
	bm, bok := before.(map[string]any)
	am, aok := after.(map[string]any)
	um, uok := unknown.(map[string]any)
	bl, blok := before.([]any)
	al, alok := after.([]any)
	ul, ulok := unknown.([]any)
	n := len(*out)
	switch {
	case (bok || before == nil) && (aok || after == nil) && (bok || aok || uok):
		keys := map[string]bool{}
		for _, m := range []map[string]any{bm, am, um} {
			for k := range m {
				keys[k] = true
			}
		}
		for k := range keys {
			sub := k
			if path != "" {
				sub = path + "." + k
			}
			// The name the cloud's value goes by, or the state's where
			// the cloud has none or the state's names a secret.
			name := entryKey(am, k)
			if b := entryKey(bm, k); !aok || secret.IsName(b) {
				name = b
			}
			diffValues(sub, name, bm[k], am[k], um[k], maskKey(bs, k), maskKey(as, k), sensitive, out)
		}
	case (blok || before == nil) && (alok || after == nil) && (blok || alok || ulok):
		for i := 0; i < max(len(bl), len(al), len(ul)); i++ {
			diffValues(fmt.Sprintf("%s[%d]", path, i), key, at(bl, i), at(al, i), at(ul, i), maskIndex(bs, i), maskIndex(as, i), sensitive, out)
		}
	}
	// A JSON document, such as a policy, that only its formatting changed
	// is the same value (see driftSameValue).
	if len(*out) == n && !driftSameValue(before, after) {
		*out = append(*out, valueLeaf{path: path, key: key, before: before, after: after, sensitive: sensitive})
	}
}

func marked(mask any) bool {
	b, ok := mask.(bool)
	return ok && b
}

func maskKey(mask any, k string) any {
	m, _ := mask.(map[string]any)
	return m[k]
}

func maskIndex(mask any, i int) any {
	l, _ := mask.([]any)
	return at(l, i)
}

// foldedAttributes are the attributes that hold a resource's tags or
// labels together with those of the provider: their changes are reported
// as the resource's own.
var foldedAttributes = map[string]string{"tags_all": "tags", "effective_labels": "labels", "terraform_labels": "labels"}

// driftLeaves returns the leaves of a resource of type typ that count as
// drift: without the arguments stateOnly names, which the cloud doesn't
// have, and write-only arguments (*_wo), which Terraform never reads
// back; with tags_all, effective_labels and terraform_labels folded into
// tags and labels, each path once.
func driftLeaves(typ string, leaves []valueLeaf, stateOnly map[string][]string) []valueLeaf {
	skip := gate.StateOnlyOf(stateOnly, typ)
	seen := map[string]int{}
	var kept []valueLeaf
	for _, l := range leaves {
		top := tfrun.TopOf(l.path)
		if slices.Contains(skip, top) || strings.HasSuffix(top, "_wo") {
			continue
		}
		if to, ok := foldedAttributes[top]; ok {
			l.path = to + l.path[len(top):]
		}
		if i, ok := seen[l.path]; ok {
			// The resource's own tags had no such key: the value it had
			// was the provider's, which the folded attribute shows.
			if kept[i].before == nil && l.before != nil {
				kept[i].before, kept[i].key = l.before, l.key
				kept[i].sensitive = kept[i].sensitive || l.sensitive
			}
			continue
		}
		seen[l.path] = len(kept)
		kept = append(kept, l)
	}
	return kept
}

// plannedChange is what the plan of the code does to one resource.
type plannedChange struct {
	actions tfjson.Actions
	leaves  []valueLeaf
	// replacePaths are the paths that force the replacement, as the plan
	// has them.
	replacePaths []any
}

// find returns the leaf of c on path, or on a path that holds it or that
// it holds, such as tags for tags.Env.
func (c *plannedChange) find(path string) (valueLeaf, bool) {
	for _, l := range c.leaves {
		if l.path == path || within(path, l.path) || within(l.path, path) {
			return l, true
		}
	}
	return valueLeaf{}, false
}

// within reports whether path is inside parent, such as tags.Env in tags
// or ingress[0].cidr_blocks in ingress.
func within(path, parent string) bool {
	if parent == "" {
		return true
	}
	return strings.HasPrefix(path, parent) && len(path) > len(parent) && (path[len(parent)] == '.' || path[len(parent)] == '[')
}

// classifyDrift returns the changes of the refresh-only plan refresh,
// each attribute of each resource it finds changed in the cloud, as the
// plan of the code with the refreshed state, code, takes them: the code
// reverts a change it plans on the same attribute, and absorbs one it
// doesn't. Resources the code still imports are left out. With
// placeholders, a sensitive attribute the code changes may only be the
// placeholder's: it is not assessable.
func classifyDrift(refresh, code *tfjson.Plan, stateOnly map[string][]string, placeholders bool) []Change {
	if refresh == nil {
		return nil
	}
	if code == nil {
		code = &tfjson.Plan{}
	}
	planned := map[string]*plannedChange{}
	importing := map[string]bool{}
	for _, rc := range code.ResourceChanges {
		if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil {
			continue
		}
		if rc.Change.Importing != nil {
			importing[rc.Address] = true
			continue
		}
		leaves := valueLeaves(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown, rc.Change.BeforeSensitive, rc.Change.AfterSensitive)
		planned[rc.Address] = &plannedChange{actions: rc.Change.Actions, leaves: driftLeaves(rc.Type, leaves, stateOnly), replacePaths: rc.Change.ReplacePaths}
	}
	forms := driftSensitiveForms(refresh, code)
	var changes []Change
	for _, rc := range refresh.ResourceDrift {
		if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil || importing[rc.Address] {
			continue
		}
		base := Change{Address: rc.Address, Type: rc.Type, ModuleAddress: rc.ModuleAddress, IDs: stateIDs(rc.Change.Before)}
		p := planned[rc.Address]
		if rc.Change.Actions.Delete() {
			c := base
			c.Kind, c.Outcome = Deleted, Absorbed
			c.Reason = "the code no longer has the resource"
			if p != nil && (p.actions.Create() || p.actions.Replace()) {
				c.Outcome, c.Reason = Reverts, "the next apply creates the resource again"
			}
			changes = append(changes, c)
			continue
		}
		if !rc.Change.Actions.Update() {
			continue
		}
		leaves := valueLeaves(rc.Change.Before, rc.Change.After, nil, rc.Change.BeforeSensitive, rc.Change.AfterSensitive)
		for _, l := range driftLeaves(rc.Type, leaves, stateOnly) {
			c := base
			c.Kind, c.Attribute = Changed, l.path
			c.Before, c.After = redactDriftValue(l.before, l.key, l.sensitive, forms), redactDriftValue(l.after, l.key, l.sensitive, forms)
			c.value, c.sensitive = l.after, l.sensitive
			c.Outcome = Absorbed
			if p != nil {
				if cl, ok := p.find(l.path); ok {
					switch {
					case cl.unknown:
						c.Outcome, c.Reason = NotAssessable, "the code's value is known only after apply"
					case cl.sensitive && placeholders:
						c.Outcome, c.Reason = NotAssessable, "a sensitive value whose variable has no value here: set its TF_VAR_ variable to assess it"
					default:
						c.Outcome = Reverts
						if p.actions.Replace() {
							c.Reason = "the next apply replaces the resource to put the code's value back"
							c.ForcesReplacement = forcesReplacement(p.replacePaths, l.path)
						}
					}
				}
			}
			changes = append(changes, c)
		}
	}
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Address != changes[j].Address {
			return changes[i].Address < changes[j].Address
		}
		return changes[i].Attribute < changes[j].Attribute
	})
	return changes
}

// forcesReplacement reports whether the attribute at path is one of
// paths, those a plan says force a replacement, or is in one; without
// paths, a replacement is taken to be the attribute's.
func forcesReplacement(paths []any, path string) bool {
	if len(paths) == 0 {
		return true
	}
	for _, p := range paths {
		steps, ok := p.([]any)
		if !ok || len(steps) == 0 {
			continue
		}
		if first, ok := steps[0].(string); ok && first == tfrun.TopOf(path) {
			return true
		}
	}
	return false
}
