// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// omitArguments removes from the resources in path the arguments omit
// names for their type, or under "*" for every type, whether written as
// attributes or as blocks. It reports whether it removed anything.
func omitArguments(path string, omit map[string][]string) (bool, error) {
	if len(omit) == 0 {
		return false, nil
	}
	return rewrite(path, func(f *hclwrite.File, _ *hclsyntax.Body) bool {
		changed := false
		for _, block := range f.Body().Blocks() {
			if block.Type() != "resource" || len(block.Labels()) != 2 {
				continue
			}
			body := block.Body()
			names := append(append([]string(nil), omit["*"]...), omit[block.Labels()[0]]...)
			for _, name := range names {
				if body.RemoveAttribute(name) != nil {
					changed = true
				}
				for _, nested := range body.Blocks() {
					if nested.Type() == name && body.RemoveBlock(nested) {
						changed = true
					}
				}
			}
		}
		return changed
	})
}
