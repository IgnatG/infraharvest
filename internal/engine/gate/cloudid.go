// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gate

import (
	"slices"
	"strings"
	"sync"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/pkg/typemap"
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
//     name, but the cloud and scanners such as Prowler name it by ARN. It
//     counts only if it is the resource's own: the arn of an event target
//     or an SNS topic policy names the function or the topic they belong
//     to, and two resources must not share one ID.
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
		if id := cloudID(rc.Type, after); id != "" {
			ids[rc.Address] = id
		}
	}
	return ids
}

// cloudID returns the canonical cloud ID among the attribute values after
// of a resource of type typ, or "" if it has none this can tell.
func cloudID(typ string, after map[string]any) string {
	if arn, _ := after["arn"].(string); strings.HasPrefix(arn, "arn:") && strings.Count(arn, ":") >= 5 && isOwnARN(typ, arn) {
		return arn
	}
	if id, _ := after["id"].(string); strings.HasPrefix(id, "/subscriptions/") || strings.HasPrefix(id, "/providers/") {
		return id
	}
	return ""
}

var (
	resolverOnce sync.Once
	resolver     *typemap.Resolver
)

// isOwnARN reports whether arn can be the ARN of a resource of type typ,
// going by the built-in catalogue, which knows how the ARNs of some types
// are made (see typemap.Resolver.FromARN). An ARN the catalogue knows the
// types of must be of typ. For one it doesn't know, typ must be a type the
// catalogue has without an ARN of its own to compare it to; a type it
// doesn't have at all may be one whose arn argument names another
// resource, as an event target's names the function it calls, so it is
// refused.
func isOwnARN(typ, arn string) bool {
	resolverOnce.Do(func() { resolver, _ = typemap.New() })
	if resolver == nil {
		return false
	}
	if types := resolver.FromARN(arn); len(types) > 0 {
		return slices.Contains(types, typ) || slices.Contains(types, resolver.Canonical(typ))
	}
	provider, _, _ := strings.Cut(typ, "_")
	cat, err := catalogue.Builtin(provider)
	if err != nil {
		return false
	}
	e, ok := cat.View(typ)
	return ok && e.ARN == nil
}
