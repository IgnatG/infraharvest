// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/logging"
	"github.com/IgnatG/infraharvest/internal/report"
)

// restoredMembers returns the members of missing, environments the
// merged root in stack has that the import, whose members are present,
// doesn't, as prev, its checkpoint, records them: from their roots set
// aside when it merged, as they were then. Else it returns why they
// can't be.
func (p *mergePhase) restoredMembers(stack string, prev *mergeCheckpoint, missing []string, present []mergeMember) ([]mergeMember, string) {
	out := p.out
	var restored []mergeMember
	for _, pm := range prev.Members {
		if !slices.Contains(missing, pm.Env) {
			continue
		}
		dir := filepath.Join(out, filepath.FromSlash(pm.Path))
		if set, err := premerged(out, dir); err != nil || !set {
			return nil, fmt.Sprintf("the root of %s isn't set aside as %s", pm.Env, filepath.ToSlash(relativePath(out, premergeDir(out, dir))))
		}
		if pm.Scope == "" {
			return nil, "the merge's checkpoint doesn't record what merging " + pm.Env + " again takes"
		}
		result, err := checkpointResult(out, dir)
		if err != nil || result == nil {
			return nil, "the root of " + pm.Env + " has no checkpoint"
		}
		provider, err := rootfile.ReadProvider(premergeDir(out, dir), present[0].engineProvider.Name)
		if err != nil {
			return nil, err.Error()
		}
		restored = append(restored, mergeMember{
			dir:            dir,
			stack:          stack,
			env:            pm.Env,
			tokens:         p.environments.EnvironmentTokens(pm.Env),
			scope:          pm.Scope,
			account:        pm.Account,
			region:         pm.Region,
			group:          pm.Group,
			engineProvider: provider,
			fingerprint:    pm.Fingerprint,
			result:         result,
			premerged:      true,
			restored:       true,
		})
	}
	return restored, ""
}

// plannable reports whether one of members, the stack's now, is of an
// environment prev, its merge checkpoint, has unplanned, that the import
// has: the merged root is then planned as it, even where nothing changed.
func plannable(prev *mergeCheckpoint, members []mergeMember) bool {
	for _, m := range members {
		if !m.restored && slices.Contains(prev.Merge.Unplanned, m.env) {
			return true
		}
	}
	return false
}

// missingEnvironments returns the environments prev, a merge checkpoint,
// merged that envs lacks, sorted.
func missingEnvironments(prev *mergeCheckpoint, envs []string) []string {
	var missing []string
	for _, m := range prev.Members {
		if !slices.Contains(envs, m.Env) && !slices.Contains(missing, m.Env) {
			missing = append(missing, m.Env)
		}
	}
	sort.Strings(missing)
	return missing
}

// joiningEnvironments returns the environments of members, the stack's
// now, that prev, its merge checkpoint, didn't merge, where it merged:
// those an import brings to the merged root for the first time, sorted.
func joiningEnvironments(prev *mergeCheckpoint, members []mergeMember) []string {
	if prev == nil || !prev.Merge.Merged {
		return nil
	}
	var joining []string
	for _, m := range members {
		merged := slices.ContainsFunc(prev.Members, func(pm mergeCheckpointMember) bool { return pm.Env == m.env })
		if !merged && !slices.Contains(joining, m.env) {
			joining = append(joining, m.env)
		}
	}
	sort.Strings(joining)
	return joining
}

// withoutEnvironments returns members but those of envs, and those.
func withoutEnvironments(members []mergeMember, envs []string) (rest, of []mergeMember) {
	for _, m := range members {
		if slices.Contains(envs, m.env) {
			of = append(of, m)
		} else {
			rest = append(rest, m)
		}
	}
	return rest, of
}

// apartRoots returns the roots of members, under out, as the report
// records them.
func apartRoots(out string, members []mergeMember) []report.MergedRoot {
	roots := make([]report.MergedRoot, len(members))
	for i, m := range members {
		roots[i] = report.MergedRoot{Env: m.env, Path: relativePath(out, m.dir)}
	}
	return roots
}

// mergeWithout merges members, the roots of the stack whose merged root
// is stack, of which prev is the checkpoint, again but those of joining,
// environments new to the merged root that couldn't join it, why says:
// their roots stay where they are, of their own, and the report says why
// with the stack's merge. Where the import has nothing new of the other
// environments, the merged root stays as it was.
func (p *mergePhase) mergeWithout(ctx context.Context, stack string, prev *mergeCheckpoint, members []mergeMember, joining []string, why string) error {
	out := p.out
	rest, apart := withoutEnvironments(members, joining)
	for _, m := range apart {
		premerge := premergeDir(out, m.dir)
		if m.premerged {
			if err := copyRoot(premerge, m.dir); err != nil {
				return err
			}
		}
		if err := os.RemoveAll(premerge); err != nil {
			return err
		}
	}
	path := relativePath(out, stack)
	logging.Warnf("%s: %s keep roots of their own, as joining the merged root failed: %s", path, strings.Join(joining, ", "), why)
	var err error
	if slices.ContainsFunc(rest, func(m mergeMember) bool { return !m.restored }) {
		err = p.mergeStack(ctx, stack, rest)
	} else {
		kept := prev.Merge
		kept.Unchanged = true
		p.recordMerge(kept, rest)
	}
	// The stack's merge, if merging the rest recorded one: it is the only
	// one of the stack, as each stack merges once a run.
	p.report.SetApart(path, apartRoots(out, apart), why)
	return err
}

// keepMerged keeps the merged root in stack as prev, its checkpoint,
// records it, and sets members aside, which an import generated or added
// to but can't merge again, why says, as a failure of the run. The
// members of environments prev didn't merge keep their roots where they
// are (see joiningEnvironments).
func (p *mergePhase) keepMerged(stack string, prev *mergeCheckpoint, members []mergeMember, why string) error {
	out := p.out
	members, apart := withoutEnvironments(members, joiningEnvironments(prev, members))
	for _, m := range members {
		if !m.premerged {
			if err := setAside(out, m.dir); err != nil {
				return err
			}
		}
	}
	p.warnMergedIncomplete(stack, members, why)
	kept := prev.Merge
	kept.Unchanged = true
	kept.Reason = why
	if len(apart) > 0 {
		kept.Apart, kept.ApartReason = apartRoots(out, apart), why
	}
	p.recordMerge(kept, members)
	// The roots of members are set aside, as the merged root has them.
	p.readdressNotInCloud(kept.Path, members, prev.Imports)
	err := fmt.Errorf("%s: %s", relativePath(out, stack), why)
	logging.Errorf("%v", err)
	p.fail(err)
	return nil
}
