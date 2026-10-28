// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"slices"

	"github.com/IgnatG/infraharvest/internal/engine/secret"
)

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

// redactResult replaces the values of secrets in the text of result that
// came from plans, which WriteReadme and the report show: the gate's
// details, why module calls were declined, the errors of what was left out
// and the warnings. Plans redact their own output already (see
// secret.RedactingTerraform); this also covers text made before a value was
// hidden.
func redactResult(result *Result, secrets []secret.Variable) {
	forms := secret.TextForms(secret.KnownValues(secrets))
	if len(forms) == 0 {
		return
	}
	for i := range result.Gate {
		for j, d := range result.Gate[i].Details {
			result.Gate[i].Details[j] = secret.RedactText(d, forms)
		}
	}
	for i := range result.Modules {
		result.Modules[i].Declined = secret.RedactText(result.Modules[i].Declined, forms)
	}
	for i := range result.Rejected {
		for j, e := range result.Rejected[i].Errors {
			result.Rejected[i].Errors[j] = secret.RedactText(e, forms)
		}
	}
	for i, w := range result.Warnings {
		result.Warnings[i] = secret.RedactText(w, forms)
	}
}
