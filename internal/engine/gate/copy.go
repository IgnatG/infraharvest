// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gate

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// NewRunner returns a Terraform runner for a directory, such as the
// private copy engine.Verify and envmerge.VerifyMerged check a root in.
type NewRunner func(dir string) (tfrun.Terraform, error)

// verifyCopyInfix is in the name of the copy VerifyCopy makes, after the
// root's, such as .us-east-1-infraharvest-verify-123456.
const verifyCopyInfix = "-infraharvest-verify-"

// IsVerifyCopy reports whether name, a directory's, is that of a copy of
// a root engine.Verify or envmerge.VerifyMerged checks, removed once
// checked, or left by a run that was killed: no root of its own.
func IsVerifyCopy(name string) bool {
	return strings.HasPrefix(name, ".") && strings.Contains(name, verifyCopyInfix)
}

// VerifyCopy copies the root in dir, for its gate to check without its
// backend and without touching the root: beside it, in a hidden directory
// of the same parent, so that the sources of local modules, such as
// ../../modules/<name>, reach the same modules. The copy has the root's
// files and subdirectories, such as vars/, without Terraform's working
// directory or any state, and its top-level .tf files without backend or
// cloud blocks: Terraform refuses to plan a backend that init, run
// without it (-backend=false), hasn't initialised, and a root initialised
// with its backend would plan against its state. It returns the copy and
// a function that removes it.
func VerifyCopy(dir string) (string, func(), error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, err
	}
	copied, err := os.MkdirTemp(filepath.Dir(abs), "."+filepath.Base(abs)+verifyCopyInfix)
	if err != nil {
		return "", nil, err
	}
	remove := func() { _ = os.RemoveAll(copied) }
	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil || rel == "." {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".terraform" || name == tfrun.WorkspaceStateDir || name == ".infraharvest" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(copied, rel), 0o700)
		}
		if !d.Type().IsRegular() || strings.HasPrefix(name, tfrun.LocalStateFileName) || name == ".terraform.tfstate.lock.info" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(name, ".tf") && filepath.Dir(rel) == "." {
			content = hclfile.DropBackend(content)
		}
		return os.WriteFile(filepath.Join(copied, rel), content, 0o600)
	})
	if err != nil {
		remove()
		return "", nil, err
	}
	return copied, remove, nil
}
