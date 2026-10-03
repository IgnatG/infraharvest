// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var notInLabel = regexp.MustCompile(`[^a-z0-9]+`)

// Label turns a resource's name into a resource block label: snake_case,
// "r_" in front if it would start with a digit, "resource" if nothing is
// left.
func Label(name string) string {
	label := strings.Trim(notInLabel.ReplaceAllString(strings.ToLower(name), "_"), "_")
	switch {
	case label == "":
		return "resource"
	case label[0] >= '0' && label[0] <= '9':
		return "r_" + label
	}
	return label
}

// labelled returns imports with Name turned into a label (see Label), made
// unique per resource type with _2, _3, ... in import ID order, so the
// labels don't depend on the order resources were listed in.
func labelled(imports []Import) []Import {
	sorted := make([]Import, len(imports))
	for i, imp := range imports {
		imp.Name = Label(imp.Name)
		sorted[i] = imp
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	taken := map[string]bool{}
	for _, imp := range sorted {
		taken[imp.Type+"."+imp.Name] = true
	}
	used := map[string]bool{}
	for i, imp := range sorted {
		name := imp.Name
		for n := 2; used[imp.Type+"."+name]; n++ {
			// Skip labels another resource has as its own.
			if candidate := fmt.Sprintf("%s_%d", imp.Name, n); !taken[imp.Type+"."+candidate] {
				name = candidate
			}
		}
		used[imp.Type+"."+name] = true
		sorted[i].Name = name
	}
	return sorted
}
