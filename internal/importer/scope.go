// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"fmt"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report/cfn"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// stackScoped is a provider whose import scope is CloudFormation stacks
// (AWS --cfn-stack), which it tells the report of once ImportScope has
// matched them with what is listed: each stack's resources with the
// listed resources they are. The importer declares it, as the report is
// the importer's and harvest knows nothing of it.
type stackScoped interface {
	CloudFormationStacks() []cfn.Stack
}

// applyImportScope applies the scope of provider, if it has one (see
// harvest.ProviderWithImportScope), to what the call listed, in defaults
// (see selection.Sources.Scope): they no longer leave out a resource the
// scope names because CloudFormation manages it, and leave out the
// resources outside it whatever a selection file says. The scope's stacks
// (see stackScoped) go into the report, each listed resource of theirs in
// scope, the call's account and region.
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
	withStacks, ok := provider.(stackScoped)
	if !ok {
		return nil
	}
	stacks := withStacks.CloudFormationStacks()
	for i := range stacks {
		for j := range stacks[i].Resources {
			for k := range stacks[i].Resources[j].Matches {
				stacks[i].Resources[j].Matches[k].Scope = scope
			}
		}
	}
	r.report.CloudFormationStacks.Add(stacks)
	return nil
}
