// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import "strings"

// AttributeTags returns the tags a lister recorded in a resource's flattened
// attributes: those under tags. (AWS, Azure) and under labels. (Google
// Cloud), with the prefix removed once. The flattening's own entries are
// left out: the counts tags.% and tags.#, and the elements of a tag list,
// such as tags.0.key. Values are kept as they are, empty ones included.
//
// It returns nil when there are no tags, and otherwise a new map; the
// attributes are not modified.
func AttributeTags(attributes map[string]string) map[string]string {
	var tags map[string]string
	for attribute, value := range attributes {
		key, ok := tagKey(attribute)
		if !ok {
			continue
		}
		if tags == nil {
			tags = make(map[string]string)
		}
		tags[key] = value
	}
	return tags
}

// tagKey returns the tag key of a flattened attribute, and false when the
// attribute is not a tag.
func tagKey(attribute string) (string, bool) {
	key, ok := strings.CutPrefix(attribute, "tags.")
	if !ok {
		key, ok = strings.CutPrefix(attribute, "labels.")
	}
	if !ok || key == "%" || key == "#" || isListElementKey(key) {
		return "", false
	}
	return key, true
}

// isListElementKey reports whether key addresses an element of a flattened
// list, "<index>.<field>", where the index is one or more ASCII digits.
func isListElementKey(key string) bool {
	index, _, ok := strings.Cut(key, ".")
	if !ok || index == "" {
		return false
	}
	for i := 0; i < len(index); i++ {
		if index[i] < '0' || index[i] > '9' {
			return false
		}
	}
	return true
}
