// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"fmt"
	"log"
	"path/filepath"
	"sync"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// outputSource is where an outputIndex reads what earlier imports left in
// the output directory: the checkpoints of the roots they generated and of
// the merged roots environments share (see diskOutput), and what the
// roots' directories hold. Directories are the roots' paths under the
// output. A missing checkpoint or file is no error: the answer is nil.
type outputSource interface {
	// roots returns the directories of the roots the checkpoints record,
	// sorted.
	roots() ([]string, error)
	// recorded reports whether a checkpoint records a root in dir, however
	// readable.
	recorded(dir string) (bool, error)
	// checkpoint returns the checkpoint of the root in dir; an error if
	// it can't be read.
	checkpoint(dir string) (*checkpoint, error)
	// configured reports whether dir still has configuration of its own.
	configured(dir string) (bool, error)
	// importBlocks returns where the import blocks of dir's root import
	// its resources, none once they are deleted.
	importBlocks(dir string) ([]engine.Managed, error)
	// moves and removed return the moved and removed blocks of dir's
	// moves.tf.
	moves(dir string) ([]rootfile.Move, error)
	removed(dir string) ([]rootfile.Removal, error)

	// stacks returns the directories of the merged roots the merge
	// checkpoints record, sorted.
	stacks() ([]string, error)
	// mergeCheckpoint returns the merge checkpoint of the merged root in
	// stack.
	mergeCheckpoint(stack string) (*mergeCheckpoint, error)

	// mergedRoots returns the directories of the roots environments share
	// that the files tell, whether a merge checkpoint records them or
	// not, sorted (see hclfile.MergedEnvironments).
	mergedRoots() ([]string, error)
	// mergedEnvironments returns the environments of one, and
	// mergedStateLocations where the state of one of them is (see
	// envmerge.MergedStateLocations).
	mergedEnvironments(dir string) ([]string, error)
	mergedStateLocations(dir, env string) ([]string, error)
}

// outputIndex answers, for one scope of a run, what the output directory
// holds of what earlier imports generated: which root holds a resource
// (see heldElsewhere), what each root imported before (see
// indexedRoot.previous), which merged roots environments share, and the
// moved blocks a root may not have applied yet. It covers the roots of
// the output and the merged roots alike, and reads what it is asked once,
// from its source, as it was when it first read it: it is not the root's
// own record, which the root's generation writes (see rootCheckpoints).
// Safe for concurrent use.
type outputIndex struct {
	out string
	src outputSource

	roots, stacks, merged lazy[[]string]

	mu sync.Mutex
	at map[string]*indexedRoot
}

// newOutputIndex returns an index of what src has of out.
func newOutputIndex(out string, src outputSource) *outputIndex {
	return &outputIndex{out: out, src: src, at: map[string]*indexedRoot{}}
}

// newDiskIndex returns an index of what the checkpoints and roots under
// out hold.
func newDiskIndex(out string) *outputIndex {
	return newOutputIndex(out, diskOutput{out: out})
}

// lazy is a value read once, when first asked for, with the error that
// reading it gave.
type lazy[T any] struct {
	once sync.Once
	v    T
	err  error
}

func (l *lazy[T]) get(read func() (T, error)) (T, error) {
	l.once.Do(func() { l.v, l.err = read() })
	return l.v, l.err
}

// indexedRoot is what an outputIndex has of the root in dir, plain or
// merged: each part is read when first asked for.
type indexedRoot struct {
	x   *outputIndex
	dir string

	is           lazy[bool]
	checkpointed lazy[*checkpoint]
	merge        lazy[*mergeCheckpoint]
	config       lazy[bool]
	imported     lazy[[]engine.Managed]
	moved        lazy[[]rootfile.Move]
	removedFrom  lazy[[]rootfile.Removal]
	envs         lazy[[]string]
}

// root returns the index's record of the root in dir.
func (x *outputIndex) root(dir string) *indexedRoot {
	dir = filepath.Clean(dir)
	x.mu.Lock()
	defer x.mu.Unlock()
	r, ok := x.at[dir]
	if !ok {
		r = &indexedRoot{x: x, dir: dir}
		x.at[dir] = r
	}
	return r
}

// recordedRoots returns the roots the checkpoints record, sorted.
func (x *outputIndex) recordedRoots() ([]*indexedRoot, error) {
	return x.listed(&x.roots, x.src.roots)
}

// mergedStacks returns the merged roots the merge checkpoints record,
// sorted.
func (x *outputIndex) mergedStacks() ([]*indexedRoot, error) {
	return x.listed(&x.stacks, x.src.stacks)
}

// mergedRoots returns the roots environments share that the files tell,
// whether a merge checkpoint records them or not, sorted.
func (x *outputIndex) mergedRoots() ([]*indexedRoot, error) {
	return x.listed(&x.merged, x.src.mergedRoots)
}

func (x *outputIndex) listed(l *lazy[[]string], list func() ([]string, error)) ([]*indexedRoot, error) {
	dirs, err := l.get(list)
	if err != nil {
		return nil, err
	}
	roots := make([]*indexedRoot, len(dirs))
	for i, dir := range dirs {
		roots[i] = x.root(dir)
	}
	return roots, nil
}

// recorded reports whether a checkpoint records the root.
func (r *indexedRoot) recorded() (bool, error) {
	return r.is.get(func() (bool, error) { return r.x.src.recorded(r.dir) })
}

// checkpoint returns the root's checkpoint; nil if it has none.
func (r *indexedRoot) checkpoint() (*checkpoint, error) {
	return r.checkpointed.get(func() (*checkpoint, error) { return r.x.src.checkpoint(r.dir) })
}

// mergeCheckpoint returns the checkpoint of the merged root; nil if it
// has none.
func (r *indexedRoot) mergeCheckpoint() (*mergeCheckpoint, error) {
	return r.merge.get(func() (*mergeCheckpoint, error) { return r.x.src.mergeCheckpoint(r.dir) })
}

// configured reports whether the root still has configuration: applied
// roots do, with their import blocks deleted.
func (r *indexedRoot) configured() (bool, error) {
	return r.config.get(func() (bool, error) { return r.x.src.configured(r.dir) })
}

// previous returns where the earlier import that generated the root put
// its resources, by type and import ID: what the checkpoint of a run that
// stopped while generating the root again kept (see checkpoint.Pending);
// else the addresses the checkpoint's result records; else, for a result
// from an older release, where the root's import blocks import; else,
// once they are deleted, what the result says it imported, unverified
// (see engine.InferredAddresses). A root whose import didn't finish has
// none, nor a root with no checkpoint. It makes no cloud call.
func (r *indexedRoot) previous() ([]engine.Managed, error) {
	return r.imported.get(func() ([]engine.Managed, error) {
		c, err := r.checkpoint()
		if err != nil || c == nil {
			return nil, err
		}
		switch {
		case len(c.Pending) > 0:
			return c.Pending, nil
		case c.Result == nil:
			return nil, nil
		case len(c.Result.Addresses) > 0:
			return c.Result.Addresses, nil
		}
		imported, err := r.x.src.importBlocks(r.dir)
		if err != nil || len(imported) > 0 {
			return imported, err
		}
		return engine.InferredAddresses(c.Result), nil
	})
}

// movesTF returns the moved blocks of the root's moves.tf.
func (r *indexedRoot) movesTF() ([]rootfile.Move, error) {
	return r.moved.get(func() ([]rootfile.Move, error) { return r.x.src.moves(r.dir) })
}

// removedTF returns the removed blocks of the root's moves.tf.
func (r *indexedRoot) removedTF() ([]rootfile.Removal, error) {
	return r.removedFrom.get(func() ([]rootfile.Removal, error) { return r.x.src.removed(r.dir) })
}

// pendingMoves returns the moved blocks the root may not have applied
// yet: those of its moves.tf or, while a run that stopped generates it
// again, those its checkpoint kept (see claimRoot).
func (r *indexedRoot) pendingMoves() ([]rootfile.Move, error) {
	c, err := r.checkpoint()
	if err != nil || c == nil {
		return nil, err
	}
	if c.Result == nil {
		return c.PendingMoves, nil
	}
	return r.movesTF()
}

// environments returns the environments of the root environments share.
func (r *indexedRoot) environments() ([]string, error) {
	return r.envs.get(func() ([]string, error) { return r.x.src.mergedEnvironments(r.dir) })
}

// stateLocations returns where the state of env, one of the root's
// environments, is.
func (r *indexedRoot) stateLocations(env string) ([]string, error) {
	return r.x.src.mergedStateLocations(r.dir, env)
}

// heldElsewhere returns, by type and import ID, the root, relative to
// out, that imports each resource the checkpoints record for a root other
// than those of byDir: a resource goes into one root only, so an
// incremental import leaves it out rather than import it again, as when
// the root an import puts a type in has changed since the root that has
// it was generated. Only roots that still have configuration count,
// applied ones too, with their import blocks deleted, and only what the
// checkpoint records them importing (see indexedRoot.previous): a
// resource one left out may be generated in another. A checkpoint that
// can't be read is logged and passed over. The same goes for the merged
// roots environments share, but those of whose roots byDir has one,
// whose import IDs come from their merge checkpoints.
func (x *outputIndex) heldElsewhere(byDir map[string][]rootfile.Import) (map[engine.External]string, error) {
	own := map[string]bool{}
	for dir := range byDir {
		own[relativePath(x.out, dir)] = true
	}
	roots, err := x.recordedRoots()
	if err != nil {
		return nil, err
	}
	held := map[engine.External]string{}
	for _, r := range roots {
		if own[relativePath(x.out, r.dir)] {
			continue
		}
		has, err := r.configured()
		if err != nil {
			return nil, err
		}
		if !has {
			continue
		}
		c, err := r.checkpoint()
		if err != nil {
			log.Printf("%s: %v; what that root imports isn't left out of the others (--incremental)", relativePath(x.out, r.dir), err)
			continue
		}
		if c == nil || c.Result == nil {
			continue
		}
		addresses, err := r.previous()
		if err != nil {
			return nil, err
		}
		root := relativePath(x.out, r.dir)
		for _, m := range addresses {
			holdFirst(held, engine.ResourceKey(m.Type, m.ID), root)
		}
	}
	stacks, err := x.mergedStacks()
	if err != nil {
		return nil, err
	}
	for _, s := range stacks {
		c, err := s.mergeCheckpoint()
		if err != nil {
			log.Printf("%s: %v; what that root imports isn't left out of the others (--incremental)", relativePath(x.out, s.dir), err)
			continue
		}
		if c == nil || !c.Merge.Merged {
			continue
		}
		if has, err := s.configured(); err != nil || !has {
			if err != nil {
				return nil, err
			}
			continue
		}
		if x.merges(c, byDir) {
			continue
		}
		for _, ids := range c.Imports {
			for address, id := range ids {
				holdFirst(held, engine.External{Type: engine.AddressType(address), ID: id}, c.Merge.Path)
			}
		}
	}
	return held, nil
}

// merges reports whether one of the roots c, a merge checkpoint, records
// as merged is one of byDir's.
func (x *outputIndex) merges(c *mergeCheckpoint, byDir map[string][]rootfile.Import) bool {
	for _, m := range c.Members {
		if _, own := byDir[filepath.Join(x.out, filepath.FromSlash(m.Path))]; own {
			return true
		}
	}
	return false
}

// generatedResources returns the roots earlier imports generated that
// still have configuration, and by type and import ID, the root that has
// each of their resources (see indexedRoot.previous), those of roots
// still being generated again too. A root whose checkpoint can't be read
// fails it, as does one whose resources can't be told: what that root has
// can't be told, and leaving any of it out would make a plan on its state
// destroy it.
func (x *outputIndex) generatedResources() (map[engine.External]string, []string, error) {
	roots, err := x.recordedRoots()
	if err != nil {
		return nil, nil, err
	}
	held := map[engine.External]string{}
	var dirs []string
	for _, r := range roots {
		has, err := r.configured()
		if err != nil {
			return nil, nil, err
		}
		if !has {
			continue
		}
		previous, err := r.previous()
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w: with --managed-state, generating roots again needs to know what each has, so that none of it is left out", relativePath(x.out, r.dir), err)
		}
		dirs = append(dirs, r.dir)
		for _, p := range previous {
			holdFirst(held, engine.ResourceKey(p.Type, p.ID), r.dir)
		}
	}
	return held, dirs, nil
}

// holdFirst records that root holds key, unless a root recorded before
// it does.
func holdFirst(held map[engine.External]string, key engine.External, root string) {
	if _, ok := held[key]; !ok {
		held[key] = root
	}
}
