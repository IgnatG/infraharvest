// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import "strings"

// ResourceKey is what a resource of type typ, imported by id, is told
// apart by from one run to the next, in a root, between roots and in the
// checkpoints: its type and its import ID, without the ?api-version= an
// azapi resource's ID ends with. That version is the newest stable one of
// the resource's type when it was imported, or none (see
// retryFallbacks), and changes as Azure releases new ones: the resource
// stays the same, so it keeps its root and its label, isn't added again
// by --incremental, and needs no moved block.
func ResourceKey(typ, id string) External {
	return External{Type: typ, ID: identityID(typ, id)}
}

// identityID is id without the ?api-version= query, for a type of the
// azapi provider; any other id as it is.
func identityID(typ, id string) string {
	if !strings.HasPrefix(typ, "azapi_") {
		return id
	}
	if base, _, ok := strings.Cut(id, "?api-version="); ok {
		return base
	}
	return id
}
