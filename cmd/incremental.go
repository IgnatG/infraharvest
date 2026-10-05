// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/IgnatG/infraharvest/engine"
	"github.com/IgnatG/infraharvest/report"
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
func (r *engineRun) addToRoot(ctx context.Context, dir string, imports []engine.Import, opts engine.Options, execPath, pluginCacheDir string) ([]engine.Import, *engine.Result, *engine.Result, error) {
	out := r.options.PathOutput
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
	var added []engine.Import
	for _, imp := range imports {
		if _, ok := existing[engine.External{Type: imp.Type, ID: imp.ID}]; ok {
			r.report.Excluded = append(r.report.Excluded, report.Excluded{Type: imp.Type, ID: imp.ID, Reason: InRootReason})
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
	tf, err := engine.NewTerraform(staging, execPath, pluginCacheDir)
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
	rootTF, err := engine.NewTerraform(dir, execPath, pluginCacheDir)
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

// stagingDir is where an incremental import into dir generates what it
// adds, a directory of its own under the output's checkpoints, which
// engine.Add empties: at dir's path relative to the output, or, as
// --path-pattern need not put dir under the output, at a hash of dir's
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
	content, err := os.ReadFile(checkpointPath(out, dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c checkpoint
	if err := json.Unmarshal(content, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", checkpointPath(out, dir), err)
	}
	return c.Result, nil
}
