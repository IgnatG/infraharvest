// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/IgnatG/infraharvest/internal/tftypes"
	"github.com/IgnatG/infraharvest/internal/version"
)

// fingerprint identifies what a root is generated from: infraharvest's
// version, the engine, the imports, the files written besides them, and
// the options that change the output.
func fingerprint(engineVersion string, imports []engine.Import, opts engine.Options) (string, error) {
	// Imports come in listing order, which varies between runs.
	imports = append([]engine.Import(nil), imports...)
	sort.Slice(imports, func(i, j int) bool {
		if imports[i].Type != imports[j].Type {
			return imports[i].Type < imports[j].Type
		}
		return imports[i].ID < imports[j].ID
	})
	adapters := make([]string, 0, len(opts.Adapters))
	for _, a := range opts.Adapters {
		adapters = append(adapters, a.Source+"@"+a.Version)
	}
	content, err := json.Marshal(struct {
		Version, Engine string
		Imports         []engine.Import
		Config          map[string][]byte
		Omit, Defaults  map[string][]string
		StateOnly       map[string][]string
		KeepEmpty       map[string][]string
		DefaultTags     *engine.DefaultTags
		EffectiveTags   *engine.EffectiveTags
		Inherited       []string
		Modules         bool
		Adapters        []string
		External        []engine.External
		DataSources     map[string]engine.DataSource
		Placeholders    map[string]engine.Placeholder
		Lifecycles      map[string]engine.Lifecycle
		IncludeOwned    bool
		KeepOwned       map[string]bool
		OtherRoots      []engine.RootResource
		ARNs            map[string]engine.ARN
		// Refine is what --cfn-stack carries into the root; empty, it
		// leaves the fingerprint of a root without it as it was.
		Refine string `json:",omitempty"`
	}{version.Version, engineVersion, imports, opts.Config, opts.Omit, opts.Defaults, opts.StateOnly, opts.KeepEmpty, opts.DefaultTags, opts.EffectiveTags, opts.Inherited, opts.ModulesDir != "", adapters, opts.External, opts.DataSources, opts.Placeholders, opts.Lifecycles, opts.IncludeOwned, opts.KeepOwned, opts.OtherRoots, opts.ARNs, opts.RefineKey})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

// claim is a root claimRoot got ready for generating.
type claim struct {
	// previous are where the earlier import that generated the root put
	// its resources (see previousAddresses); none for a new root.
	previous []engine.Managed
	// labels are the labels that import gave them, by type and import
	// ID, and moved the moved blocks its moves.tf had (see
	// engine.Previous).
	labels map[engine.External]string
	moved  []engine.Move
	// removed are the removed blocks its moves.tf had, with those of the
	// resources the root hands over to other roots now (see
	// planHandovers).
	removed []engine.Removal
	// backup is the root as it was before, if it has previous, to put
	// back when generating it changes what moved blocks can't follow (see
	// refuseReaddressed).
	backup *rootBackup
	// modules counts the users of the generated modules the root takes
	// up, which putting it back releases; nil counts none.
	modules *engine.ModuleRefs
}

// previousRoot returns what the earlier import generated in the root, for
// generating it again; nil for a new root.
func (c *claim) previousRoot() *engine.Previous {
	if len(c.previous) == 0 && len(c.moved) == 0 && len(c.removed) == 0 {
		return nil
	}
	return &engine.Previous{Addresses: c.previous, Labels: c.labels, Moved: c.moved, Removed: c.removed}
}

// claimRoot gets dir ready for generating a root from fp, as d, a
// decision to generate it afresh or again, says (see
// rootDecision.generated). A fresh root gets a checkpoint. Generating a
// root an earlier import generated again, from what d found of it on
// disk, first records where its resources were (see previousAddresses,
// and shared.previousAddressesOf for what s has of its own state) in its
// checkpoint, then removes what the import generated (see
// clearGenerated). The checkpoint then has no result: it records that
// the root's generation started, so that a later run knows the files are
// infraharvest's even if this one fails, and that the root isn't
// complete (see unfinishedRoot).
func claimRoot(out, dir, fp string, d rootDecision, s *shared) (*claim, error) {
	claimed := &claim{}
	if s != nil {
		claimed.modules = &s.modules
	}
	if d.mode == rootFresh {
		return claimed, saveCheckpoint(out, dir, fp, nil)
	}
	onDisk := d.onDisk
	c := onDisk.c
	var err error
	if onDisk.readable {
		if claimed.previous, err = s.previousAddressesOf(out, dir); err != nil {
			return nil, err
		}
		// A run that stopped while generating the root again removed
		// moves.tf, and may have written another.
		claimed.moved, claimed.removed = c.PendingMoves, c.PendingRemoved
		if c.Result != nil {
			if claimed.moved, err = engine.ReadMoves(dir); err != nil {
				return nil, err
			}
			if claimed.removed, err = readRemoved(dir, c.Result); err != nil {
				return nil, err
			}
			claimed.labels = map[engine.External]string{}
			for _, imp := range c.Result.Imported {
				claimed.labels[engine.ResourceKey(imp.Type, imp.ID)] = imp.Name
			}
		}
	}
	if len(claimed.previous) > 0 {
		if claimed.backup, err = backupRoot(dir, checkpointPath(out, dir), onDisk.content); err != nil {
			return nil, err
		}
	}
	// Before anything is removed: the files to remove are the earlier
	// result's, which this checkpoint replaces.
	started := checkpoint{Fingerprint: fp, Pending: claimed.previous, PendingFiles: c.PendingFiles, PendingMoves: claimed.moved, PendingRemoved: claimed.removed}
	if c.Result != nil {
		started.PendingFiles = c.Result.Files
	}
	if err := writeCheckpoint(out, dir, started); err != nil {
		return nil, err
	}
	return claimed, clearGenerated(out, dir)
}

// readRemoved returns the removed blocks of dir's moves.tf, with what
// result, the root's last, says each hands over.
func readRemoved(dir string, result *engine.Result) ([]engine.Removal, error) {
	removed, err := engine.ReadRemoved(dir)
	if err != nil {
		return nil, err
	}
	for i, r := range removed {
		for _, had := range result.Removed {
			if had.From == r.From {
				removed[i].Resources = had.Resources
			}
		}
	}
	return removed, nil
}

// tfFiles returns the names of the .tf files of dir.
func tfFiles(dir string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(paths))
	for _, path := range paths {
		names = append(names, filepath.Base(path))
	}
	return names, nil
}

// previousAddresses returns where the earlier import that generated dir,
// a root of out, put its resources, by type and import ID: what the
// checkpoint of a run that stopped while generating the root again kept
// (see checkpoint.Pending); else the addresses the checkpoint's result
// records; else, for a result from an older release, where the root's
// import blocks import; else, once they are deleted, what the result
// says it imported, unverified (see engine.InferredAddresses). A root
// whose import didn't finish has none. It makes no cloud call.
func previousAddresses(out, dir string) ([]engine.Managed, error) {
	c, err := readCheckpoint(out, dir)
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
	imported, err := engine.ImportedAddresses(dir)
	if err != nil || len(imported) > 0 {
		return imported, err
	}
	return engine.InferredAddresses(c.Result), nil
}

// recordStated records m, where the root in dir has a resource as its own
// state says (see excludeManaged).
func (s *shared) recordStated(dir string, m engine.Managed) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stated == nil {
		s.stated = map[string][]engine.Managed{}
	}
	dir = filepath.Clean(dir)
	for _, had := range s.stated[dir] {
		if had.Type == m.Type && had.ID == m.ID {
			return
		}
	}
	s.stated[dir] = append(s.stated[dir], m)
}

// previousAddressesOf returns previousAddresses for the root in dir, with
// where its own state has its resources, if --managed-state read it (see
// excludeManaged), in place of the addresses only inferred, and with the
// resources the state has that the root's checkpoint doesn't say: the
// state's addresses are the ones a plan on it starts from. A nil s reads
// no state.
func (s *shared) previousAddressesOf(out, dir string) ([]engine.Managed, error) {
	previous, err := previousAddresses(out, dir)
	if err != nil || s == nil {
		return previous, err
	}
	s.mu.Lock()
	stated := s.stated[filepath.Clean(dir)]
	s.mu.Unlock()
	return withStated(previous, stated), nil
}

// withStated returns previous with the addresses of stated, where a root's
// own state has its resources, in place of those previous only infers,
// and with those of stated it hasn't, sorted by type and import ID.
func withStated(previous, stated []engine.Managed) []engine.Managed {
	if len(stated) == 0 {
		return previous
	}
	at := map[engine.External]engine.Managed{}
	for _, m := range stated {
		at[engine.ResourceKey(m.Type, m.ID)] = m
	}
	merged := make([]engine.Managed, 0, len(previous)+len(stated))
	for _, p := range previous {
		key := engine.ResourceKey(p.Type, p.ID)
		if m, ok := at[key]; ok {
			if p.Unverified {
				p = m
			}
			delete(at, key)
		}
		merged = append(merged, p)
	}
	for _, m := range stated {
		if _, ok := at[engine.ResourceKey(m.Type, m.ID)]; ok {
			merged = append(merged, m)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Type != merged[j].Type {
			return merged[i].Type < merged[j].Type
		}
		return merged[i].ID < merged[j].ID
	})
	return merged
}

// refuseReaddressed fails when result, of generating the claimed root
// again from imports, would change what the root's state has in a way
// no moved block of result follows, naming what: resources of it at
// other addresses than before that result doesn't move (see
// engine.Readdressed and engine.Result.Unmoved), resources of another
// type now (see engine.Retyped), resources it no longer has that
// elsewhere, by type and import ID, puts in another root of the run, by
// its path, unless a removed block of result hands them over (see
// planHandovers), and resources of imports it leaves out, such as those
// its plan rejected; each by its type or another name of it (see
// tftypes.Aliases), which result forgets and imports again (see
// engine.Retype). Applied on the root's state, its next plan would
// destroy them, and create them where they go. The root is put back as
// it was, without the generated modules it took up (see
// engine.ModuleRefs.Release). A resource the run no longer imports at all,
// such as one deleted in the cloud since, isn't counted.
func (c *claim) refuseReaddressed(dir string, result *engine.Result, imports []engine.Import, elsewhere map[engine.External]string) error {
	changed := engine.Readdressed(c.previous, result.Addresses, result.Moves)
	changed = append(changed, engine.Retyped(c.previous, result.Addresses)...)
	now := map[engine.External]bool{}
	for _, m := range result.Addresses {
		now[engine.ResourceKey(m.Type, m.ID)] = true
	}
	asked := map[engine.External]bool{}
	for _, imp := range imports {
		asked[engine.ResourceKey(imp.Type, imp.ID)] = true
	}
	handed := map[engine.External]bool{}
	for _, r := range result.Removed {
		for _, d := range r.Resources {
			handed[engine.ResourceKey(d.Type, d.ID)] = true
		}
	}
	for _, p := range c.previous {
		if handed[engine.ResourceKey(p.Type, p.ID)] {
			continue
		}
		var kept, left bool
		root := ""
		for _, typ := range append([]string{p.Type}, tftypes.Aliases(p.Type)...) {
			key := engine.External{Type: typ, ID: p.ID}
			kept = kept || now[key]
			left = left || asked[key]
			if to, ok := elsewhere[key]; ok && root == "" {
				root = to
			}
		}
		switch {
		case kept:
		case root != "":
			changed = append(changed, fmt.Sprintf("%s is now in %s", p.Address, root))
		case left:
			changed = append(changed, p.Address+" is left out")
		}
	}
	if len(changed) == 0 && len(result.Unmoved) == 0 {
		return nil
	}
	sort.Strings(changed)
	err := fmt.Errorf("generating %s again would move or leave out %d of its resources where no moved block can follow them, which a plan on its state would destroy, and create where they go, so it is left as it was: %s", dir, len(changed), strings.Join(append(changed, result.Unmoved...), "; "))
	return errors.Join(err, c.putBack(dir))
}

// putBack puts the claimed root in dir back as it was before it was
// generated again, without the generated modules it took up since.
func (c *claim) putBack(dir string) error {
	var err error
	if c.modules != nil {
		err = c.modules.Release(dir)
	}
	if c.backup != nil {
		err = errors.Join(err, c.backup.restore())
	}
	return err
}

// rootBackup is what a root had before it was generated again: the files
// directly in its directory, but its local state (see
// engine.LocalStateFile), and its checkpoint. The state is Terraform's,
// which may write it in between, so it is never written back or removed.
type rootBackup struct {
	dir   string
	files map[string]savedFile
	// checkpoint is the checkpoint's path, and content what it held.
	checkpoint string
	content    []byte
}

type savedFile struct {
	content []byte
	mode    fs.FileMode
}

// backupRoot returns what dir has, and its checkpoint at path, which
// holds content.
func backupRoot(dir, path string, content []byte) (*rootBackup, error) {
	b := &rootBackup{dir: dir, files: map[string]savedFile{}, checkpoint: path, content: content}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || engine.LocalStateFile(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		saved, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		b.files[e.Name()] = savedFile{content: saved, mode: info.Mode().Perm()}
	}
	return b, nil
}

// restore puts the root back as b has it: the files written directly in
// its directory since are removed, but local state, the others written
// back, and so is the checkpoint.
func (b *rootBackup) restore() error {
	entries, err := os.ReadDir(b.dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if _, ok := b.files[e.Name()]; ok || !e.Type().IsRegular() || engine.LocalStateFile(e.Name()) {
			continue
		}
		if err := os.Remove(filepath.Join(b.dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	names := make([]string, 0, len(b.files))
	for name := range b.files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f := b.files[name]
		if err := fsutil.WriteFile(filepath.Join(b.dir, name), f.content, f.mode); err != nil {
			return err
		}
	}
	return fsutil.WriteFile(b.checkpoint, b.content, 0o644)
}
