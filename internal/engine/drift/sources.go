// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"slices"
)

// firstString returns the first of keys whose value in m is a string
// other than "", or "".
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// maxSourceExpression is the longest expression a Source quotes.
const maxSourceExpression = 120

// addDriftSources sets, as best it can, where the root's code, contents
// by file name, sets each change's attribute (see driftEditor.resolve),
// and returns the sources it found, by change.
func addDriftSources(changes []Change, contents map[string][]byte, editor *driftEditor) ([]codeSource, error) {
	cfg, err := parseDriftConfig(contents)
	if err != nil {
		return nil, err
	}
	sources := make([]codeSource, len(changes))
	for i := range changes {
		sources[i] = editor.resolve(cfg, &changes[i])
		changes[i].Source = reportSource(cfg, sources[i], editor.forms)
		if s := changes[i].shared; s != nil {
			for _, m := range s.members {
				m.Source = reportSource(cfg, editor.resolve(cfg, m), editor.forms)
			}
		}
	}
	return sources, nil
}

// at returns the i-th element of list, or nil past its end.
func at(list []any, i int) any {
	if i < len(list) {
		return list[i]
	}
	return nil
}

// idAttributes are the attributes of a resource's state that identify it
// to its cloud's audit log, in the order stateIDs gives them.
var idAttributes = []string{"id", "arn", "self_link"}

// stateIDs returns the values of idAttributes the state value before has,
// each once: what an audit log may name the resource by.
func stateIDs(before any) []string {
	values, ok := before.(map[string]any)
	if !ok {
		return nil
	}
	var ids []string
	for _, name := range idAttributes {
		if id, ok := values[name].(string); ok && id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}
