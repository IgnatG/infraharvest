// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gate

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// walkOutput calls visit with each file Generate wrote into dir, by path
// relative to dir, skipping Terraform's working files, and the local state
// of a root applied without a backend (see tfrun.LocalStateFile), which no
// check reads: it holds the secret values the configuration mustn't.
func walkOutput(dir string, visit func(rel, content string)) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".terraform" || d.Name() == tfrun.WorkspaceStateDir {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == tfrun.LockFileName || tfrun.LocalStateFile(d.Name()) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		visit(filepath.ToSlash(rel), string(content))
		return nil
	})
}

// walkOutputAndModules is walkOutput on dir and then on each local module
// it calls outside it (see calledModules), such as the shared modules
// under the output directory's modules directory. The files of a module
// are named by their path relative to dir, as in ../../modules/vpc/main.tf.
func walkOutputAndModules(dir string, visit func(rel, content string)) error {
	if err := walkOutput(dir, visit); err != nil {
		return err
	}
	modules, err := calledModules(dir)
	if err != nil {
		return err
	}
	for _, m := range modules {
		err := walkOutput(m, func(path, content string) {
			visit(rel(dir, filepath.Join(m, filepath.FromSlash(path))), content)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// calledModules returns the directories of the local modules that dir's
// configuration calls, and that those call in turn, other than those under
// dir, which walkOutput covers already; sorted. A call whose source isn't
// a constant local path, or whose directory doesn't exist, is skipped, as
// is a file that doesn't parse: validate (G2) reports those.
func calledModules(dir string) ([]string, error) {
	seen := map[string]bool{}
	var walk func(d string) error
	walk = func(d string) error {
		paths, err := filepath.Glob(filepath.Join(d, "*.tf"))
		if err != nil {
			return err
		}
		sort.Strings(paths)
		for _, path := range paths {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			f, err := hclfile.Parse(path, content)
			if err != nil {
				continue
			}
			for _, b := range f.Syntax.Blocks {
				m := LocalModuleDir(d, b)
				if m == "" || seen[m] {
					continue
				}
				if info, err := os.Stat(m); err != nil || !info.IsDir() {
					continue
				}
				seen[m] = true
				if err := walk(m); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(dir); err != nil {
		return nil, err
	}
	var dirs []string
	for m := range seen {
		if r := rel(dir, m); r == ".." || strings.HasPrefix(r, "../") {
			dirs = append(dirs, m)
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

// LocalModuleDir returns the directory a module block in dir calls when
// its source is a local path, such as "../../modules/vpc", or "".
func LocalModuleDir(dir string, b *hclsyntax.Block) string {
	if b.Type != "module" {
		return ""
	}
	attr, ok := b.Body.Attributes["source"]
	if !ok {
		return ""
	}
	source, ok := hclfile.ConstantString(attr.Expr)
	if !ok || (!strings.HasPrefix(source, "./") && !strings.HasPrefix(source, "../")) {
		return ""
	}
	return filepath.Join(dir, filepath.FromSlash(source))
}

// rel names path relative to dir, with forward slashes; "." for dir.
func rel(dir, path string) string {
	r, err := filepath.Rel(dir, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(r)
}
