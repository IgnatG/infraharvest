// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package driftpolicy

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// RootsDigest returns a digest of the files Terraform reads under the
// output directory out, each by its path and a hash of its content: the
// configuration files (.tf, .tf.json, .tofu and .tofu.json), the var
// files, the backend settings and the dependency lock files, the
// .terraform directories aside. The MCP server confirms a drift run for
// the roots with one digest, and drift, given it, runs only on roots that
// still have it, checked before it stages any.
func RootsDigest(out string) (string, error) {
	var files []string
	err := filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".terraform" {
				return filepath.SkipDir
			}
			return nil
		}
		if !readByTerraform(d.Name()) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(out, path)
		if err != nil {
			return err
		}
		files = append(files, fmt.Sprintf("%q %x", filepath.ToSlash(rel), sha256.Sum256(content)))
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(files, "\n")))), nil
}

// readByTerraform reports whether name is a file RootsDigest covers.
func readByTerraform(name string) bool {
	for _, ext := range []string{".tf", ".tf.json", ".tofu", ".tofu.json", ".tfvars", ".tfvars.json", ".hcl", ".tfbackend"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}
