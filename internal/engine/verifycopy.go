// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
)

// NewRunner returns a Terraform runner for a directory, such as the
// private copy Verify and VerifyMerged check a root in.
type NewRunner func(dir string) (Terraform, error)

// verifyCopyInfix is in the name of the copy verifyCopy makes, after the
// root's, such as .us-east-1-infraharvest-verify-123456.
const verifyCopyInfix = "-infraharvest-verify-"

// IsVerifyCopy reports whether name, a directory's, is that of a copy of
// a root Verify or VerifyMerged checks, removed once checked, or left by a
// run that was killed: no root of its own.
func IsVerifyCopy(name string) bool {
	return strings.HasPrefix(name, ".") && strings.Contains(name, verifyCopyInfix)
}

// verifyCopy copies the root in dir, for its gate to check without its
// backend and without touching the root: beside it, in a hidden directory
// of the same parent, so that the sources of local modules, such as
// ../../modules/<name>, reach the same modules. The copy has the root's
// files and subdirectories, such as vars/, without Terraform's working
// directory or any state, and its top-level .tf files without backend or
// cloud blocks: Terraform refuses to plan a backend that init, run
// without it (-backend=false), hasn't initialised, and a root initialised
// with its backend would plan against its state. It returns the copy and
// a function that removes it.
func verifyCopy(dir string) (string, func(), error) {
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
			if name == ".terraform" || name == workspaceStateDir || name == ".infraharvest" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(copied, rel), 0o700)
		}
		if !d.Type().IsRegular() || strings.HasPrefix(name, localStateFileName) || name == ".terraform.tfstate.lock.info" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(name, ".tf") && filepath.Dir(rel) == "." {
			content = dropBackend(content)
		}
		return os.WriteFile(filepath.Join(copied, rel), content, 0o600)
	})
	if err != nil {
		remove()
		return "", nil, err
	}
	return copied, remove, nil
}

// dropBackend returns src, a configuration file, without the backend and
// cloud blocks of its terraform blocks, cut out of it, with their lines
// where nothing else is on them: the rest stays byte for byte, so that the
// format check finds what it would in the root, but where src was
// formatted, which it stays. It returns src as it is where it has none,
// or doesn't parse.
func dropBackend(src []byte) []byte {
	f, err := parseHCL("", src)
	if err != nil {
		return src
	}
	var cuts [][2]int
	for _, b := range f.syntax.Blocks {
		if b.Type != "terraform" {
			continue
		}
		for _, inner := range b.Body.Blocks {
			if inner.Type != "backend" && inner.Type != "cloud" {
				continue
			}
			rng := inner.Range()
			start, end := rng.Start.Byte, rng.End.Byte
			if ls, le := lineStart(src, start), lineEnd(src, end); len(bytes.TrimSpace(src[ls:start])) == 0 && len(bytes.TrimSpace(src[end:le])) == 0 {
				start, end = ls, le
			}
			cuts = append(cuts, [2]int{start, end})
		}
	}
	if len(cuts) == 0 {
		return src
	}
	formatted := bytes.Equal(hclwrite.Format(src), src)
	out := slices.Clone(src)
	for i := len(cuts) - 1; i >= 0; i-- {
		out = slices.Concat(out[:cuts[i][0]], out[cuts[i][1]:])
	}
	if formatted {
		return hclwrite.Format(out)
	}
	return out
}
