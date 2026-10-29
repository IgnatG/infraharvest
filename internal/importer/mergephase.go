// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"fmt"
	"log"
	"maps"
	"slices"
	"sort"

	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/managed"
	"github.com/IgnatG/infraharvest/internal/report"
)

// mergePhase merges the roots of each stack's environments once every
// scope of a command is imported (see mergePhase.run): it orchestrates
// envmerge, adopts merged roots no checkpoint records (see
// adoptedCheckpoint), works out what merged roots hand over and vacates
// them (see mergedHandovers and vacateMergedRoots), readdresses what
// they have, and keeps their merge checkpoints. It reads nothing of the
// run but what it is given, and records what it did into report and
// fail.
type mergePhase struct {
	// out is the output directory. enabled says --merge-environments, and
	// incremental, resume and regenerate the flags of those names.
	out                                      string
	enabled, incremental, resume, regenerate bool
	// members are the roots the run generated of the configuration file's
	// environments, and vacating the handovers of its scopes that plan to
	// vacate merged roots (see mergeState).
	members  []mergeMember
	vacating []*handovers
	// environments are the estate's environments, which name the tokens
	// of a member restored from its set-aside root (see restoredMembers).
	environments config.Environments
	// tools are what the phase reaches outside the process through, the
	// Terraform it plans each environment with among them (see
	// callTools.runner), and schemas the providers' schemas the run's roots
	// have read, which that Terraform shares.
	tools   *callTools
	schemas *tfrun.SchemaCache
	// state returns what --managed-state loaded (see shared.loadedState);
	// nil without it.
	state func() managed.Resources
	// report is where the phase records what it did, and fail records
	// what it couldn't do as a failure of the run.
	report *report.Report
	fail   func(error)
}

// mergePhase returns the merge phase of r, with what r's Import calls
// handed it (see mergeState).
func (r *Run) mergePhase() *mergePhase {
	options := r.options
	p := &mergePhase{
		out:          options.Out,
		enabled:      options.MergeEnvironments,
		incremental:  options.Incremental,
		resume:       options.Resume,
		regenerate:   options.Regenerate,
		members:      r.members,
		vacating:     r.vacating,
		environments: r.environments,
		tools:        r.tools,
		schemas:      &r.shared.schemas,
		report:       &r.report,
		fail:         func(err error) { r.recordFailure(options, err) },
	}
	if len(options.ManagedState) > 0 {
		p.state = r.shared.loadedState
	}
	return p
}

// run merges the roots of each stack of p's members (see mergeMember):
// the stack's roots of two environments or more become one root they
// share, in the directory stackLayout names, checked by its gate once
// per environment with that environment's credentials (see
// envmerge.MergeRoots). A stack whose roots don't merge so that every
// environment's plan stays the same keeps them, and the report says why.
// The roots of a merged stack are set aside under the output's
// checkpoints (see premergeDir). Then a merged root none of whose
// environments' roots the run generated, all of whose resources the run
// put in other roots, keeps only the removed blocks that hand them over
// (see vacateMergedRoots). It returns an error only where the run can't
// go on, such as when it is interrupted.
func (p *mergePhase) run(ctx context.Context) error {
	byStack := map[string][]mergeMember{}
	if p.enabled {
		for _, m := range p.members {
			byStack[m.stack] = append(byStack[m.stack], m)
		}
	}
	for _, stack := range slices.Sorted(maps.Keys(byStack)) {
		members := byStack[stack]
		sort.Slice(members, func(i, j int) bool {
			if members[i].env != members[j].env {
				return members[i].env < members[j].env
			}
			return members[i].dir < members[j].dir
		})
		if err := p.mergeStack(ctx, stack, members); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("%s: %v", stack, err)
			p.fail(fmt.Errorf("merging %s: %w", relativePath(p.out, stack), err))
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// The merged roots all of whose resources go to other roots now.
	if err := p.vacateMergedRoots(byStack); err != nil {
		log.Print(err)
		p.fail(fmt.Errorf("handing over what merged roots have: %w", err))
	}
	return nil
}
