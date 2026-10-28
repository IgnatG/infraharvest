// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/IgnatG/infraharvest/internal/engine/drift"
)

// DriftRules returns the rules of the output that drift holds the edits
// accepting changes to, as generation with opts would write them: the
// standards check, with the omissions of opts, which leaves the
// organisation's own modules its adapters call to their owners, and the
// moves check, as the verification gate runs them (see FullGate); the
// values the defaults of opts name, which an edit leaves out; and the
// references to other resources that generation writes for their IDs,
// ARNs and names (see uniqueTarget).
func DriftRules(opts Options) drift.Rules {
	policy := FullGate(opts)
	return drift.Rules{
		Standards: policy.Standards,
		Moves:     policy.Moves,
		IsDefault: func(typ, path string, expr hclsyntax.Expression) bool {
			want, ok := defaultValues(opts.Defaults[typ])[path]
			return ok && matchesValue(expr, want, nil)
		},
		Referrer: func(values map[string]map[string]any) drift.Referrer {
			index := referenceIndex(values)
			return func(argument, value string) (string, hcl.Traversal, bool) {
				target, ok := uniqueTarget(argument, value, index[value])
				if !ok {
					return "", nil, false
				}
				return target.address, target.traversal(), true
			}
		},
	}
}
