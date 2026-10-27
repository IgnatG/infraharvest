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
	"github.com/IgnatG/infraharvest/internal/selection"
)

// applyImportScope applies the scope of provider, if it has one (see
// harvest.ProviderWithImportScope), to what the call listed, in defaults
// (see selection.Sources.Scope): they no longer leave out a resource the
// scope names because CloudFormation manages it, and leave out the
// resources outside it whatever a selection file says. The scope's stacks
// go into the report, each listed resource of theirs in scope, the call's
// account and region.
func (r *Run) applyImportScope(ctx context.Context, provider harvest.Provider, listed listing, defaults *selection.Sources, scope string) error {
	scoped, ok := provider.(harvest.ProviderWithImportScope)
	if !ok {
		return nil
	}
	var all []harvest.Resource
	for _, resources := range listed.resources {
		all = append(all, resources...)
	}
	s, err := scoped.ImportScope(ctx, all)
	if err != nil {
		return fmt.Errorf("import scope: %w", err)
	}
	if s == nil {
		return nil
	}
	defaults.Scope(s)
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
	return nil
}

// logStacks logs, for each CloudFormation stack the import is scoped to,
// whether it is safe to delete, and what keeps it from being so.
func logStacks(stacks []report.CloudFormationStack) {
	for _, s := range stacks {
		switch {
		case s.Deleted:
			log.Printf("CloudFormation stack %s: deleted already; what it retained is imported", s.Name)
		case s.SafeToDelete:
			log.Printf("CloudFormation stack %s: safe to delete once the roots are applied: every resource is imported, left out by the selection file or acknowledged, the plans change nothing, and every resource is retained", s.Name)
		default:
			log.Printf("CloudFormation stack %s: not safe to delete yet:\n- %s", s.Name, strings.Join(s.Blockers, "\n- "))
			if s.RetainTemplate != "" {
				log.Printf("CloudFormation stack %s: a copy of its template with Retain added where it lacks it is in %s: update the stack with it before deleting it", s.Name, s.RetainTemplate)
			}
		}
	}
}
