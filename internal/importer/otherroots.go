// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"maps"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// callRoots are the roots a run's Import calls generated, with what
// each imports, by account (see scopeAccount) and directory: a later call
// of the account refers to them as other roots (see withEarlierRoots).
type callRoots map[string]map[string][]engine.Import

// scopeAccount is the provider and account, subscription or project of
// scope (see discoveryScope), without its region.
func scopeAccount(scope string) string {
	provider, rest, _ := strings.Cut(scope, "/")
	account, _, _ := strings.Cut(rest, "/")
	return provider + "/" + account
}

// recordRoot records that the call imports into scope generated the root
// dir, which imports imports. Only roots of the same account count for
// later calls: another account's resources are read with other
// credentials.
func (r *Run) recordRoot(scope, dir string, imports []engine.Import) {
	if len(imports) == 0 {
		return
	}
	account := scopeAccount(scope)
	if r.roots == nil {
		r.roots = callRoots{}
	}
	if r.roots[account] == nil {
		r.roots[account] = map[string][]engine.Import{}
	}
	r.roots[account][dir] = imports
}

// earlierRoots returns the roots earlier calls of scope's account
// generated (see recordRoot).
func (r *Run) earlierRoots(scope string) map[string][]engine.Import {
	return r.roots[scopeAccount(scope)]
}

// withEarlierRoots returns the roots one call generates, byDir, with the
// earlier calls' roots, so that each refers to the others (see
// withOtherRoots): the regional roots of an AWS account to its global root,
// say. A directory of byDir is the call's, whatever an earlier call put
// there.
func withEarlierRoots(byDir, earlier map[string][]engine.Import) map[string][]engine.Import {
	if len(earlier) == 0 {
		return byDir
	}
	all := maps.Clone(earlier)
	maps.Copy(all, byDir)
	return all
}
