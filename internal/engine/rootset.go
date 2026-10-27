// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// GeneratedFiles returns the names of the files Generate may write
// directly into a root, besides those its configuration is split into
// (see Result.Files) and versions.tf: what generating the root again
// replaces.
func GeneratedFiles() []string {
	return []string{GeneratedFileName, ImportsFileName, RejectedFileName, LocalsFileName, VariablesFileName, DataFileName, ReadmeFileName, MovesFileName}
}

// AddedFiles returns the paths of the files of resources incremental
// imports added to the root in dir (see AddedFileName).
func AddedFiles(dir string) ([]string, error) {
	return filepath.Glob(filepath.Join(dir, "generated_*.tf"))
}

// configurationFiles returns the files result's configuration is in: the
// files it was split into, or generated.tf.
func configurationFiles(result *Result) []string {
	if result != nil && len(result.Files) > 0 {
		return result.Files
	}
	return []string{GeneratedFileName}
}

// ResultFiles returns the files the root in dir must still have to be as
// result, of generating it, left it: versions.tf and its configuration,
// with moves.tf if result moves, removes or retypes resources, while the
// root has imports.tf. Once the root is applied, its README says to
// delete both.
func ResultFiles(dir string, result *Result) ([]string, error) {
	names := append([]string{VersionsFileName}, configurationFiles(result)...)
	imports, err := pathExists(filepath.Join(dir, ImportsFileName))
	if err != nil {
		return nil, err
	}
	if imports && (len(result.Moves) > 0 || len(result.Removed) > 0 || len(result.Retyped) > 0) {
		names = append(names, MovesFileName)
	}
	return names, nil
}

// Unfinished reports whether the root in dir is what a Generate that
// didn't finish left: it has generated.tf, but not the README written
// last.
func Unfinished(dir string) (bool, error) {
	generated, err := pathExists(filepath.Join(dir, GeneratedFileName))
	if err != nil || !generated {
		return false, err
	}
	readme, err := pathExists(filepath.Join(dir, ReadmeFileName))
	return !readme, err
}

// pathExists reports whether path exists.
func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
