// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// applyImportScope applies the scope of provider, if it has one (see
// harvest.ProviderWithImportScope), to what the call listed: defaults no
// longer leave out a resource the scope names because CloudFormation
// manages it, by the stacks that list it or by its tags, as the scope is
// those stacks; the resources outside the scope are left out, and recorded
// in listed so that no selection file brings them back (see
// selectResources). The scope's stacks go into the report, each listed
// resource of theirs in scope, the call's account and region. It returns
// defaults, changed.
func (r *Run) applyImportScope(ctx context.Context, provider harvest.Provider, listed *listing, defaults map[string]string, scope string) (map[string]string, error) {
	scoped, ok := provider.(harvest.ProviderWithImportScope)
	if !ok {
		return defaults, nil
	}
	var all []harvest.Resource
	for _, resources := range listed.resources {
		all = append(all, resources...)
	}
	s, err := scoped.ImportScope(ctx, all)
	if err != nil {
		return defaults, fmt.Errorf("import scope: %w", err)
	}
	if s == nil {
		return defaults, nil
	}
	if defaults == nil {
		defaults = map[string]string{}
	}
	for key := range s.Inside {
		if report.OwnerOf(defaults[key]) == report.OwnerCloudFormation {
			delete(defaults, key)
		}
	}
	for key, reason := range s.Outside {
		defaults[key] = reason
	}
	listed.outside = s.Outside
	// Discover lists only part of what the services have, as a filter
	// does: the selection file keeps the entries it doesn't find.
	r.filtered = true
	for i := range s.Stacks {
		for j := range s.Stacks[i].Resources {
			for k := range s.Stacks[i].Resources[j].Matches {
				s.Stacks[i].Resources[j].Matches[k].Scope = scope
			}
		}
	}
	r.report.AddCloudFormationStacks(s.Stacks)
	return defaults, nil
}

// logStacks logs, for each CloudFormation stack the import is scoped to,
// whether it is safe to delete, and what keeps it from being so.
func logStacks(stacks []report.CloudFormationStack) {
	for _, s := range stacks {
		switch {
		case s.Deleted:
			log.Printf("CloudFormation stack %s: deleted already; what it retained is imported", s.Name)
		case s.SafeToDelete:
			log.Printf("CloudFormation stack %s: safe to delete once the roots are applied: every resource is imported, left out by the selection file or acknowledged, the plans change nothing, and every resource but those acknowledged is retained", s.Name)
		default:
			log.Printf("CloudFormation stack %s: not safe to delete yet:\n- %s", s.Name, strings.Join(s.Blockers, "\n- "))
			if s.RetainTemplate != "" {
				log.Printf("CloudFormation stack %s: a copy of its template with Retain added where it lacks it is in %s: update the stack with it before deleting it", s.Name, s.RetainTemplate)
			}
		}
	}
}
