// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"errors"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/envmerge"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// diskOutput is an outputSource that reads the checkpoints and roots under
// the output directory out.
type diskOutput struct {
	out string
}

func (d diskOutput) roots() ([]string, error) { return rootCheckpoints.dirs(d.out) }

func (d diskOutput) recorded(dir string) (bool, error) {
	return fileExists(rootCheckpoints.path(d.out, dir))
}

func (d diskOutput) checkpoint(dir string) (*checkpoint, error) {
	return rootCheckpoints.read(d.out, dir)
}

func (d diskOutput) configured(dir string) (bool, error) {
	names, err := tfFiles(dir)
	return len(names) > 0, err
}

func (d diskOutput) importBlocks(dir string) ([]engine.Managed, error) {
	return engine.ImportedAddresses(dir)
}

func (d diskOutput) moves(dir string) ([]rootfile.Move, error) { return rootfile.ReadMoves(dir) }

func (d diskOutput) removed(dir string) ([]rootfile.Removal, error) {
	return rootfile.ReadRemoved(dir)
}

func (d diskOutput) stacks() ([]string, error) { return mergeCheckpoints.dirs(d.out) }

func (d diskOutput) mergeCheckpoint(stack string) (*mergeCheckpoint, error) {
	return mergeCheckpoints.read(d.out, stack)
}

// mergedRoots returns the directories under out that hold a root
// environments share (see hclfile.MergedEnvironments), sorted: outside
// the checkpoints, Terraform's working directories and local workspaces.
func (d diskOutput) mergedRoots() ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(d.out, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !e.IsDir() {
			return nil
		}
		switch e.Name() {
		case CheckpointDir, ".terraform", "terraform.tfstate.d", ".git", "vars":
			if path != d.out {
				return filepath.SkipDir
			}
		}
		envs, err := hclfile.MergedEnvironments(path)
		if err != nil || len(envs) == 0 {
			return err
		}
		if names, err := tfFiles(path); err != nil || len(names) == 0 {
			return err
		}
		dirs = append(dirs, path)
		return nil
	})
	sort.Strings(dirs)
	return dirs, err
}

func (d diskOutput) mergedEnvironments(dir string) ([]string, error) {
	return hclfile.MergedEnvironments(dir)
}

func (d diskOutput) mergedStateLocations(dir, env string) ([]string, error) {
	return envmerge.MergedStateLocations(dir, env)
}
