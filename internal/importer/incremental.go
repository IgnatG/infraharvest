// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// InRootReason is why an incremental import leaves out a resource: the
// root has it.
const InRootReason = "already in the root"

// addToRoot adds to dir, a root an earlier import generated, the resources
// of imports it doesn't have yet (see engine.Existing and engine.Add),
// generating them in a staging directory under the output's checkpoints.
// It records the others as excluded, and returns the resources it added,
// the result, and what the root then holds for its checkpoint. Neither
// result is set if nothing is new. On an error, the resources returned
// are those the error failed: every one of imports until it knows which
// are new.
func (r *Run) addToRoot(ctx context.Context, dir string, imports []engine.Import, opts engine.Options, tr terraformRun) ([]engine.Import, *engine.Result, *engine.Result, error) {
	out := r.options.Out
	// A root whose import didn't finish isn't one to add to: what it
	// has was never checked, and may be only part of it.
	unfinished, err := unfinishedRoot(out, dir)
	if err != nil {
		return imports, nil, nil, err
	}
	if unfinished {
		return imports, nil, nil, fmt.Errorf("the import that generated %s didn't finish: generate the root again with --resume, then add to it with --incremental", dir)
	}
	previous, err := checkpointResult(out, dir)
	if err != nil {
		return imports, nil, nil, err
	}
	existing, err := engine.Existing(dir, previous)
	if err != nil {
		return imports, nil, nil, err
	}
	if len(existing) == 0 && len(r.options.ManagedState) == 0 {
		return imports, nil, nil, fmt.Errorf("%s has configuration, but neither its import blocks nor a checkpoint of the import that generated it say which resources it has: pass --managed-state with its state, so that the import leaves them out", dir)
	}
	owned := previouslyOwned(previous, opts)
	var added []engine.Import
	for _, imp := range imports {
		key := engine.External{Type: imp.Type, ID: imp.ID}
		if _, ok := existing[key]; ok {
			r.report.Excluded = append(r.report.Excluded, report.Excluded{Type: imp.Type, ID: imp.ID, Reason: InRootReason, Scope: r.scope})
			continue
		}
		if owner, ok := owned[key]; ok {
			r.report.Excluded = append(r.report.Excluded, report.Excluded{Type: imp.Type, ID: imp.ID, Reason: ownedReason(owner), Scope: r.scope})
			continue
		}
		added = append(added, imp)
	}
	if len(added) == 0 {
		log.Printf("%s: nothing new to add (--incremental)", dir)
		return nil, nil, nil, nil
	}
	log.Printf("adding %d new resources to %s (--incremental)", len(added), dir)
	staging := stagingDir(out, dir)
	tf, err := tr.in(staging)
	if err != nil {
		return added, nil, nil, err
	}
	// The same provider versions as the root.
	lock := r.lock
	if content, err := os.ReadFile(filepath.Join(dir, engine.LockFileName)); err == nil {
		lock = content
	}
	if lock != nil {
		if err := os.WriteFile(filepath.Join(staging, engine.LockFileName), lock, 0o644); err != nil {
			return added, nil, nil, err
		}
	}
	rootTF, err := tr.in(dir)
	if err != nil {
		return added, nil, nil, err
	}
	result, err := engine.Add(ctx, tf, rootTF, staging, dir, added, opts, existing)
	if err != nil {
		return added, nil, nil, err
	}
	if r.lock == nil {
		r.lock, _ = os.ReadFile(filepath.Join(dir, engine.LockFileName))
	}
	holds := previous.With(result)
	if previous != nil {
		// Without the earlier result, the root's README stays as it is.
		if err := engine.WriteReadme(dir, holds); err != nil {
			return added, nil, nil, err
		}
	}
	return added, result, holds, nil
}

// previouslyOwned returns, by type and import ID, who manages each
// resource the import that generated a root left out because its tags,
// as Terraform generated them, say another tool manages it (see
// engine.Result.Owned): the listing may not see those tags, so without
// this every incremental import would generate those resources again only
// to leave them out again. None are returned when opts keep owned
// resources, and a resource opts keep is left to generate.
func previouslyOwned(previous *engine.Result, opts engine.Options) map[engine.External]engine.Owner {
	if previous == nil || opts.IncludeOwned {
		return nil
	}
	owned := map[engine.External]engine.Owner{}
	for _, o := range previous.Owned {
		typ := resourceTypeOf(o.Address)
		if opts.KeepOwned[typ+" "+o.ID] {
			continue
		}
		owned[engine.External{Type: typ, ID: o.ID}] = o.Owner
	}
	return owned
}

// stagingDir is where an incremental import into dir generates what it
// adds, a directory of its own under the output's checkpoints, which
// engine.Add empties: at dir's path relative to the output, or, as
// --layout need not put dir under the output, at a hash of dir's
// path when the relative path would lead out of there.
func stagingDir(out, dir string) string {
	base := filepath.Join(out, CheckpointDir, "incremental")
	if rel, err := filepath.Rel(out, dir); err == nil && rel != "." && !filepath.IsAbs(rel) {
		staging := filepath.Join(base, rel)
		inside, err := filepath.Rel(base, staging)
		if err == nil && inside != "." && inside != ".." && !strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
			return staging
		}
	}
	sum := sha256.Sum256([]byte(filepath.ToSlash(filepath.Clean(dir))))
	return filepath.Join(base, hex.EncodeToString(sum[:])[:16])
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

// InOtherRootReason is why an incremental import leaves out a resource
// another root of the output imports, which the reason names after it.
const InOtherRootReason = "already in another root of the output"

// heldElsewhere returns, by type and import ID, the root, relative to
// out, that imports each resource the checkpoints under out record for a
// root other than those of byDir: a resource goes into one root only, so
// an incremental import leaves it out rather than import it again, as
// when the root an import puts a type in has changed since the root that
// has it was generated. Only roots that still have their import blocks
// count, and only what they import: a resource one left out may be
// generated in another. A checkpoint that can't be read is logged and
// passed over.
func heldElsewhere(out string, byDir map[string][]engine.Import) (map[engine.External]string, error) {
	base := filepath.Join(out, CheckpointDir, "checkpoints")
	if _, err := os.Stat(base); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	own := map[string]bool{}
	for dir := range byDir {
		own[filepath.Clean(checkpointPath(out, dir))] = true
	}
	held := map[engine.External]string{}
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".json" || own[filepath.Clean(path)] {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		dir := filepath.Join(out, strings.TrimSuffix(rel, ".json"))
		if ok, err := fileExists(filepath.Join(dir, engine.ImportsFileName)); err != nil || !ok {
			return err
		}
		c, err := readCheckpoint(out, dir)
		if err != nil {
			log.Printf("%s: %v; what that root imports isn't left out of the others (--incremental)", relativePath(out, dir), err)
			return nil
		}
		if c == nil || c.Result == nil {
			return nil
		}
		root := relativePath(out, dir)
		for _, imp := range c.Result.Imported {
			key := engine.External{Type: imp.Type, ID: imp.ID}
			if _, ok := held[key]; !ok {
				held[key] = root
			}
		}
		return nil
	})
	return held, err
}

// leaveOutHeld removes from byDir the imports of what another root holds
// (see heldElsewhere), recording each as excluded, with the root, and
// drops the roots it leaves with nothing to import.
func (r *Run) leaveOutHeld(byDir map[string][]engine.Import, held map[engine.External]string) {
	if len(held) == 0 {
		return
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		var kept []engine.Import
		for _, imp := range byDir[dir] {
			if root, ok := held[engine.External{Type: imp.Type, ID: imp.ID}]; ok {
				r.report.Excluded = append(r.report.Excluded, report.Excluded{Type: imp.Type, ID: imp.ID, Reason: InOtherRootReason + ", " + root, Scope: r.scope})
				continue
			}
			kept = append(kept, imp)
		}
		if len(kept) == 0 {
			log.Printf("%s: each of its resources is in another root of the output (--incremental)", dir)
			delete(byDir, dir)
			continue
		}
		byDir[dir] = kept
	}
}
