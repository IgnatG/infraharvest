// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/report"
)

// addToRoot adds to dir, a root an earlier import generated in full (see
// listedCall.decideRoot), the resources of imports it doesn't have yet
// (see engine.Existing and engine.Add), generating them in a staging
// directory under the output's checkpoints.
// It records the others as excluded, and what the root has that is no
// longer in the cloud (see listedHere.notInCloud), and returns the
// resources it added, the result, and what the root then holds for its
// checkpoint, with its README, which names what is gone too. Neither
// result is set if nothing is new. On an error, the resources returned
// are those the error failed: every one of imports until it knows which
// are new.
func (r *Run) addToRoot(ctx context.Context, dir string, imports []rootfile.Import, opts engine.Options, tr terraformRun) ([]rootfile.Import, *engine.Result, *engine.Result, error) {
	out := r.options.Out
	previous, err := checkpointResult(out, dir)
	if err != nil {
		return imports, nil, nil, err
	}
	existing, err := engine.Existing(dir, previous)
	if err != nil {
		return imports, nil, nil, err
	}
	// What the root has that the listing didn't find, where it lists all
	// there is, is gone: the root's next plan would create it again.
	gone := r.here.notInCloud(existing)
	r.recordNotInCloud(dir, gone)
	if len(existing) == 0 && len(r.options.ManagedState) == 0 {
		return imports, nil, nil, fmt.Errorf("%s has configuration, but neither its import blocks nor a checkpoint of the import that generated it say which resources it has: pass --managed-state with its state, so that the import leaves them out", dir)
	}
	owned := previouslyOwned(previous, opts)
	var added []rootfile.Import
	for _, imp := range imports {
		key := engine.ResourceKey(imp.Type, imp.ID)
		if _, ok := existing[key]; ok {
			r.report.Exclude(r.scope, imp.Type, imp.ID, report.InRoot())
			continue
		}
		if owner, ok := owned[key]; ok {
			r.report.Exclude(r.scope, imp.Type, imp.ID, ownedByTags(owner))
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
	if content, err := os.ReadFile(filepath.Join(dir, tfrun.LockFileName)); err == nil {
		lock = content
	}
	if lock != nil {
		if err := os.WriteFile(filepath.Join(staging, tfrun.LockFileName), lock, 0o644); err != nil {
			return added, nil, nil, err
		}
	}
	rootTF, err := tr.in(dir)
	if err != nil {
		return added, nil, nil, err
	}
	result, err := r.tools.add(ctx, tf, rootTF, staging, dir, added, opts, existing)
	if err != nil {
		// What staging generated may hold secrets in the clear: Add removes
		// it only once it is done.
		return added, nil, nil, errors.Join(err, os.RemoveAll(staging))
	}
	if r.lock == nil {
		r.lock, _ = os.ReadFile(filepath.Join(dir, tfrun.LockFileName))
	}
	holds := previous.With(result)
	if previous != nil {
		// Without the earlier result, the root's README stays as it is.
		holds.NotInCloud = gone
		if err := r.tools.writeReadme(dir, holds); err != nil {
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
		typ := engine.AddressType(o.Address)
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

// leaveOutHeld removes from byDir the imports of what another root holds
// (see outputIndex.heldElsewhere), recording each as excluded, with the root, and
// drops the roots it leaves with nothing to import.
func (r *Run) leaveOutHeld(byDir map[string][]rootfile.Import, held map[engine.External]string) {
	if len(held) == 0 {
		return
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		var kept []rootfile.Import
		for _, imp := range byDir[dir] {
			if root, ok := held[engine.ResourceKey(imp.Type, imp.ID)]; ok {
				r.report.Exclude(r.scope, imp.Type, imp.ID, report.InOtherRoot(root))
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
