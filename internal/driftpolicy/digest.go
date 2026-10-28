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

// RootsDigest returns the digest of the files Terraform reads under the
// output directory out (see ReadRoots). The MCP server confirms a drift
// run for the roots with one digest, and drift, given it, runs only on
// roots that still have it, checked as it copies them before it stages
// any: Terraform runs on the copy.
func RootsDigest(out string) (string, error) {
	return ReadRoots(out, nil)
}

// ReadRoots reads each file Terraform reads under the output directory
// out once, hands its path and content to read, if not nil, and returns
// a digest of them all, each by its path and a hash of the content read
// had: the configuration files (.tf, .tf.json, .tofu and .tofu.json), the
// var files, the backend settings and the dependency lock files, the
// .terraform directories aside. A check of the roots that reads them
// through it so covers, by the digest it returns, the very bytes it
// checked, which a second read could find changed.
//
// An output directory that isn't there has no roots, and a digest all
// the same. Any other error is one, such as a file gone mid-walk: a
// digest of part of the files would pass roots it never covered. A
// terraform.d directory, a filesystem mirror Terraform would install
// providers from, and a symbolic link to a directory, which Terraform
// follows and a walk doesn't, are refused, as is a file whose link leads
// nowhere or out of out.
func ReadRoots(out string, read func(path string, content []byte) error) (string, error) {
	var files []string
	if _, err := os.Lstat(out); errors.Is(err, fs.ErrNotExist) {
		// No roots: drift says so.
		return digestOf(files), nil
	} else if err != nil {
		return "", err
	}
	resolvedOut, err := filepath.EvalSymlinks(out)
	if err != nil {
		return "", err
	}
	err = filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(out, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case ".terraform":
				return filepath.SkipDir
			case "terraform.d":
				return fmt.Errorf("%s is a terraform.d directory, which Terraform installs providers from", rel)
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				return fmt.Errorf("%s is a symbolic link to a directory, which this check doesn't follow", rel)
			}
		}
		if !readByTerraform(d.Name()) {
			return nil
		}
		content, err := readUnder(resolvedOut, path, rel)
		if err != nil {
			return err
		}
		if read != nil {
			if err := read(path, content); err != nil {
				return err
			}
		}
		files = append(files, fmt.Sprintf("%q %x", rel, sha256.Sum256(content)))
		return nil
	})
	if err != nil {
		return "", err
	}
	return digestOf(files), nil
}

// readUnder returns the content of the file at path, rel under the
// output directory, whose path with no symbolic links is resolvedOut,
// where it leads: a symbolic link must lead to a file under it.
func readUnder(resolvedOut, path, rel string) ([]byte, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("%s can't be read: %w", rel, err)
	}
	if r, err := filepath.Rel(resolvedOut, resolved); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%s is a symbolic link out of the output directory", rel)
	}
	return os.ReadFile(resolved)
}

// digestOf returns the digest of files, each a path and a hash of its
// content.
func digestOf(files []string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(files, "\n"))))
}

// readByTerraform reports whether name is a file ReadRoots covers.
func readByTerraform(name string) bool {
	for _, ext := range []string{".tf", ".tf.json", ".tofu", ".tofu.json", ".tfvars", ".tfvars.json", ".hcl", ".tfbackend"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}
