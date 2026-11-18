// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/envmerge"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/logging"
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
			logging.Warnf("%s: its checkpoint can't be read (%v); its stack isn't merged again", dir, err)
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
// envmerge.MergeRoots). A stack whose members are as they were when it
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
//
// It goes phase by phase, each a method of stackMerge: what the stack
// merged before (see stackMerge.previous), the environments it merges
// (see stackMerge.environments), whether it need merge at all (see
// stackMerge.settled), the merge itself (see stackMerge.clearPrevious,
// stackMerge.mergeRoots and stackMerge.handOver), and its outcome (see
// stackMerge.conclude).
func (p *mergePhase) mergeStack(ctx context.Context, stack string, members []mergeMember) (err error) {
	s := &stackMerge{phase: p, out: p.out, stack: stack, members: members, envs: memberEnvironments(members)}
	if done, err := s.previous(); done || err != nil {
		return err
	}
	if done, err := s.environments(); done || err != nil {
		return err
	}
	if len(s.envs) < 2 {
		return nil
	}
	if done, err := s.settled(); done || err != nil {
		return err
	}
	// What the merged root of a stack merged before had comes back on any
	// error until the stack either keeps it (see restorePrevious) or its
	// merged root is written for good.
	defer func() {
		if err != nil {
			err = errors.Join(err, s.backup.restore())
		}
	}()
	if err := s.clearPrevious(); err != nil {
		return err
	}
	if err := s.mergeRoots(ctx); err != nil {
		return err
	}
	if err := s.handOver(); err != nil {
		return err
	}
	return s.conclude(ctx)
}

// stackMerge is what mergeStack works out of one stack, phase by phase.
type stackMerge struct {
	phase      *mergePhase
	out, stack string
	// members are the stack's roots, and envs their environments, with
	// those the merged root has that the import doesn't, restored from
	// the roots set aside when it merged (see stackMerge.environments).
	members []mergeMember
	envs    []string
	// prev is the stack's merge checkpoint, if it merged before, and
	// adopted says it was told from the merged root's files (see
	// adoptedCheckpoint). joining are the environments the import brings
	// that the merged root doesn't have yet.
	prev    *mergeCheckpoint
	adopted bool
	joining []string
	// merge is the outcome the report records, and fp the members'
	// fingerprint (see mergeFingerprint).
	merge report.Merge
	fp    string
	// backup is what the merged root of a stack merged before had (see
	// clearPrevious), and kept and keptRemoved the moved and removed
	// blocks of its moves.tf, while it is there: those of an earlier merge
	// with --regenerate, not yet applied and deleted.
	backup      *treeBackup
	kept        []rootfile.Move
	keptRemoved []rootfile.Removal
	// result is what the merge did, and imports what the merged root
	// imports, by environment, then address.
	result  *envmerge.MergeResult
	imports map[string]map[string]string
	// removed are what the merged root hands over to other roots (see
	// planMergedHandovers), and incomplete the handovers it can't finish.
	removed    []rootfile.Removal
	incomplete []report.IncompleteHandover
}

// memberEnvironments returns the environments of members, in their
// order, each once.
func memberEnvironments(members []mergeMember) []string {
	var envs []string
	for _, m := range members {
		if !slices.Contains(envs, m.env) {
			envs = append(envs, m.env)
		}
	}
	return envs
}

// previous reads what the stack merged before, its checkpoint. A merged
// root no checkpoint records, as in a fresh checkout of the output,
// merges again from the roots the import generated afresh, with what it
// has told from its files (see adoptedCheckpoint); a plain import refuses
// it (see mergeRefusal). It returns done where the merged root stays as
// it was (see keepMerged).
func (s *stackMerge) previous() (done bool, err error) {
	p := s.phase
	if s.prev, err = mergeCheckpoints.read(s.out, s.stack); err != nil {
		return false, err
	}
	if s.prev != nil || (!p.incremental && !p.resume && !p.regenerate) {
		return false, nil
	}
	c, why, err := p.adoptedCheckpoint(s.stack, s.members)
	if err != nil {
		return false, err
	}
	if why != "" {
		return true, p.keepMerged(s.stack, c, s.members, why)
	}
	s.prev, s.adopted = c, c != nil
	if !s.adopted || !p.incremental {
		return false, nil
	}
	// What the merged root doesn't have yet is kept apart, as an
	// incremental import adds it to a kept root.
	for i, m := range s.members {
		if ids, ok := s.prev.Imports[m.env]; ok {
			s.members[i].isolated = newSinceMerge(m.result, ids)
		}
	}
	// What it has that is no longer in the cloud, which the roots
	// generated afresh lack, it keeps as it was.
	why, err = p.keepGone(s.stack, s.prev, s.members)
	if err != nil {
		return false, err
	}
	if why != "" {
		return true, p.keepMerged(s.stack, s.prev, s.members, why)
	}
	return false, nil
}

// environments works out the environments the stack merges: those the
// import brings that the merged root doesn't have yet join it, and those
// it has that the import doesn't merge again as their roots were set
// aside, unplanned (see restoredMembers). It returns done where they
// can't, and the merged root stays as it was (see keepMerged).
func (s *stackMerge) environments() (done bool, err error) {
	s.joining = joiningEnvironments(s.prev, s.members)
	if s.prev == nil || !s.prev.Merge.Merged {
		return false, nil
	}
	missing := missingEnvironments(s.prev, s.envs)
	if len(missing) == 0 {
		return false, nil
	}
	restored, why := s.phase.restoredMembers(s.stack, s.prev, missing, s.members)
	if why != "" {
		return true, s.phase.keepMerged(s.stack, s.prev, s.members, fmt.Sprintf("the import has %s, not %s, which the merged root has too, and %s: it stays as it was, and what is new waits in the roots set aside under %s until an import has every environment", strings.Join(s.envs, ", "), strings.Join(missing, ", "), why, filepath.ToSlash(filepath.Join(CheckpointDir, premergeDirName))))
	}
	s.members = append(slices.Clone(s.members), restored...)
	sort.Slice(s.members, func(i, j int) bool {
		if s.members[i].env != s.members[j].env {
			return s.members[i].env < s.members[j].env
		}
		return s.members[i].dir < s.members[j].dir
	})
	s.envs = append(s.envs, missing...)
	sort.Strings(s.envs)
	return false, nil
}

// settled starts the stack's report, and returns done, with it recorded,
// where the stack needn't merge: the merge is refused (see mergeRefusal),
// or the members are as they were when it merged, or an incremental
// import added nothing to them, and the stack keeps what it has.
func (s *stackMerge) settled() (done bool, err error) {
	p, members := s.phase, s.members
	s.merge = report.Merge{Path: relativePath(s.out, s.stack), Environments: s.envs}
	for _, m := range members {
		s.merge.Roots = append(s.merge.Roots, report.MergedRoot{Env: m.env, Path: relativePath(s.out, m.dir)})
	}
	if reason := mergeRefusal(s.out, s.stack, members, s.adopted); reason != "" {
		s.merge.Reason = reason
		p.recordMerge(s.merge, members)
		return true, nil
	}
	if s.fp, err = mergeFingerprint(s.out, members); err != nil {
		return false, err
	}
	prev := s.prev
	if prev != nil && prev.Fingerprint == s.fp && keptAsMerged(s.stack, prev, members) && !plannable(prev, members) {
		logging.Infof("%s is unchanged since it was merged", s.merge.Path)
		s.keepUnchanged()
		return true, nil
	}
	if p.incremental && prev != nil && prev.Merge.Merged && keptAsMerged(s.stack, prev, members) && !plannable(prev, members) && !slices.ContainsFunc(members, func(m mergeMember) bool { return len(m.isolated) > 0 }) {
		// An incremental import added nothing to the roots set aside,
		// which are as they were when the stack merged, though what it
		// listed has changed, such as resources gone from the cloud,
		// whose import a plan of the merged root would fail on.
		logging.Infof("%s: nothing new to merge (--incremental)", s.merge.Path)
		s.keepUnchanged()
		return true, nil
	}
	return false, nil
}

// keepUnchanged records that the stack keeps what it merged before, as
// it was.
func (s *stackMerge) keepUnchanged() {
	kept := s.prev.Merge
	kept.Unchanged = true
	s.phase.recordMerge(kept, s.members)
	s.phase.readdressNotInCloud(kept.Path, s.members, s.prev.Imports)
}

// clearPrevious backs up what the merged root of a stack merged before
// had, with the moved and removed blocks of its moves.tf, and removes its
// files: they come back if this merge fails.
func (s *stackMerge) clearPrevious() error {
	if s.prev == nil || !s.prev.Merge.Merged {
		return nil
	}
	b, err := backupTree(s.stack)
	if err != nil {
		return err
	}
	s.backup = b
	if s.kept, err = rootfile.ReadMoves(s.stack); err != nil {
		return err
	}
	if s.keptRemoved, err = rootfile.ReadRemoved(s.stack); err != nil {
		return err
	}
	for _, name := range s.prev.Files {
		if err := os.Remove(filepath.Join(s.stack, filepath.FromSlash(name))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// restorePrevious writes back what the merged root had (see
// clearPrevious), once only.
func (s *stackMerge) restorePrevious() error {
	b := s.backup
	s.backup = nil
	return b.restore()
}

// mergeRoots sets the members' roots aside and merges them (see
// mergeRoot), into the merged root. Nothing says which of the resources
// of a stack merged before an earlier merge kept apart, as what an
// incremental import added (see mergeMember.isolated), nor the labels it
// gave them, which came from their roots then, and differ from those of
// the roots generated since, or told from the merged root's files: what
// would get another address is kept apart (see keepApart), and the stack
// merges again, as many times as that takes, up to maxKeptApart. With
// --regenerate, moved blocks follow it instead.
func (s *stackMerge) mergeRoots(ctx context.Context) error {
	p, prev := s.phase, s.prev
	logging.Infof("merging the roots of %s into %s", strings.Join(s.envs, ", "), s.merge.Path)
	roots := make([]envmerge.MergeRoot, len(s.members))
	for i, m := range s.members {
		root, err := s.mergeRoot(ctx, m)
		if err != nil {
			return err
		}
		roots[i] = root
	}
	// A restored member has no engine options or profile of its own.
	imported := s.members[slices.IndexFunc(s.members, func(m mergeMember) bool { return !m.restored })]
	opts := imported.opts
	mergeOptions := envmerge.MergeOptions{Profile: imported.profile, Gate: engine.FullGate(opts), StateOnly: opts.StateOnly, CommandTimeout: opts.CommandTimeout}
	result, err := p.tools.mergeRoots(ctx, s.stack, roots, mergeOptions)
	s.recordPlans(result)
	// What the merged root matched across environments isn't kept apart:
	// it would get another address all the same.
	var shared map[string]bool
	if prev != nil {
		shared = sharedAddresses(prev.Imports)
	}
	for attempt := 0; err == nil && prev != nil && prev.Merge.Merged && !p.regenerate && result.Reason == "" && attempt < maxKeptApart; attempt++ {
		if !s.keepApart(roots, result, shared) {
			break
		}
		logging.Infof("%s: merging again, with what would get another address kept apart as its environment's", s.merge.Path)
		if err := clearMerged(s.stack, result.Files); err != nil {
			return err
		}
		result, err = p.tools.mergeRoots(ctx, s.stack, roots, mergeOptions)
		s.recordPlans(result)
	}
	if err != nil {
		p.warnMergedIncomplete(s.stack, s.members, err.Error())
		return err
	}
	s.result = result
	s.imports = map[string]map[string]string{}
	for _, env := range result.Environments {
		s.imports[env.Env] = env.Imports
	}
	return nil
}

// mergeRoot sets m's root aside, unless it is already, and returns it as
// the merge takes it, with how to run Terraform in it, unless it was
// restored, unplanned.
func (s *stackMerge) mergeRoot(ctx context.Context, m mergeMember) (envmerge.MergeRoot, error) {
	premerge := premergeDir(s.out, m.dir)
	if !m.premerged {
		if err := copyRoot(m.dir, premerge); err != nil {
			return envmerge.MergeRoot{}, err
		}
	}
	var runner func(string) (tfrun.Terraform, error)
	if !m.restored {
		tr := terraformRun{execPath: m.execPath, pluginCacheDir: filepath.Join(m.cacheDir, "plugins"), schemas: s.phase.schemas, parallelism: m.parallelism}
		var err error
		if runner, err = s.phase.tools.runner(ctx, tr, m.provider); err != nil {
			return envmerge.MergeRoot{}, err
		}
	}
	return envmerge.MergeRoot{
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
		Gone:      s.phase.goneFrom(m.dir),
		Arrivals:  m.result.Arrived,
	}, nil
}

// keepApart keeps apart, in the members and their roots, what result, the
// merge of roots, would give another address than the merged root has
// for it, with the label it has there, but for shared, the addresses it
// matched across environments (see movedApart). It reports whether it
// kept any apart, so that the stack merges again.
func (s *stackMerge) keepApart(roots []envmerge.MergeRoot, result *envmerge.MergeResult, shared map[string]bool) bool {
	members := s.members
	now := map[string]map[string]string{}
	for _, env := range result.Environments {
		now[env.Env] = env.Imports
	}
	again := false
	for i, m := range members {
		if more := movedApart(m, s.prev.Imports[m.env], now[m.env], shared); len(more) > 0 {
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
	return again
}

// handOver works out, for a stack merged before, what its merged root
// hands over to other roots and the moved blocks that follow what it
// readdresses (see mergedHandovers and mergePhase.moveMerged), or why it can't
// merge again, in which case what the merge wrote goes.
func (s *stackMerge) handOver() error {
	prev, result := s.prev, s.result
	if result.Reason != "" || prev == nil || !prev.Merge.Merged {
		return nil
	}
	if gone := leftOutOfMerge(prev.Imports, s.imports); s.adopted && len(gone) > 0 {
		// The roots generated afresh have only what this run lists.
		result.Reason = "merging it again from what this run lists would leave out what the merged root has that this run doesn't list, but can't tell is no longer in the cloud (not listed in full with these services, filters and layout, or in another root): " + strings.Join(gone, "; ")
	} else {
		s.removed, s.incomplete, result.Reason = mergedHandovers(s.out, s.stack, s.members, s.imports)
		if result.Reason == "" {
			var err error
			if result.Reason, err = s.phase.moveMerged(s.stack, result, prev.Imports, s.imports, s.kept, append(s.removed, stillRemoved(s.keptRemoved, s.imports, s.removed)...)); err != nil {
				// What the merge wrote goes, as where it can't merge
				// again, so that only what the merged root had comes
				// back (see mergeStack).
				return errors.Join(err, clearMerged(s.stack, result.Files))
			}
		}
	}
	if result.Reason != "" {
		return clearMerged(s.stack, result.Files)
	}
	return nil
}

// recordPlans adds the plans the merge result ran as each member's
// environment to the member's directory in the report (see
// envmerge.MergeResult.Plans): each read the environment's resources,
// whether the stack merged or not.
func (s *stackMerge) recordPlans(result *envmerge.MergeResult) {
	if result == nil {
		return
	}
	for i, plans := range result.Plans {
		if i < len(s.members) {
			s.phase.report.AddPlans(relativePath(s.out, s.members[i].dir), plans)
		}
	}
}

// conclude records the merge's outcome. Where a stack merged before
// doesn't merge again, its merged root stays as it was: without the
// environments new to it (see mergeWithout), or else with what is new
// waiting in the roots set aside (see keepMerged). Otherwise the members'
// roots go, set aside, where they merged, or the roots set aside go,
// where they didn't, and the merge's checkpoint is written.
func (s *stackMerge) conclude(ctx context.Context) error {
	p, out, members := s.phase, s.out, s.members
	fillMerge(&s.merge, s.result)
	merge := s.merge
	if !merge.Merged && s.backup != nil {
		if err := s.restorePrevious(); err != nil {
			return err
		}
		if len(s.joining) > 0 {
			// The merged root an earlier import wrote stays as it was for
			// what it has, and the environments new to it keep their roots.
			return p.mergeWithout(ctx, s.stack, s.prev, members, s.joining, merge.Reason)
		}
		// The merged root an earlier import wrote, which may be in use,
		// stays; so do the roots set aside, with what is new in them, which
		// keepMerged sets aside.
		return p.keepMerged(s.stack, s.prev, members, "merging it again failed, so it stays as it was, and what is new waits in the roots set aside under "+filepath.ToSlash(filepath.Join(CheckpointDir, premergeDirName))+": "+merge.Reason)
	}
	if !merge.Merged {
		logging.Infof("%s: the environments keep their roots: %s", merge.Path, merge.Reason)
		for _, m := range members {
			if err := os.RemoveAll(premergeDir(out, m.dir)); err != nil {
				return err
			}
		}
	} else {
		// The merged root is written for good once its checkpoint is:
		// until then an error puts back what it had (see mergeStack).
		logging.Infof("%s: %s share one root; their own roots are kept under %s", merge.Path, strings.Join(s.envs, ", "), filepath.Join(out, CheckpointDir, premergeDirName))
		if s.prev != nil && s.prev.Merge.Merged {
			p.report.AddMergedMoves(merge.Path, s.prev.Imports, s.imports, s.removed)
		}
		for _, i := range s.incomplete {
			recordIncomplete(p.report, i)
		}
		for _, m := range members {
			if err := removeRoot(out, m.dir); err != nil {
				return err
			}
		}
	}
	p.recordMerge(merge, members)
	if merge.Merged {
		if gone := p.readdressNotInCloud(merge.Path, members, s.imports); len(gone) > 0 {
			if err := p.tools.writeMergedNotInCloud(s.stack, gone); err != nil {
				return err
			}
		}
	}
	if err := mergeCheckpoints.write(out, s.stack, mergeCheckpoint{Fingerprint: s.fp, Members: checkpointMembers(out, members), Merge: merge, Files: s.result.Files, Imports: s.imports}); err != nil {
		return err
	}
	// What the merged root had doesn't come back over the one written.
	s.backup = nil
	return nil
}
