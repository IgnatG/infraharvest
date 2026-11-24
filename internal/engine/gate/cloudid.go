// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gate

import (
	"strings"

	tfjson "github.com/hashicorp/terraform-json"
)

// CloudIDs returns the canonical cloud ID of each resource the plan
// imports, by address: what the cloud itself calls the resource, which can
// differ from the ID Terraform imports it by. It is what a cloud security
// scanner names a resource by, so a finding can be traced to the address
// that manages it. It goes by the shape of the values, not by provider,
// so that the engine stays provider-neutral:
//
//   - An Amazon Resource Name in the arn attribute, in any partition
//     (arn:aws, arn:aws-cn, arn:aws-us-gov). An S3 bucket imports by its
//     name, but the cloud and scanners such as Prowler name it by ARN.
//   - An Azure Resource Manager ID in the id attribute: under a
//     subscription, or for a few resources such as management groups at
//     the tenant's provider root. Attributes named id that aren't ARM IDs,
//     such as a Key Vault secret's URL, are left out.
//
// Resources whose ID the plan doesn't know, or that have neither, are left
// out: a consumer then falls back to the import ID. It reads only values
// the gate's plan already holds, so it costs no Terraform or cloud call.
func CloudIDs(p *tfjson.Plan) map[string]string {
	ids := map[string]string{}
	if p == nil {
		return ids
	}
	for _, rc := range p.ResourceChanges {
		if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil || rc.Change.Importing == nil {
			continue
		}
		after, ok := rc.Change.After.(map[string]any)
		if !ok {
			continue
		}
		if id := cloudID(after); id != "" {
			ids[rc.Address] = id
		}
	}
	return ids
}

// cloudID returns the canonical cloud ID among the attribute values after,
// or "" if it has none this can tell.
func cloudID(after map[string]any) string {
	if arn, _ := after["arn"].(string); strings.HasPrefix(arn, "arn:") && strings.Count(arn, ":") >= 5 {
		return arn
	}
	if id, _ := after["id"].(string); strings.HasPrefix(id, "/subscriptions/") || strings.HasPrefix(id, "/providers/") {
		return id
	}
	return ""
}
