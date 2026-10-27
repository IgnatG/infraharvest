// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import "slices"

// Blocks returns what r, of a root, imports, by type and import ID, with
// the address of the block of the root each is in: a resource's, or a
// module call's (see RemovedFrom). A result without addresses, as in a
// checkpoint an older release wrote, has only the labels Generate gave
// them.
func (r *Result) Blocks() []Managed {
	var blocks []Managed
	if len(r.Addresses) > 0 {
		for _, a := range r.Addresses {
			blocks = append(blocks, Managed{Type: a.Type, ID: a.ID, Address: RemovedFrom(a.Address)})
		}
		return blocks
	}
	for _, imp := range r.Imported {
		blocks = append(blocks, Managed{Type: imp.Type, ID: imp.ID, Address: imp.Type + "." + imp.Name})
	}
	return blocks
}

// WithKept returns what a root holds once kept, resource blocks written
// into it besides what Generate wrote, such as those of a merged root an
// environment's root generated afresh lacks, are in it too: r, with kept
// imported after its own resources, at their labels. Its addresses gain
// them only where r records addresses, as a result without any has only
// labels (see Blocks). r stays as it is.
func (r *Result) WithKept(kept []Import) *Result {
	with := *r
	with.Imported = append(slices.Clone(r.Imported), kept...)
	if len(r.Addresses) > 0 {
		with.Addresses = slices.Clone(r.Addresses)
		for _, k := range kept {
			with.Addresses = append(with.Addresses, Managed{Type: k.Type, ID: k.ID, Address: k.Type + "." + k.Name})
		}
	}
	return &with
}
