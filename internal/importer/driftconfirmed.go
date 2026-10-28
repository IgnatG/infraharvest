// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/IgnatG/infraharvest/internal/driftpolicy"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// confirmedRoots is a copy of the roots under an output directory as the
// user confirmed them, by their digest (see DriftOptions.RootsDigest), in
// a private directory of the run's own: drift runs Terraform on the copy
// alone, never on the output directory, where a file could change after
// the digest was checked, such as a root not yet run, rewritten while
// another runs.
type confirmedRoots struct {
	// out is the output directory, copy its copy, in dir, the run's own.
	out, copy, dir string
	// hashes are those of the files copied, by their paths relative to
	// out, to tell those the run changed (see writeBack).
	hashes map[string][sha256.Size]byte
}

// copyConfirmedRoots copies into a private directory each file Terraform
// reads under out, as driftpolicy.ReadRoots reads it, once, and fails
// unless the digest of those bytes is digest: the copy is then exactly
// what the user confirmed. The roots' local state, which the digest
// doesn't cover, is copied too, for drift to pull. The checkpoints aren't
// copied, as drift stages each root's plans there, in the copy.
func copyConfirmedRoots(out, digest string) (_ *confirmedRoots, err error) {
	dir, err := os.MkdirTemp("", "infraharvest-drift-roots-")
	if err != nil {
		return nil, err
	}
	c := &confirmedRoots{out: out, copy: filepath.Join(dir, "out"), dir: dir, hashes: map[string][sha256.Size]byte{}}
	defer func() {
		if err != nil {
			c.remove()
		}
	}()
	got, err := driftpolicy.ReadRoots(out, func(path string, content []byte) error {
		rel, err := filepath.Rel(out, path)
		if err != nil || inCheckpoints(rel) {
			return err
		}
		c.hashes[rel] = sha256.Sum256(content)
		return writePrivate(filepath.Join(c.copy, rel), content)
	})
	if err != nil {
		return nil, err
	}
	if got != digest {
		return nil, fmt.Errorf("the roots in %s changed since they were checked: a file Terraform reads under it isn't as it was", out)
	}
	if err := c.copyStates(); err != nil {
		return nil, err
	}
	return c, nil
}

// copyStates copies the local state of the roots under out, each
// terraform.tfstate, of the default workspace or another, into the copy.
func (c *confirmedRoots) copyStates() error {
	return filepath.WalkDir(c.out, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(c.out, path)
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".terraform" || inCheckpoints(rel)) {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() || d.Name() != "terraform.tfstate" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return writePrivate(filepath.Join(c.copy, rel), content)
	})
}

// writeBack writes into the output directory each file Terraform reads
// that the run added to the copy or changed there, as drift did into the
// roots before it ran on a copy: the code of the changes it accepted, and
// the dependency lock files its inits wrote.
func (c *confirmedRoots) writeBack() error {
	_, err := driftpolicy.ReadRoots(c.copy, func(path string, content []byte) error {
		rel, err := filepath.Rel(c.copy, path)
		if err != nil || inCheckpoints(rel) {
			return err
		}
		if hash, ok := c.hashes[rel]; ok && hash == sha256.Sum256(content) {
			return nil
		}
		target := filepath.Join(c.out, rel)
		perm := fs.FileMode(0o644)
		if info, err := os.Stat(target); err == nil {
			perm = info.Mode().Perm()
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return fsutil.WriteFile(target, content, perm)
	})
	return err
}

// remove removes the copy.
func (c *confirmedRoots) remove() {
	_ = os.RemoveAll(c.dir)
}

// inCheckpoints reports whether rel, a path relative to the output
// directory, is in its checkpoints, CheckpointDir.
func inCheckpoints(rel string) bool {
	return rel == CheckpointDir || strings.HasPrefix(rel, CheckpointDir+string(filepath.Separator))
}

// writePrivate writes content to path, in directories private to the
// user, created as needed: the copy holds the roots' state.
func writePrivate(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o600)
}
