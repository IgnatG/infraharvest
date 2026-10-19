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

// premerged reports whether the root in dir, under out, is one its
// stack's merge set aside (see premergeDir): its files are there, and dir
// has no configuration of its own.
func premerged(out, dir string) (bool, error) {
	set, err := fileExists(filepath.Join(premergeDir(out, dir), engine.VersionsFileName))
	if err != nil || !set {
		return false, err
	}
	names, err := tfFiles(dir)
	return len(names) == 0, err
}

// rootSource returns where the files of the root in dir, under out, are:
// in dir, or where its stack's merge set them aside.
func rootSource(out, dir string) string {
	if set, err := premerged(out, dir); err == nil && set {
		return premergeDir(out, dir)
	}
	return dir
}

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
// the set-aside roots, which the failure names.
func (r *Run) mergeStack(ctx context.Context, stack string, members []mergeMember) error {
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
	if prev != nil && prev.Merge.Merged {
		if missing := missingEnvironments(prev, envs); len(missing) > 0 {
			return r.keepMerged(stack, prev, members, fmt.Sprintf("the import has %s, not %s, which the merged root has too: it stays as it was, and what is new waits in the roots set aside under %s until an import has every environment", strings.Join(envs, ", "), strings.Join(missing, ", "), filepath.ToSlash(filepath.Join(CheckpointDir, premergeDirName))))
		}
	}
	if len(envs) < 2 {
		return nil
	}
	merge := report.Merge{Path: relativePath(out, stack), Environments: envs}
	for _, m := range members {
		merge.Roots = append(merge.Roots, report.MergedRoot{Env: m.env, Path: relativePath(out, m.dir)})
	}
	if reason := mergeRefusal(out, stack, members); reason != "" {
		merge.Reason = reason
		r.recordMerge(merge, members)
		return nil
	}
	fp, err := mergeFingerprint(out, members)
	if err != nil {
		return err
	}
	if prev != nil && prev.Fingerprint == fp && keptAsMerged(stack, prev, members) {
		log.Printf("%s is unchanged since it was merged", merge.Path)
		kept := prev.Merge
		kept.Unchanged = true
		r.recordMerge(kept, members)
		return nil
	}

	var backup *treeBackup
	if prev != nil && prev.Merge.Merged {
		// Merged again: what it had goes, and comes back if this merge
		// fails.
		if backup, err = backupTree(stack); err != nil {
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
		tr := terraformRun{execPath: m.execPath, pluginCacheDir: filepath.Join(m.cacheDir, "plugins")}
		if tr.env, err = terraformEnv(ctx, m.provider); err != nil {
			return err
		}
		tr.freshEnv = freshTerraformEnv(m.provider)
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
			Terraform: tr.in,
			Isolated:  m.isolated,
		}
	}
	opts := members[0].opts
	result, err := engine.MergeRoots(ctx, stack, roots, engine.MergeOptions{Omit: opts.Omit, StateOnly: opts.StateOnly, Scanners: opts.Scanners, Adapters: opts.Adapters, CommandTimeout: opts.CommandTimeout})
	if err != nil {
		return errors.Join(err, backup.restore())
	}
	imports := map[string]map[string]string{}
	for _, env := range result.Environments {
		imports[env.Env] = env.Imports
	}
	if result.Reason == "" && prev != nil && prev.Merge.Merged {
		if moved := readdressed(prev.Imports, imports); len(moved) > 0 {
			result.Reason = "merging it again would give what the merged root has other addresses: " + strings.Join(moved, "; ")
			if err := clearMerged(stack, result.Files); err != nil {
				return err
			}
		}
	}
	fillMerge(&merge, result)
	if !merge.Merged && backup != nil {
		// The merged root an earlier import wrote, which may be in use,
		// stays; so do the roots set aside, with what is new in them.
		if err := backup.restore(); err != nil {
			return err
		}
		for _, m := range members {
			if !m.premerged {
				if err := removeRoot(out, m.dir); err != nil {
					return err
				}
			}
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
		log.Printf("%s: %s share one root; their own roots are kept under %s", merge.Path, strings.Join(envs, ", "), filepath.Join(out, CheckpointDir, premergeDirName))
		for _, m := range members {
			if err := removeRoot(out, m.dir); err != nil {
				return err
			}
		}
	}
	r.recordMerge(merge, members)
	r.readdressNotInCloud(merge, members, result)
	return writeMergeCheckpoint(out, stack, mergeCheckpoint{Fingerprint: fp, Members: checkpointMembers(out, members), Merge: merge, Files: result.Files, Imports: imports})
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

// keepMerged keeps the merged root in stack as prev, its checkpoint,
// records it, and sets members aside, which an import generated or added
// to but can't merge again, why says, as a failure of the run.
func (r *Run) keepMerged(stack string, prev *mergeCheckpoint, members []mergeMember, why string) error {
	out := r.options.Out
	for _, m := range members {
		if !m.premerged {
			if err := setAside(out, m.dir); err != nil {
				return err
			}
		}
	}
	kept := prev.Merge
	kept.Unchanged = true
	kept.Reason = why
	r.recordMerge(kept, members)
	err := fmt.Errorf("%s: %s", relativePath(out, stack), why)
	log.Print(err)
	r.recordFailure(r.options, err)
	return nil
}

// keptAsMerged reports whether the merged root in stack, of which prev is
// the checkpoint, stands as prev has it for members: merged, with its
// files, and members set aside; or kept apart, with members' roots where
// they are.
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
// longer in the cloud, in the root of members, of the stack merge says
// merged, one of the merged root, at its address there (see
// engine.MergedEnvironment.Addresses): the root it was in is set aside.
func (r *Run) readdressNotInCloud(merge report.Merge, members []mergeMember, result *engine.MergeResult) {
	if !merge.Merged {
		return
	}
	addresses := map[string]map[string]string{}
	for i, m := range members {
		if i < len(result.Environments) {
			addresses[relativePath(r.options.Out, m.dir)] = result.Environments[i].Addresses
		}
	}
	for i, g := range r.report.NotInCloud {
		byAddress, ok := addresses[g.Path]
		if !ok {
			continue
		}
		r.report.NotInCloud[i].Path = merge.Path
		if to, ok := byAddress[g.Address]; ok {
			r.report.NotInCloud[i].Address = to
		}
	}
}

// treeBackup is what a directory held: each file under it, but in
// Terraform's working directory, by path relative to it.
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
		if d.IsDir() && d.Name() == ".terraform" {
			return filepath.SkipDir
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
