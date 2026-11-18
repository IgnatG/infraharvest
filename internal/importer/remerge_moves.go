// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine/envmerge"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/logging"
)

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
		if name == rootfile.MovesFileName {
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
func mergedMoves(before, now map[string]map[string]string, kept []rootfile.Move) ([]rootfile.Move, string) {
	to := map[string]string{}
	// The environment each address moves in, and one it stays in.
	movedIn, staysIn := map[string]string{}, map[string]string{}
	for _, m := range kept {
		to[m.From] = m.To
		movedIn[m.From] = rootfile.MovesFileName
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
			moves = append(moves, rootfile.Move{From: address, To: after})
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
func (p *mergePhase) moveMerged(stack string, result *envmerge.MergeResult, before, now map[string]map[string]string, kept []rootfile.Move, removed []rootfile.Removal) (string, error) {
	moved := readdressed(before, now)
	if len(moved) > 0 && !p.regenerate {
		return "merging it again would give what the merged root has other addresses: " + strings.Join(moved, "; "), nil
	}
	moves, why := mergedMoves(before, now, kept)
	if why != "" {
		return "merging it again would give what the merged root has other addresses, and " + why, nil
	}
	if len(moves) == 0 && len(removed) == 0 {
		return "", nil
	}
	result.Files = append(result.Files, rootfile.MovesFileName)
	sort.Strings(result.Files)
	check, err := p.tools.writeMergedMoves(stack, moves, removed)
	if err != nil {
		return "", err
	}
	if !check.Passed {
		return "merging it again would give what the merged root has other addresses, or hand it over to other roots, and the moved and removed blocks fail the moves check: " + strings.Join(check.Details, "; "), nil
	}
	if len(moved) > 0 {
		logging.Warnf("%s: moved blocks in %s move what has other addresses now (--regenerate): %s", relativePath(p.out, stack), rootfile.MovesFileName, strings.Join(moved, "; "))
	}
	return "", nil
}

// clearMerged removes files, those envmerge.MergeRoots wrote in stack, with
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
func (p *mergePhase) readdressNotInCloud(path string, members []mergeMember, imports map[string]map[string]string) map[string][]rootfile.Import {
	envOf := map[string]string{}
	for _, m := range members {
		envOf[relativePath(p.out, m.dir)] = m.env
	}
	return p.report.ReaddressNotInCloud(path, envOf, imports)
}

// goneFrom returns the addresses of the resources of the root in dir an
// incremental import found no longer in the cloud (see addToRoot), sorted:
// merged again, its stack keeps them as the root has them, and plans
// without importing them (see envmerge.MergeRoot.Gone).
func (p *mergePhase) goneFrom(dir string) []string {
	return p.report.NotInCloudAt(relativePath(p.out, dir))
}

// treeBackup is what a directory held: each file under it, but in
// Terraform's working directory and local state (see
// tfrun.LocalStateFile), such as its workspaces' in terraform.tfstate.d,
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
		if d.IsDir() && (d.Name() == ".terraform" || tfrun.LocalStateFile(d.Name())) {
			return filepath.SkipDir
		}
		if tfrun.LocalStateFile(d.Name()) {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		content, err := os.ReadFile(path) //nolint:gosec // G122: walks the user's own directory and reads regular files; no untrusted concurrent writer
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
