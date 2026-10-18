// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"strings"

	"github.com/infraharvest/infraharvest-cli/internal/engine"
	"github.com/infraharvest/infraharvest-cli/internal/harvest"
	"github.com/infraharvest/infraharvest-cli/internal/managed"
	"github.com/infraharvest/infraharvest-cli/internal/report"
)

// includeOwnedHint ends the reason a resource another tool owns is left
// out with: how to import it all the same.
const includeOwnedHint = "; --include-owned imports it"

// ownedReason is why a resource whose tags say another tool manages it is
// left out. A Terraform or OpenTofu owner counts as Terraform's in the
// report (see managed.Reason), other tools as another tool's (see
// report.OtherToolPrefix).
func ownedReason(o engine.Owner) string {
	switch {
	case o.TerraformState():
		return managed.Reason + " (" + o.Tool + " elsewhere, per its tag " + o.Tag + includeOwnedHint + ")"
	case o.Stack != "":
		return report.OtherToolPrefix + "CloudFormation stack " + o.Stack + " (per its tag " + o.Tag + includeOwnedHint + ")"
	default:
		return report.OtherToolPrefix + o.Tool + " (per its tag " + o.Tag + includeOwnedHint + ")"
	}
}

// excludeOwned adds to defaults the listed resources whose tags or labels
// say another Terraform state or tool manages them (see engine.OwnedBy),
// unless defaults leave them out already, such as by the CloudFormation
// stacks that list them: importing them would give them two owners.
func excludeOwned(listed listing, defaults map[string]string) map[string]string {
	for _, resources := range listed.resources {
		for _, res := range resources {
			key := res.Address.Type + " " + res.Observed.ID
			if _, ok := defaults[key]; ok {
				continue
			}
			owner, ok := engine.OwnedBy(listed.tagsOf(res))
			if !ok {
				continue
			}
			if defaults == nil {
				defaults = map[string]string{}
			}
			defaults[key] = ownedReason(owner)
		}
	}
	return defaults
}

// resourceTypeOf returns the type of a resource address.
func resourceTypeOf(address string) string {
	t, _, _ := strings.Cut(address, ".")
	return t
}

// keptOwned returns, as "<type> <import ID>", the selected resources whose
// listed tags say another tool manages them: the selection file or
// --include-owned kept them, so the engine keeps them too (see
// engine.Options.KeepOwned).
func keptOwned(selected map[string][]harvest.Resource, listed listing, importID func(harvest.Resource) (string, bool)) map[string]bool {
	var kept map[string]bool
	for _, resources := range selected {
		for _, res := range resources {
			if _, owned := engine.OwnedBy(listed.tagsOf(res)); !owned {
				continue
			}
			if id, ok := importID(res); ok {
				if kept == nil {
					kept = map[string]bool{}
				}
				kept[res.Address.Type+" "+id] = true
			}
		}
	}
	return kept
}
