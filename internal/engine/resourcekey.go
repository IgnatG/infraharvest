// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// ResourceKey is what a resource of type typ, imported by id, is told
// apart by from one run to the next, in a root, between roots and in the
// checkpoints: its type and its import ID, without the part the
// catalogue says changes while the resource stays the same (see
// catalogue.UnstableIDSuffix), such as the ?api-version= an azapi
// resource's ID ends with. That version is the newest stable one of the
// resource's type when it was imported, or none (see retryFallbacks),
// and changes as Azure releases new ones: the resource stays the same, so
// it keeps its root and its label, isn't added again by --incremental,
// and needs no moved block.
func ResourceKey(typ, id string) External {
	return External{Type: typ, ID: identityID(typ, id)}
}

// identityID is id without the part that changes while the resource
// stays the same, for a type the catalogue says has one; any other id as
// it is.
func identityID(typ, id string) string {
	suffix := catalogue.ForType(typ).UnstableIDSuffix(typ)
	if suffix == "" {
		return id
	}
	if base, _, ok := strings.Cut(id, suffix); ok {
		return base
	}
	return id
}
