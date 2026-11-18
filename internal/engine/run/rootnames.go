// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package run

import (
	"sort"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// RootNames returns the names files use (see Names), with those that
// moved and removed blocks take resources from: a plan rejects a moved
// block from an address the configuration declares.
func RootNames(files []*hclfile.File) Names {
	names := Names{}
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			switch {
			case b.Type == "moved" || b.Type == "removed":
				if from, ok := hclfile.MovedFrom(b); ok {
					names[from] = true
				}
			case b.Type == "resource" && len(b.Labels) == 2:
				names[b.Labels[0]+"."+b.Labels[1]] = true
			case b.Type == "data" && len(b.Labels) == 2:
				names["data."+b.Labels[0]+"."+b.Labels[1]] = true
			case b.Type == "module" && len(b.Labels) == 1:
				names["module."+b.Labels[0]] = true
			case b.Type == "variable" && len(b.Labels) == 1:
				names["var."+b.Labels[0]] = true
			case b.Type == "locals":
				for name := range b.Body.Attributes {
					names["local."+name] = true
				}
			}
		}
	}
	return names
}

// SortExternal sorts list by type, then ID, then name, with a name before
// none.
func SortExternal(list []External) {
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		if (a.Name == "") != (b.Name == "") {
			return a.Name != ""
		}
		return a.Name < b.Name
	})
}
