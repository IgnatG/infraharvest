// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// pathStep is a step of an attribute's path: a name, of an argument, a
// nested block or a map key, or an index.
type pathStep struct {
	name    string
	index   int
	isIndex bool
}

// splitPath splits a path such as root_block_device[0].volume_size or
// tags.kubernetes.io/role into its steps, with the keys of the maps in
// values, each a value the path goes into: a map key may have dots.
func splitPath(path string, values ...any) []pathStep {
	var steps []pathStep
	rest := path
	current := values
	for rest != "" {
		if rest[0] == '[' {
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return append(steps, pathStep{name: rest})
			}
			i, err := strconv.Atoi(rest[1:end])
			if err != nil {
				return append(steps, pathStep{name: rest})
			}
			steps = append(steps, pathStep{index: i, isIndex: true})
			rest = strings.TrimPrefix(rest[end+1:], ".")
			var next []any
			for _, v := range current {
				if l, ok := v.([]any); ok && i < len(l) {
					next = append(next, l[i])
				}
			}
			current = next
			continue
		}
		key := ""
		for _, v := range current {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			for k := range m {
				if len(k) > len(key) && (rest == k || strings.HasPrefix(rest, k+".") || strings.HasPrefix(rest, k+"[")) {
					key = k
				}
			}
		}
		if key == "" {
			key = rest
			if i := strings.IndexAny(rest, ".["); i > 0 {
				key = rest[:i]
			}
		}
		steps = append(steps, pathStep{name: key})
		rest = strings.TrimPrefix(rest[len(key):], ".")
		var next []any
		for _, v := range current {
			if m, ok := v.(map[string]any); ok {
				next = append(next, m[key])
			}
		}
		current = next
	}
	return steps
}

// valueAt returns the value at steps in v, and whether v has it.
func valueAt(v any, steps []pathStep) (any, bool) {
	for _, s := range steps {
		switch {
		case s.isIndex:
			l, ok := v.([]any)
			if !ok || s.index >= len(l) {
				return nil, false
			}
			v = l[s.index]
		default:
			m, ok := v.(map[string]any)
			if !ok {
				return nil, false
			}
			if v, ok = m[s.name]; !ok {
				return nil, false
			}
		}
	}
	return v, true
}

// resolve finds where cfg sets c's attribute, and the edit that takes
// the cloud's value (see codeSource). A secret, whether the plans mark it
// sensitive, a sensitive variable sets it, or the edit would write one,
// has no edit.
func (e *driftEditor) resolve(cfg *driftConfig, c *Change) codeSource {
	src := e.locate(cfg, c)
	if reason, ok := e.providerTagged[c.Address+" "+c.Attribute]; ok && src.Edit == nil && c.value == nil && c.Kind == Changed {
		src.Reason = reason
	}
	switch {
	case c.sensitive, src.Kind == sourceSensitiveVariable:
		src.Secret = true
	case src.Edit != nil && e.writesSecret(src.Edit.Text):
		src.Secret = true
	case src.Edit != nil && hasSensitive(maskKey(e.resources[c.Address].afterSensitive, tfrun.TopOf(c.Attribute))):
		// What is written may be more than the change, such as a whole
		// list: none of the argument may be sensitive.
		src.Secret = true
	}
	if src.Secret {
		src.Edit, src.Also = nil, nil
		if src.Kind != sourceSensitiveVariable {
			src.Reason = "a secret, which is never written into the code: move it into a sensitive variable by hand, and set that from your secret store"
		}
	}
	if e.env != "" && src.Edit != nil && src.Edit.File != e.envVars && !e.ownBlock(cfg, c, src) {
		src.Edit, src.Also = nil, nil
		src.Reason = fmt.Sprintf("the environments share this root's code, so it isn't edited for %s alone: edit it by hand, for every environment", e.env)
	}
	return src
}

// hasSensitive reports whether a mask, as a plan's after_sensitive has
// it, marks anything sensitive.
func hasSensitive(mask any) bool {
	switch m := mask.(type) {
	case bool:
		return m
	case map[string]any:
		for _, sub := range m {
			if hasSensitive(sub) {
				return true
			}
		}
	case []any:
		for _, sub := range m {
			if hasSensitive(sub) {
				return true
			}
		}
	}
	return false
}

// writesSecret reports whether text, a value to write, holds a value the
// plans mark sensitive, or a credential.
func (e *driftEditor) writesSecret(text []byte) bool {
	s := string(text)
	if secret.CredentialFormat(s) != "" || secret.AWSKeyPair(s) {
		return true
	}
	for _, form := range e.forms {
		if strings.Contains(s, form) {
			return true
		}
	}
	return false
}

func (e *driftEditor) locate(cfg *driftConfig, c *Change) codeSource {
	if c.shared != nil {
		return e.locateShared(cfg, c)
	}
	if c.ModuleAddress != "" {
		return e.locateInModule(cfg, c)
	}
	address := hclfile.TrimInstanceKey(c.Address)
	b, ok := cfg.blocks[address]
	if !ok {
		return codeSource{Kind: sourceNoBlock, Reason: "the root's code has no block for it"}
	}
	w := &driftWalk{e: e, cfg: cfg, c: c, file: b.file, f: b.f, address: address, res: e.resources[c.Address]}
	if c.Kind == Deleted {
		return w.manual(sourceResource, b.b.DefRange(), "deleted in the cloud: remove the resource from the code by hand, or apply to create it again")
	}
	if reason := w.instances(b.b); reason != "" {
		return w.manual(sourceExpression, b.b.DefRange(), reason)
	}
	w.steps = splitPath(c.Attribute, e.pathValues(c.Address)...)
	if len(w.steps) == 0 || w.steps[0].isIndex {
		return w.manual(sourceExpression, b.b.DefRange(), "edit it by hand")
	}
	src := w.body(b.b, nil, 0)
	if w.each != nil && src.Edit != nil && !src.instance {
		// What the block sets but through each.value, every instance gets.
		return codeSource{File: src.File, Range: src.Range, Kind: src.Kind, Reason: "every instance of the block's for_each gets it: edit it by hand, or give each key its own value in the map for_each is over"}
	}
	return src
}

// pathValues are the values the path of a change of the resource at
// address goes into, for splitPath: the state's, the cloud's, and the
// cloud's tags and labels with the provider's, as the change has them.
func (e *driftEditor) pathValues(address string) []any {
	res := e.resources[address]
	values := []any{res.before, res.after}
	if m, ok := res.after.(map[string]any); ok {
		for from, to := range foldedAttributes {
			if v, ok := m[from]; ok {
				values = append(values, map[string]any{to: v})
			}
		}
	}
	return values
}
