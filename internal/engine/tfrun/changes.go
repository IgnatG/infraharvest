// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"
)

// Leaf is one attribute of a resource a plan changes, by its path, such as
// user[0].password; Unknown says its value is known only after apply.
type Leaf struct {
	Path    string
	Unknown bool
}

// ChangedLeaves returns the attributes that differ between before and
// after, down to nested blocks, or that will only be known after apply,
// sorted by path.
func ChangedLeaves(before, after, afterUnknown any) []Leaf {
	var leaves []Leaf
	diffLeaves("", before, after, afterUnknown, &leaves)
	sort.Slice(leaves, func(i, j int) bool { return leaves[i].Path < leaves[j].Path })
	return leaves
}

func diffLeaves(path string, before, after, unknown any, out *[]Leaf) {
	if u, ok := unknown.(bool); ok && u {
		*out = append(*out, Leaf{Path: path, Unknown: true})
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
		for k := range bm {
			keys[k] = true
		}
		for k := range am {
			keys[k] = true
		}
		for k := range um {
			keys[k] = true
		}
		for k := range keys {
			sub := k
			if path != "" {
				sub = path + "." + k
			}
			diffLeaves(sub, bm[k], am[k], um[k], out)
		}
	case (blok || before == nil) && (alok || after == nil) && (blok || alok || ulok):
		for i := 0; i < max(len(bl), len(al), len(ul)); i++ {
			diffLeaves(fmt.Sprintf("%s[%d]", path, i), at(bl, i), at(al, i), at(ul, i), out)
		}
	}
	if len(*out) == n && !reflect.DeepEqual(before, after) {
		*out = append(*out, Leaf{Path: path})
	}
}

func at(list []any, i int) any {
	if i < len(list) {
		return list[i]
	}
	return nil
}

// TopOf returns the top-level attribute of a path: user for
// user[0].password.
func TopOf(path string) string {
	top, _, _ := strings.Cut(path, ".")
	top, _, _ = strings.Cut(top, "[")
	return top
}

// TopLevel returns the top-level attributes of leaves, each once, sorted.
func TopLevel(leaves []Leaf) []string {
	var names []string
	for _, l := range leaves {
		names = append(names, TopOf(l.Path))
	}
	sort.Strings(names)
	return slices.Compact(names)
}

var indexes = regexp.MustCompile(`\[[^\]]*\]`)

// WithoutIndexes drops the indexes from a path, so that the same attribute
// matches whichever element of a set it is in: user[0].password becomes
// user.password.
func WithoutIndexes(path string) string {
	return indexes.ReplaceAllString(path, "")
}

// ChangedAttributes returns the top-level attributes that differ between
// before and after, or that will only be known after apply, sorted.
func ChangedAttributes(before, after, afterUnknown any) []string {
	return TopLevel(ChangedLeaves(before, after, afterUnknown))
}

// ChangeSignature describes a resource's planned change: its actions and
// the attributes that change.
func ChangeSignature(rc *tfjson.ResourceChange) string {
	return strings.Join(ActionNames(rc.Change.Actions), ",") + ":" + strings.Join(ChangedAttributes(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown), ",")
}

// ActionNames returns the names of actions, such as create and delete.
func ActionNames(actions tfjson.Actions) []string {
	names := make([]string, 0, len(actions))
	for _, a := range actions {
		names = append(names, string(a))
	}
	return names
}
