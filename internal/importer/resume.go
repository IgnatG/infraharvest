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
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/version"
)

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
	}{version.Version, engineVersion, imports, opts.Config, opts.Omit, opts.Defaults, opts.StateOnly, opts.KeepEmpty, opts.DefaultTags, opts.EffectiveTags, opts.Inherited, opts.ModulesDir != "", adapters, opts.External, opts.DataSources, opts.Placeholders, opts.Lifecycles, opts.IncludeOwned, opts.KeepOwned, opts.OtherRoots, opts.ARNs})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

func checkpointPath(out, dir string) string {
	return filepath.Join(out, CheckpointDir, "checkpoints", filepath.FromSlash(relativePath(out, dir))+".json")
}

// resumed returns the result a checkpoint recorded for dir, if it has the
// same fingerprint and the root's files are still there; nil otherwise.
// Those are its configuration and versions.tf, with imports.tf and, if
// the result moves or removes resources, moves.tf, or without both: once
// the root is applied, its README says to delete them.
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
	names := append([]string{engine.VersionsFileName}, configurationFiles(c.Result)...)
	// A root its stack's merge set aside is resumed from there.
	dir = rootSource(out, dir)
	imports, err := fileExists(filepath.Join(dir, engine.ImportsFileName))
	if err != nil {
		return nil, err
	}
	if imports && (len(c.Result.Moves) > 0 || len(c.Result.Removed) > 0 || len(c.Result.Retyped) > 0) {
		names = append(names, engine.MovesFileName)
	}
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return nil, nil // a missing file means generating again
		}
	}
	return c.Result, nil
}

// configurationFiles are the files a result's configuration is in: the
// files it was split into, or generated.tf.
func configurationFiles(result *engine.Result) []string {
	if result != nil && len(result.Files) > 0 {
		return result.Files
	}
	return []string{engine.GeneratedFileName}
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

// generatedFiles are the files a previous run wrote into a root, which
// --resume removes before generating the root again.
var generatedFiles = []string{
	engine.GeneratedFileName, engine.ImportsFileName, engine.RejectedFileName, engine.LocalsFileName,
	engine.VariablesFileName, engine.DataFileName, engine.ReadmeFileName, BackendFileName, engine.MovesFileName,
}

// clearGenerated removes what a previous run generated in dir, so that the
// root can be generated again: generatedFiles, the files its checkpoint
// under out says the configuration was split into, and the files
// --incremental added (see engine.AddedFileName), which the checkpoint
// names too.
func clearGenerated(out, dir string) error {
	names := slices.Clone(generatedFiles)
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
	added, err := filepath.Glob(filepath.Join(dir, "generated_*.tf"))
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
}

// previousRoot returns what the earlier import generated in the root, for
// generating it again; nil for a new root.
func (c *claim) previousRoot() *engine.Previous {
	if len(c.previous) == 0 && len(c.moved) == 0 && len(c.removed) == 0 {
		return nil
	}
	return &engine.Previous{Addresses: c.previous, Labels: c.labels, Moved: c.moved, Removed: c.removed}
}

// claimRoot gets dir ready for generating a root from fp. A directory no
// checkpoint under out says an import wrote must hold no .tf files, so
// that an unrelated root is never overwritten. A root an earlier import
// generated, which may be in use, is generated again only with
// regenerate or, if that import didn't finish, with resume, unless no .tf
// file of it is left; else claimRoot refuses it. Generating it again
// first records where its resources were (see previousAddresses) in its
// checkpoint, then removes what the import generated (see
// clearGenerated). The checkpoint then has no result: it records that
// the root's generation started, so that a later run knows the files are
// infraharvest's even if this one fails, and that the root isn't
// complete (see unfinishedRoot).
func claimRoot(out, dir, fp string, resume, regenerate bool) (*claim, error) {
	names, err := tfFiles(dir)
	if err != nil {
		return nil, err
	}
	path := checkpointPath(out, dir)
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if len(names) > 0 {
			return nil, fmt.Errorf("%s already holds %s, which no import by infraharvest wrote: use an empty output directory, or --incremental to add to a root an import generated", dir, strings.Join(names, ", "))
		}
		return &claim{}, saveCheckpoint(out, dir, fp, nil)
	}
	if err != nil {
		return nil, err
	}
	var c checkpoint
	readable := json.Unmarshal(content, &c) == nil
	if !readable {
		c = checkpoint{} // as of a root whose import didn't finish
	}
	finished := c.Result != nil
	if len(names) > 0 && !regenerate && (finished || !resume) {
		if finished {
			return nil, fmt.Errorf("%s is a root an earlier import generated, which may be in use: pass --regenerate to generate it again, or --incremental to add what is new to it", dir)
		}
		return nil, fmt.Errorf("the import that generated %s didn't finish: pass --resume to generate it again", dir)
	}
	claimed := &claim{}
	if readable {
		if claimed.previous, err = previousAddresses(out, dir); err != nil {
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
				claimed.labels[engine.External{Type: imp.Type, ID: imp.ID}] = imp.Name
			}
		}
	}
	if len(claimed.previous) > 0 {
		if claimed.backup, err = backupRoot(dir, path, content); err != nil {
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
// engine.AliasTypes), which result forgets and imports again (see
// engine.Retype). Applied on the root's state, its next plan would
// destroy them, and create them where they go. The root is put back as
// it was, without the generated modules it took up (see
// engine.ReleaseModules). A resource the run no longer imports at all,
// such as one deleted in the cloud since, isn't counted.
func (c *claim) refuseReaddressed(dir string, result *engine.Result, imports []engine.Import, elsewhere map[engine.External]string) error {
	changed := engine.Readdressed(c.previous, result.Addresses, result.Moves)
	changed = append(changed, engine.Retyped(c.previous, result.Addresses)...)
	now := map[engine.External]bool{}
	for _, m := range result.Addresses {
		now[engine.External{Type: m.Type, ID: m.ID}] = true
	}
	asked := map[engine.External]bool{}
	for _, imp := range imports {
		asked[engine.External{Type: imp.Type, ID: imp.ID}] = true
	}
	handed := map[engine.External]bool{}
	for _, r := range result.Removed {
		for _, d := range r.Resources {
			handed[engine.External{Type: d.Type, ID: d.ID}] = true
		}
	}
	for _, p := range c.previous {
		if handed[engine.External{Type: p.Type, ID: p.ID}] {
			continue
		}
		var kept, left bool
		root := ""
		for _, typ := range append([]string{p.Type}, engine.AliasTypes(p.Type)...) {
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
	err := engine.ReleaseModules(dir)
	if c.backup != nil {
		err = errors.Join(err, c.backup.restore())
	}
	return err
}

// rootBackup is what a root had before it was generated again: the files
// directly in its directory, and its checkpoint.
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
		if !e.Type().IsRegular() {
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
// its directory since are removed, the others written back, and so is
// the checkpoint.
func (b *rootBackup) restore() error {
	entries, err := os.ReadDir(b.dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if _, ok := b.files[e.Name()]; ok || !e.Type().IsRegular() {
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

// unfinishedRoot reports whether dir is what an import that didn't finish
// generating it left: its checkpoint records that the generation started
// (see claimRoot) and no result or, without a checkpoint, the root has
// generated.tf but not the README written last.
func unfinishedRoot(out, dir string) (bool, error) {
	c, err := readCheckpoint(out, dir)
	if err != nil {
		return false, err
	}
	if c != nil {
		return c.Result == nil, nil
	}
	return unfinishedFiles(dir)
}

// unfinishedFiles reports whether dir has generated.tf but no README.
func unfinishedFiles(dir string) (bool, error) {
	generated, err := fileExists(filepath.Join(dir, engine.GeneratedFileName))
	if err != nil || !generated {
		return false, err
	}
	readme, err := fileExists(filepath.Join(dir, engine.ReadmeFileName))
	return !readme, err
}

// fileExists reports whether path exists.
func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
