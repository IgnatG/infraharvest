// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// rootMode is what a run does with one of the roots it generates (see
// listedCall.decideRoot).
type rootMode int

const (
	// rootFresh is generated into a directory no import wrote.
	rootFresh rootMode = iota
	// rootRegenerated is a root an earlier import generated, generated
	// again from scratch: with --regenerate, with --resume if that import
	// didn't finish, or with --incremental if it has no resources.
	rootRegenerated
	// rootResumed is as its checkpoint records, unchanged since it was
	// generated (--resume).
	rootResumed
	// rootIncremental is added to (--incremental, see addToRoot).
	rootIncremental
	// rootRefused is left as it was, for the decision's reason.
	rootRefused
)

// rootDecision is what a run does with one of its roots, and what it
// found on disk deciding it.
type rootDecision struct {
	mode rootMode
	// reason is why a refused root is left as it was.
	reason error
	// result is what the checkpoint of a resumed root records.
	result *engine.Result
	// premerged says the root is one its stack's merge set aside (see
	// premergeDir), and restore that an incremental import brings it back
	// first, to add to it as it was, then sets it aside again, for the
	// merge phase to merge its stack again (see MergeEnvironments).
	premerged, restore bool
	// onDisk is what an earlier import left of a root generated afresh or
	// again, for claimRoot.
	onDisk rootOnDisk
}

// refused returns d, refused for reason.
func (d rootDecision) refused(reason error) rootDecision {
	d.mode, d.reason = rootRefused, reason
	return d
}

// decideRoot decides, once, what the call does with the root in dir, from
// fingerprint fp, by the run's mode and what earlier imports left on
// disk. A root the call's handovers leave as it was is refused (see
// handovers.refusal); with --resume, one unchanged since its checkpoint
// is resumed (see resumed); with --incremental, one with resources is
// added to, unless, with --resume, its import didn't finish. Any other is
// generated, afresh or again, but for one that would hand over what roots
// generated before it didn't import (see handovers.refuseUnreceived),
// one its stack's merge set aside, which may be in use, without
// --regenerate or --resume, and one an earlier import's files or
// checkpoint refuse (see rootOnDisk.refusal). It returns an error only if
// the call can't go on.
func (c *listedCall) decideRoot(dir, fp string) (rootDecision, error) {
	out, options := c.options.Out, c.options
	if err := c.handovers.refusal(dir); err != nil {
		return rootDecision{mode: rootRefused, reason: err}, nil
	}
	if options.Resume {
		result, err := resumed(out, dir, fp)
		if err != nil {
			return rootDecision{}, err
		}
		if result != nil {
			return rootDecision{mode: rootResumed, result: result}, nil
		}
	}
	set, err := premerged(out, dir)
	if err != nil {
		return rootDecision{}, err
	}
	d := rootDecision{premerged: set, restore: set && options.Incremental}
	// Where the root's files are until the import brings it back.
	files := dir
	if d.restore {
		files = premergeDir(out, dir)
	}
	if options.Incremental {
		has, err := engine.HasConfiguration(files)
		if err != nil {
			return rootDecision{}, err
		}
		if has && options.Resume {
			// With --resume, a root whose import didn't finish is
			// generated again rather than added to (see addToRoot).
			unfinished, err := unfinishedRoot(out, dir)
			if err != nil {
				return rootDecision{}, err
			}
			has = !unfinished
		}
		if has {
			d.mode = rootIncremental
			return d, nil
		}
	}
	// A root an earlier import generated is generated again, from
	// scratch, with --regenerate, or with --resume if that import didn't
	// finish; a directory no import wrote is left alone. With
	// --incremental, a root gets here only if it has no resources or, with
	// --resume, its import didn't finish.
	// A root that would hand over what a root generated before it didn't
	// import is left as it was, still holding it.
	if err := c.handovers.refuseUnreceived(dir); err != nil {
		return d.refused(err), nil
	}
	if set && !options.Regenerate && !options.Resume {
		return d.refused(fmt.Errorf("%s was merged into the root its stack's environments share, which may be in use: pass --regenerate to generate both again, or --incremental to add what is new", dir)), nil
	}
	onDisk, err := inspectRoot(out, dir, files)
	if err == nil {
		err = onDisk.refusal(dir, options.Resume, options.Regenerate || options.Incremental)
	}
	if err != nil {
		return d.refused(err), nil
	}
	d.mode, d.onDisk = rootFresh, onDisk
	if onDisk.checkpointed {
		d.mode = rootRegenerated
	}
	return d, nil
}

// rootOnDisk is what earlier imports left of a root a run generates: the
// .tf files of its directory, and its checkpoint, if checkpointed, which
// holds content.
type rootOnDisk struct {
	names        []string
	checkpointed bool
	content      []byte
	// c is the checkpoint, if readable; else as of a root whose import
	// didn't finish.
	c        checkpoint
	readable bool
}

// inspectRoot returns what earlier imports left of the root in dir, under
// out, whose files are in files: dir, or where its stack's merge set them
// aside, for a root the import is to bring back.
func inspectRoot(out, dir, files string) (rootOnDisk, error) {
	var e rootOnDisk
	var err error
	if e.names, err = tfFiles(files); err != nil {
		return e, err
	}
	content, err := os.ReadFile(checkpointPath(out, dir))
	if errors.Is(err, fs.ErrNotExist) {
		return e, nil
	}
	if err != nil {
		return e, err
	}
	e.checkpointed, e.content = true, content
	e.readable = json.Unmarshal(content, &e.c) == nil
	if !e.readable {
		e.c = checkpoint{}
	}
	return e, nil
}

// refusal returns why the root in dir isn't generated from what e says an
// earlier import left; nil if it is. A directory no checkpoint says an
// import wrote must hold no .tf files, so that an unrelated root is never
// overwritten. A root an earlier import generated, which may be in use,
// is generated again only with regenerate or, if that import didn't
// finish, with resume, unless no .tf file of it is left.
func (e rootOnDisk) refusal(dir string, resume, regenerate bool) error {
	if !e.checkpointed {
		if len(e.names) > 0 {
			return fmt.Errorf("%s already holds %s, which no import by infraharvest wrote: use an empty output directory, or --incremental to add to a root an import generated", dir, strings.Join(e.names, ", "))
		}
		return nil
	}
	finished := e.c.Result != nil
	if len(e.names) > 0 && !regenerate && (finished || !resume) {
		if finished {
			return fmt.Errorf("%s is a root an earlier import generated, which may be in use: pass --regenerate to generate it again, or --incremental to add what is new to it", dir)
		}
		return fmt.Errorf("the import that generated %s didn't finish: pass --resume to generate it again", dir)
	}
	return nil
}

// CheckpointDir holds, in the output directory, a checkpoint of each root
// an import generated, so that --resume can skip it. .gitignore keeps it
// out of version control.
const CheckpointDir = ".infraharvest"

// checkpoint records a generated root: what it was generated from, and
// the result, for the report.
type checkpoint struct {
	Fingerprint string         `json:"fingerprint"`
	Result      *engine.Result `json:"result"`
	// Pending are, while a run generates a root an earlier import
	// generated again, where that import's resources were (see
	// previousAddresses), and PendingFiles the files its configuration
	// was in: the checkpoint has no result then, and a run interrupted on
	// the way would otherwise lose them.
	Pending      []engine.Managed `json:"pending,omitempty"`
	PendingFiles []string         `json:"pending_files,omitempty"`
	// PendingMoves are the moved blocks the root's moves.tf had then,
	// which the run removed with the rest.
	PendingMoves []engine.Move `json:"pending_moves,omitempty"`
	// PendingRemoved are the removed blocks it had then.
	PendingRemoved []engine.Removal `json:"pending_removed,omitempty"`
}

// checkpointsDir holds, under out, the checkpoints of the roots, each at
// its root's path relative to out (see checkpointPath).
func checkpointsDir(out string) string {
	return filepath.Join(out, CheckpointDir, "checkpoints")
}

// checkpointPath returns where the checkpoint of the root in dir, under
// out, is.
func checkpointPath(out, dir string) string {
	return filepath.Join(checkpointsDir(out), filepath.FromSlash(relativePath(out, dir))+".json")
}

// checkpointDirs returns the directories of the roots the checkpoints
// under out record, sorted.
func checkpointDirs(out string) ([]string, error) {
	base := checkpointsDir(out)
	if _, err := os.Stat(base); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	var dirs []string
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		dirs = append(dirs, filepath.Join(out, strings.TrimSuffix(rel, ".json")))
		return nil
	})
	sort.Strings(dirs)
	return dirs, err
}

// resumed returns the result a checkpoint recorded for dir, if it has the
// same fingerprint and the root's files are still there (see
// engine.ResultFiles); nil otherwise.
// A root its stack's merge set aside counts as there where its set-aside
// copy is (see premergeDir).
func resumed(out, dir, fp string) (*engine.Result, error) {
	content, err := os.ReadFile(checkpointPath(out, dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c checkpoint
	if err := json.Unmarshal(content, &c); err != nil || c.Fingerprint != fp || c.Result == nil {
		return nil, nil // an unreadable checkpoint means generating again
	}
	// A root its stack's merge set aside is resumed from there.
	dir = rootSource(out, dir)
	names, err := engine.ResultFiles(dir, c.Result)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return nil, nil // a missing file means generating again
		}
	}
	return c.Result, nil
}

// saveCheckpoint records that dir was generated from fp, with result.
func saveCheckpoint(out, dir, fp string, result *engine.Result) error {
	return writeCheckpoint(out, dir, checkpoint{Fingerprint: fp, Result: result})
}

// writeCheckpoint writes c as the checkpoint of dir under out.
func writeCheckpoint(out, dir string, c checkpoint) error {
	path := checkpointPath(out, dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFile(path, append(content, '\n'), 0o644)
}

// readCheckpoint returns the checkpoint of dir under out; nil if there is
// none.
func readCheckpoint(out, dir string) (*checkpoint, error) {
	path := checkpointPath(out, dir)
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c checkpoint
	if err := json.Unmarshal(content, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// checkpointResult returns the result the checkpoint of dir records, from
// whichever run; nil if there is none.
func checkpointResult(out, dir string) (*engine.Result, error) {
	c, err := readCheckpoint(out, dir)
	if err != nil || c == nil {
		return nil, err
	}
	return c.Result, nil
}

// clearGenerated removes what a previous run generated in dir, so that the
// root can be generated again: the files the engine generates (see
// engine.GeneratedFiles), the backend's, the files its checkpoint under
// out says the configuration was split into, and the files --incremental
// added (see engine.AddedFiles), which the checkpoint names too.
func clearGenerated(out, dir string) error {
	names := append(engine.GeneratedFiles(), BackendFileName)
	if content, err := os.ReadFile(checkpointPath(out, dir)); err == nil {
		var c checkpoint
		if json.Unmarshal(content, &c) == nil {
			names = append(names, c.PendingFiles...)
			if c.Result != nil {
				names = append(names, c.Result.Files...)
			}
		}
	}
	for _, name := range names {
		if filepath.Base(name) != name {
			continue // a checkpoint names files of the root only
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	added, err := engine.AddedFiles(dir)
	if err != nil {
		return err
	}
	for _, path := range added {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// unfinishedRoot reports whether dir is what an import that didn't finish
// generating it left: its checkpoint records that the generation started
// (see claimRoot) and no result or, without a checkpoint, its files are
// what an unfinished generation left (see engine.Unfinished), where they
// are (see rootSource).
func unfinishedRoot(out, dir string) (bool, error) {
	c, err := readCheckpoint(out, dir)
	if err != nil {
		return false, err
	}
	if c != nil {
		return c.Result == nil, nil
	}
	return engine.Unfinished(rootSource(out, dir))
}

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

// fileExists reports whether path exists.
func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
