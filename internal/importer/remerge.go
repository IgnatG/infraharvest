// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/report"
)

// setAside moves the root in dir, under out, to where its stack's merge
// sets it aside (see premergeDir), as it is now.
func setAside(out, dir string) error {
	if err := copyRoot(dir, premergeDir(out, dir)); err != nil {
		return err
	}
	return removeRoot(out, dir)
}

// recordAdded records, for the merge phase, the root in dir, which its
// stack merged before and an incremental import added to, with added,
// its result, nil where nothing was new: from fingerprint fp, holding
// holds (the earlier result where nothing was added), with opts. What it
// added is kept apart, by the addresses the root gave it.
func (c *listedCall) recordAdded(dir, fp string, holds *engine.Result, opts engine.Options, added *engine.Result) {
	if holds == nil {
		var err error
		if holds, err = checkpointResult(c.options.Out, dir); err != nil || holds == nil {
			log.Printf("%s: its checkpoint can't be read (%v); its stack isn't merged again", dir, err)
			return
		}
	}
	var isolated []string
	if added != nil {
		for _, imp := range added.Imported {
			isolated = append(isolated, imp.Type+"."+imp.Name)
		}
	}
	c.recordMemberWith(dir, fp, holds, opts, isolated)
}

// mergeStack merges members, the roots of the stack whose merged root is
// stack, of two environments or more, recording the outcome in the
// report: into the merged root, checked once per environment (see
// engine.MergeRoots). A stack whose members are as they were when it
// merged, as with --resume, keeps its merged root, or its separate
// roots, without planning again. A stack
// merged before is merged again from its members' set-aside roots, with
// what an incremental import added to them kept apart (see
// mergeMember.isolated); where that fails, or would give what the merged
// root has other addresses, the merged root stays as it was, and so do
// the set-aside roots, which the failure names. An environment the merged
// root doesn't have yet, which the import brings, joins it, its resources
// matched with the others'; where that fails, or would give what the
// merged root has other addresses, its roots stay apart, and the stack
// merges again without it (see mergeWithout).
func (r *Run) mergeStack(ctx context.Context, stack string, members []mergeMember) (err error) {
	out := r.options.Out
	var envs []string
	for _, m := range members {
		if !slices.Contains(envs, m.env) {
			envs = append(envs, m.env)
		}
	}
	prev, err := readMergeCheckpoint(out, stack)
	if err != nil {
		return err
	}
	// A merged root no checkpoint records, as in a fresh checkout of the
	// output, merges again from the roots the import generated afresh,
	// with what it has told from its files (see adoptedCheckpoint); a
	// plain import refuses it (see mergeRefusal).
	adopted := false
	if prev == nil && (r.options.Incremental || r.options.Resume || r.options.Regenerate) {
		c, why, err := r.adoptedCheckpoint(stack, members)
		if err != nil {
			return err
		}
		if why != "" {
			return r.keepMerged(stack, c, members, why)
		}
		prev, adopted = c, c != nil
		if adopted && r.options.Incremental {
			// What the merged root doesn't have yet is kept apart, as an
			// incremental import adds it to a kept root.
			for i, m := range members {
				if ids, ok := prev.Imports[m.env]; ok {
					members[i].isolated = newSinceMerge(m.result, ids)
				}
			}
			// What it has that is no longer in the cloud, which the roots
			// generated afresh lack, it keeps as it was.
			why, err := r.keepGone(stack, prev, members)
			if err != nil {
				return err
			}
			if why != "" {
				return r.keepMerged(stack, prev, members, why)
			}
		}
	}
	joining := joiningEnvironments(prev, members)
	if prev != nil && prev.Merge.Merged {
		if missing := missingEnvironments(prev, envs); len(missing) > 0 {
			// The environments the import doesn't have merge again as
			// their roots were set aside, unplanned.
			restored, why := r.restoredMembers(stack, prev, missing, members)
			if why != "" {
				return r.keepMerged(stack, prev, members, fmt.Sprintf("the import has %s, not %s, which the merged root has too, and %s: it stays as it was, and what is new waits in the roots set aside under %s until an import has every environment", strings.Join(envs, ", "), strings.Join(missing, ", "), why, filepath.ToSlash(filepath.Join(CheckpointDir, premergeDirName))))
			}
			members = append(slices.Clone(members), restored...)
			sort.Slice(members, func(i, j int) bool {
				if members[i].env != members[j].env {
					return members[i].env < members[j].env
				}
				return members[i].dir < members[j].dir
			})
			envs = append(envs, missing...)
			sort.Strings(envs)
		}
	}
	if len(envs) < 2 {
		return nil
	}
	merge := report.Merge{Path: relativePath(out, stack), Environments: envs}
	for _, m := range members {
		merge.Roots = append(merge.Roots, report.MergedRoot{Env: m.env, Path: relativePath(out, m.dir)})
	}
	if reason := mergeRefusal(out, stack, members, adopted); reason != "" {
		merge.Reason = reason
		r.recordMerge(merge, members)
		return nil
	}
	fp, err := mergeFingerprint(out, members)
	if err != nil {
		return err
	}
	if prev != nil && prev.Fingerprint == fp && keptAsMerged(stack, prev, members) && !plannable(prev, members) {
		log.Printf("%s is unchanged since it was merged", merge.Path)
		kept := prev.Merge
		kept.Unchanged = true
		r.recordMerge(kept, members)
		r.readdressNotInCloud(kept.Path, members, prev.Imports)
		return nil
	}
	if r.options.Incremental && prev != nil && prev.Merge.Merged && keptAsMerged(stack, prev, members) && !plannable(prev, members) && !slices.ContainsFunc(members, func(m mergeMember) bool { return len(m.isolated) > 0 }) {
		// An incremental import added nothing to the roots set aside,
		// which are as they were when the stack merged, though what it
		// listed has changed, such as resources gone from the cloud,
		// whose import a plan of the merged root would fail on.
		log.Printf("%s: nothing new to merge (--incremental)", merge.Path)
		kept := prev.Merge
		kept.Unchanged = true
		r.recordMerge(kept, members)
		r.readdressNotInCloud(kept.Path, members, prev.Imports)
		return nil
	}

	// What the merged root of a stack merged before had, which comes back
	// on any error until the stack either keeps it (see restore) or its
	// merged root is written for good.
	var backup *treeBackup
	defer func() {
		if err != nil {
			err = errors.Join(err, backup.restore())
		}
	}()
	// restore writes back what the merged root had, once only.
	restore := func() error {
		b := backup
		backup = nil
		return b.restore()
	}
	// The moved and removed blocks of its moves.tf, while it is there:
	// those of an earlier merge with --regenerate, not yet applied and
	// deleted.
	var kept []engine.Move
	var keptRemoved []engine.Removal
	if prev != nil && prev.Merge.Merged {
		// Merged again: what it had goes, and comes back if this merge
		// fails.
		b, err := backupTree(stack)
		if err != nil {
			return err
		}
		backup = b
		if kept, err = engine.ReadMoves(stack); err != nil {
			return err
		}
		if keptRemoved, err = engine.ReadRemoved(stack); err != nil {
			return err
		}
		for _, name := range prev.Files {
			if err := os.Remove(filepath.Join(stack, filepath.FromSlash(name))); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	log.Printf("merging the roots of %s into %s", strings.Join(envs, ", "), merge.Path)
	roots := make([]engine.MergeRoot, len(members))
	for i, m := range members {
		premerge := premergeDir(out, m.dir)
		if !m.premerged {
			if err := copyRoot(m.dir, premerge); err != nil {
				return err
			}
		}
		var runner func(string) (engine.Terraform, error)
		if !m.restored {
			tr := terraformRun{execPath: m.execPath, pluginCacheDir: filepath.Join(m.cacheDir, "plugins")}
			if tr.env, err = terraformEnv(ctx, m.provider); err != nil {
				return err
			}
			tr.freshEnv = freshTerraformEnv(m.provider)
			runner = tr.in
		}
		roots[i] = engine.MergeRoot{
			Env:       m.env,
			Tokens:    m.tokens,
			Scope:     m.account,
			Region:    m.region,
			Group:     m.group,
			Dir:       premerge,
			Origin:    m.dir,
			Provider:  m.engineProvider,
			Secrets:   m.result.Secrets,
			Terraform: runner,
			Isolated:  m.isolated,
			Labels:    m.labels,
			Gone:      r.goneFrom(m.dir),
			Arrivals:  m.result.Arrived,
		}
	}
	// A restored member has no engine options or profile of its own.
	imported := members[slices.IndexFunc(members, func(m mergeMember) bool { return !m.restored })]
	opts := imported.opts
	mergeOptions := engine.MergeOptions{Profile: imported.profile, Omit: opts.Omit, StateOnly: opts.StateOnly, Scanners: opts.Scanners, Adapters: opts.Adapters, CommandTimeout: opts.CommandTimeout}
	result, err := r.options.tools.mergeRoots(ctx, stack, roots, mergeOptions)
	// Nothing says which of the resources of a stack merged before an
	// earlier merge kept apart, as what an incremental import added (see
	// mergeMember.isolated), nor the labels it gave them, which came from
	// their roots then, and differ from those of the roots generated since,
	// or told from the merged root's files: what would get another
	// address is kept apart, with the label the merged root has for it,
	// and the stack merges again, as many times as that takes, up to
	// maxKeptApart. With --regenerate, moved blocks follow it instead.
	// What the merged root matched across environments isn't kept apart:
	// it would get another address all the same.
	var shared map[string]bool
	if prev != nil {
		shared = sharedAddresses(prev.Imports)
	}
	for attempt := 0; err == nil && prev != nil && prev.Merge.Merged && !r.options.Regenerate && result.Reason == "" && attempt < maxKeptApart; attempt++ {
		now := map[string]map[string]string{}
		for _, env := range result.Environments {
			now[env.Env] = env.Imports
		}
		again := false
		for i, m := range members {
			if more := movedApart(m, prev.Imports[m.env], now[m.env], shared); len(more) > 0 {
				if members[i].labels == nil {
					members[i].labels = map[string]string{}
				}
				for _, address := range slices.Sorted(maps.Keys(more)) {
					members[i].isolated = append(members[i].isolated, address)
					members[i].labels[address] = more[address]
				}
				roots[i].Isolated, roots[i].Labels = members[i].isolated, members[i].labels
				again = true
			}
		}
		if !again {
			break
		}
		log.Printf("%s: merging again, with what would get another address kept apart as its environment's", merge.Path)
		if err := clearMerged(stack, result.Files); err != nil {
			return err
		}
		result, err = r.options.tools.mergeRoots(ctx, stack, roots, mergeOptions)
	}
	if err != nil {
		r.warnMergedIncomplete(stack, members, err.Error())
		return err
	}
	imports := map[string]map[string]string{}
	for _, env := range result.Environments {
		imports[env.Env] = env.Imports
	}
	// What the merged root hands over to other roots (see
	// planMergedHandovers), and the handovers it can't finish.
	var removed []engine.Removal
	var incomplete []report.IncompleteHandover
	if result.Reason == "" && prev != nil && prev.Merge.Merged {
		if gone := leftOutOfMerge(prev.Imports, imports); adopted && len(gone) > 0 {
			// The roots generated afresh have only what this run lists.
			result.Reason = "merging it again from what this run lists would leave out what the merged root has that this run doesn't list, but can't tell is no longer in the cloud (not listed in full with these services, filters and layout, or in another root): " + strings.Join(gone, "; ")
		} else {
			removed, incomplete, result.Reason = mergedHandovers(out, stack, members, imports)
			if result.Reason == "" {
				if result.Reason, err = r.moveMerged(stack, result, prev.Imports, imports, kept, append(removed, stillRemoved(keptRemoved, imports, removed)...)); err != nil {
					return err
				}
			}
		}
		if result.Reason != "" {
			if err := clearMerged(stack, result.Files); err != nil {
				return err
			}
		}
	}
	fillMerge(&merge, result)
	if !merge.Merged && backup != nil && len(joining) > 0 {
		// The merged root an earlier import wrote stays as it was for
		// what it has, and the environments new to it keep their roots.
		if err := restore(); err != nil {
			return err
		}
		return r.mergeWithout(ctx, stack, prev, members, joining, merge.Reason)
	}
	if !merge.Merged && backup != nil {
		// The merged root an earlier import wrote, which may be in use,
		// stays; so do the roots set aside, with what is new in them, which
		// keepMerged sets aside.
		if err := restore(); err != nil {
			return err
		}
		return r.keepMerged(stack, prev, members, "merging it again failed, so it stays as it was, and what is new waits in the roots set aside under "+filepath.ToSlash(filepath.Join(CheckpointDir, premergeDirName))+": "+merge.Reason)
	}
	if !merge.Merged {
		log.Printf("%s: the environments keep their roots: %s", merge.Path, merge.Reason)
		for _, m := range members {
			if err := os.RemoveAll(premergeDir(out, m.dir)); err != nil {
				return err
			}
		}
	} else {
		// The merged root is written for good: what it had doesn't come
		// back over it.
		backup = nil
		log.Printf("%s: %s share one root; their own roots are kept under %s", merge.Path, strings.Join(envs, ", "), filepath.Join(out, CheckpointDir, premergeDirName))
		if prev != nil && prev.Merge.Merged {
			r.report.AddMergedMoves(merge.Path, prev.Imports, imports, removed)
		}
		for _, i := range incomplete {
			r.recordIncomplete(i)
		}
		for _, m := range members {
			if err := removeRoot(out, m.dir); err != nil {
				return err
			}
		}
	}
	r.recordMerge(merge, members)
	if merge.Merged {
		if gone := r.readdressNotInCloud(merge.Path, members, imports); len(gone) > 0 {
			if err := r.options.tools.writeMergedNotInCloud(stack, gone); err != nil {
				return err
			}
		}
	}
	return writeMergeCheckpoint(out, stack, mergeCheckpoint{Fingerprint: fp, Members: checkpointMembers(out, members), Merge: merge, Files: result.Files, Imports: imports})
}

// restoredMembers returns the members of missing, environments the
// merged root in stack has that the import, whose members are present,
// doesn't, as prev, its checkpoint, records them: from their roots set
// aside when it merged, as they were then. Else it returns why they
// can't be.
func (r *Run) restoredMembers(stack string, prev *mergeCheckpoint, missing []string, present []mergeMember) ([]mergeMember, string) {
	out := r.options.Out
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
		provider, err := engine.RootProvider(premergeDir(out, dir), present[0].engineProvider.Name)
		if err != nil {
			return nil, err.Error()
		}
		restored = append(restored, mergeMember{
			dir:            dir,
			stack:          stack,
			env:            pm.Env,
			tokens:         r.environments.EnvironmentTokens(pm.Env),
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
func (r *Run) mergeWithout(ctx context.Context, stack string, prev *mergeCheckpoint, members []mergeMember, joining []string, why string) error {
	out := r.options.Out
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
	log.Printf("%s: %s keep roots of their own, as joining the merged root failed: %s", path, strings.Join(joining, ", "), why)
	var err error
	if slices.ContainsFunc(rest, func(m mergeMember) bool { return !m.restored }) {
		err = r.mergeStack(ctx, stack, rest)
	} else {
		kept := prev.Merge
		kept.Unchanged = true
		r.recordMerge(kept, rest)
	}
	// The stack's merge, if merging the rest recorded one: it is the only
	// one of the stack, as each stack merges once a run.
	r.report.SetApart(path, apartRoots(out, apart), why)
	return err
}

// keepMerged keeps the merged root in stack as prev, its checkpoint,
// records it, and sets members aside, which an import generated or added
// to but can't merge again, why says, as a failure of the run. The
// members of environments prev didn't merge keep their roots where they
// are (see joiningEnvironments).
func (r *Run) keepMerged(stack string, prev *mergeCheckpoint, members []mergeMember, why string) error {
	out := r.options.Out
	members, apart := withoutEnvironments(members, joiningEnvironments(prev, members))
	for _, m := range members {
		if !m.premerged {
			if err := setAside(out, m.dir); err != nil {
				return err
			}
		}
	}
	r.warnMergedIncomplete(stack, members, why)
	kept := prev.Merge
	kept.Unchanged = true
	kept.Reason = why
	if len(apart) > 0 {
		kept.Apart, kept.ApartReason = apartRoots(out, apart), why
	}
	r.recordMerge(kept, members)
	// The roots of members are set aside, as the merged root has them.
	r.readdressNotInCloud(kept.Path, members, prev.Imports)
	err := fmt.Errorf("%s: %s", relativePath(out, stack), why)
	log.Print(err)
	r.recordFailure(r.options, err)
	return nil
}

// keptAsMerged reports whether the merged root in stack, of which prev is
// the checkpoint, stands as prev has it for members: merged, with its
// files but moves.tf, and members set aside; or kept apart, with
// members' roots where they are.
func keptAsMerged(stack string, prev *mergeCheckpoint, members []mergeMember) bool {
	for _, m := range members {
		if m.premerged != prev.Merge.Merged {
			return false
		}
	}
	if !prev.Merge.Merged {
		return true
	}
	for _, name := range prev.Files {
		if name == engine.MovesFileName {
			continue // deleted once applied, as the README says
		}
		if _, err := os.Stat(filepath.Join(stack, filepath.FromSlash(name))); err != nil {
			return false
		}
	}
	return true
}

// readdressed returns, of before, the import IDs of each environment's
// resources in a merged root by address, those now, merged again, has
// at other addresses, as "<env>: <before> is now <after>", sorted.
func readdressed(before, now map[string]map[string]string) []string {
	var moved []string
	for env, ids := range before {
		at := map[string]string{}
		for address, id := range now[env] {
			at[id] = address
		}
		for address, id := range ids {
			if to, ok := at[id]; ok && to != address {
				moved = append(moved, fmt.Sprintf("%s: %s is now %s", env, address, to))
			}
		}
	}
	sort.Strings(moved)
	return moved
}

// mergedMoves returns the moved blocks of a merged root merged again
// with --regenerate: kept, those of its moves.tf from before, then one
// from each address before, the import IDs of each environment's
// resources in it by address, has that now has at another, from there to
// that one. It returns why there can't be, where there can't: an address
// that would move to two, or one in a module call, which moved blocks
// can't take out of a module package; or one that moves in some
// environments and stays in others, as the environments share the merged
// root's code, so a moved block moves it in every environment's state.
func mergedMoves(before, now map[string]map[string]string, kept []engine.Move) ([]engine.Move, string) {
	to := map[string]string{}
	// The environment each address moves in, and one it stays in.
	movedIn, staysIn := map[string]string{}, map[string]string{}
	for _, m := range kept {
		to[m.From] = m.To
		movedIn[m.From] = engine.MovesFileName
	}
	moves := slices.Clone(kept)
	for _, env := range slices.Sorted(maps.Keys(before)) {
		at := map[string]string{}
		for address, id := range now[env] {
			at[id] = address
		}
		for _, address := range slices.Sorted(maps.Keys(before[env])) {
			after, ok := at[before[env][address]]
			if !ok {
				continue
			}
			if after == address {
				if _, ok := staysIn[address]; !ok {
					staysIn[address] = env
				}
				continue
			}
			if _, ok := movedIn[address]; !ok {
				movedIn[address] = env
			}
			if strings.HasPrefix(address, "module.") || strings.HasPrefix(after, "module.") {
				return nil, fmt.Sprintf("%s: %s is now %s, and moved blocks don't move resources of module calls", env, address, after)
			}
			if other, ok := to[address]; ok {
				if other != after {
					return nil, fmt.Sprintf("%s would move to both %s and %s", address, other, after)
				}
				continue
			}
			to[address] = after
			moves = append(moves, engine.Move{From: address, To: after})
		}
	}
	for _, m := range moves {
		if env, ok := staysIn[m.From]; ok {
			return nil, fmt.Sprintf("%s: %s is now %s, but %s keeps it at %s, and a moved block would move it in every environment's state, as they share the merged root's code", movedIn[m.From], m.From, m.To, env, m.From)
		}
	}
	return moves, ""
}

// moveMerged writes the moved blocks of the merged root in stack, merged
// again into result, where before, the import IDs of each environment's
// resources in it by address, has some that now has at other addresses:
// with --regenerate, a moved block from there to here for each (see
// mergedMoves), after kept, those of its moves.tf from before, which a
// merge without --regenerate writes back as they were. It returns why it
// can't merge again, where it can't: without --regenerate, as what it
// has would get other addresses; with it, as no moved blocks take them
// there, or the moves check of them fails. With them go removed, the
// removed blocks of what it hands over to other roots (see
// mergedHandovers), and those of its moves.tf from before.
func (r *Run) moveMerged(stack string, result *engine.MergeResult, before, now map[string]map[string]string, kept []engine.Move, removed []engine.Removal) (string, error) {
	moved := readdressed(before, now)
	if len(moved) > 0 && !r.options.Regenerate {
		return "merging it again would give what the merged root has other addresses: " + strings.Join(moved, "; "), nil
	}
	moves, why := mergedMoves(before, now, kept)
	if why != "" {
		return "merging it again would give what the merged root has other addresses, and " + why, nil
	}
	if len(moves) == 0 && len(removed) == 0 {
		return "", nil
	}
	result.Files = append(result.Files, engine.MovesFileName)
	sort.Strings(result.Files)
	check, err := r.options.tools.writeMergedMoves(stack, moves, removed)
	if err != nil {
		return "", err
	}
	if !check.Passed {
		return "merging it again would give what the merged root has other addresses, or hand it over to other roots, and the moved and removed blocks fail the moves check: " + strings.Join(check.Details, "; "), nil
	}
	if len(moved) > 0 {
		log.Printf("%s: moved blocks in %s move what has other addresses now (--regenerate): %s", relativePath(r.options.Out, stack), engine.MovesFileName, strings.Join(moved, "; "))
	}
	return "", nil
}

// clearMerged removes files, those engine.MergeRoots wrote in stack, with
// Terraform's working directory, and the directories they leave empty.
func clearMerged(stack string, files []string) error {
	for _, name := range files {
		if err := os.Remove(filepath.Join(stack, filepath.FromSlash(name))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return os.RemoveAll(filepath.Join(stack, ".terraform"))
}

// readdressNotInCloud makes each resource an incremental import found no
// longer in the cloud, in the root of one of members, the roots set aside
// of the stack whose merged root is at path, relative to the output
// directory, one of the merged root, of the member's environment: at its
// address there, as imports, the import IDs of each environment's
// resources in it by address, has it. It returns them, by environment,
// with their addresses in the merged root.
func (r *Run) readdressNotInCloud(path string, members []mergeMember, imports map[string]map[string]string) map[string][]engine.Import {
	envOf := map[string]string{}
	for _, m := range members {
		envOf[relativePath(r.options.Out, m.dir)] = m.env
	}
	return r.report.ReaddressNotInCloud(path, envOf, imports)
}

// goneFrom returns the addresses of the resources of the root in dir an
// incremental import found no longer in the cloud (see addToRoot), sorted:
// merged again, its stack keeps them as the root has them, and plans
// without importing them (see engine.MergeRoot.Gone).
func (r *Run) goneFrom(dir string) []string {
	return r.report.NotInCloudAt(relativePath(r.options.Out, dir))
}

// treeBackup is what a directory held: each file under it, but in
// Terraform's working directory and local state (see
// engine.LocalStateFile), such as its workspaces' in terraform.tfstate.d,
// by path relative to it: restoring it never writes state back.
type treeBackup struct {
	dir   string
	files map[string][]byte
}

// backupTree returns what dir holds.
func backupTree(dir string) (*treeBackup, error) {
	b := &treeBackup{dir: dir, files: map[string][]byte{}}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() && (d.Name() == ".terraform" || engine.LocalStateFile(d.Name())) {
			return filepath.SkipDir
		}
		if engine.LocalStateFile(d.Name()) {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		b.files[rel] = content
		return nil
	})
	return b, err
}

// restore writes back what b holds; a nil b restores nothing.
func (b *treeBackup) restore() error {
	if b == nil {
		return nil
	}
	for _, rel := range slices.Sorted(maps.Keys(b.files)) {
		path := filepath.Join(b.dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := fsutil.WriteFile(path, b.files[rel], 0o644); err != nil {
			return err
		}
	}
	return nil
}

// mergedHolds returns, by type and import ID, the merged root, relative
// to out, that each resource the merge checkpoints under out record is
// in, for the merged roots that still have configuration but those of
// whose roots byDir has one: as heldElsewhere does for roots, a resource
// goes into one root only.
func mergedHolds(out string, byDir map[string][]engine.Import) (map[engine.External]string, error) {
	base := filepath.Join(out, CheckpointDir, "merge")
	held := map[engine.External]string{}
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		stack := filepath.Join(out, strings.TrimSuffix(rel, ".json"))
		c, err := readMergeCheckpoint(out, stack)
		if err != nil {
			log.Printf("%s: %v; what that root imports isn't left out of the others (--incremental)", relativePath(out, stack), err)
			return nil
		}
		if c == nil || !c.Merge.Merged {
			return nil
		}
		if names, err := tfFiles(stack); err != nil || len(names) == 0 {
			return err
		}
		for _, m := range c.Members {
			if _, own := byDir[filepath.Join(out, filepath.FromSlash(m.Path))]; own {
				return nil
			}
		}
		for _, ids := range c.Imports {
			for address, id := range ids {
				key := engine.External{Type: engine.AddressType(address), ID: id}
				if _, ok := held[key]; !ok {
					held[key] = c.Merge.Path
				}
			}
		}
		return nil
	})
	return held, err
}
