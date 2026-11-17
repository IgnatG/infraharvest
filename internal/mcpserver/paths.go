// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// within checks that path, which the agent chose, is under the root: an
// agent may not read or write files anywhere else on the machine. The path
// has to be under it as written and where it leads, so a symbolic link
// under the root to somewhere else doesn't take it out. A relative path is
// relative to the root, not to the directory the server runs in; within
// returns the path to use, joined onto the root if it was relative.
func (t *tools) within(what, path string) (string, error) {
	if strings.ContainsFunc(path, unicode.IsControl) {
		// Such as a newline, which could make the confirmation the user is
		// shown say something else.
		return "", fmt.Errorf("%s %q has a control character", what, path)
	}
	root, err := filepath.Abs(t.root)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return path, checkWithin(what, root, path)
}

// checkWithin checks that path, which is absolute, is under root (see
// within).
func checkWithin(what, root, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	resolvedRoot, err := resolve(root)
	if err != nil {
		return err
	}
	// Some systems take a ".." after a symbolic link up from where the
	// link leads, and others, Windows, up from the link: path must stay
	// under the root either way.
	for _, p := range []string{path, abs} {
		resolved, err := resolve(p)
		if err != nil {
			return err
		}
		if !under(resolvedRoot, resolved) {
			return fmt.Errorf("%s %q must be under %s", what, path, root)
		}
		if err := noJunctionsBelow(resolvedRoot, resolved); err != nil {
			return fmt.Errorf("%s %q: %w", what, path, err)
		}
	}
	if !under(root, abs) {
		return fmt.Errorf("%s %q must be under %s", what, path, root)
	}
	return nil
}

// noJunctionsBelow checks that no directory or file on the way from root
// to path, which resolve returned and is under root, both of which are
// absolute, is a link resolve doesn't follow (see unfollowedLink): a
// junction on Windows, which Go reports as fs.ModeIrregular since 1.23,
// not as a symbolic link, and filepath.EvalSymlinks leaves as it is, so
// that path seems under the root where it leads out of it.
func noJunctionsBelow(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return err
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		linked, err := unfollowedLink(current, info)
		if err != nil {
			return err
		}
		if linked {
			return fmt.Errorf("%s is a junction or another link that can't be followed", current)
		}
	}
	return nil
}

// under reports whether path is dir or under it; both are absolute.
func under(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolve returns where path leads, absolute: each symbolic link in it
// that exists is followed, and a ".." after one goes up from where the
// link leads. The part that doesn't exist yet is kept as it is.
func resolve(path string) (string, error) {
	if !filepath.IsAbs(path) {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		// Not filepath.Join, which would take a ".." up from the element
		// before it rather than from where that leads.
		path = wd + string(filepath.Separator) + path
	}
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	parts := strings.FieldsFunc(path[len(volume):], func(r rune) bool { return r == '/' || r == filepath.Separator })
	for _, part := range parts {
		switch part {
		case ".":
		case "..":
			current = filepath.Dir(current)
		default:
			next := filepath.Join(current, part)
			resolved, err := filepath.EvalSymlinks(next)
			switch {
			case err == nil:
				current = resolved
			case errors.Is(err, fs.ErrNotExist):
				current = next
			default:
				return "", err
			}
		}
	}
	return current, nil
}

// readWithin reads the file at path, which the agent chose, under the root
// (see openWithin), and returns the path to use and the content.
func (t *tools) readWithin(what, path string) (string, []byte, error) {
	path, f, err := t.openWithin(what, path)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	content, err := io.ReadAll(f)
	if err != nil {
		return "", nil, err
	}
	return path, content, nil
}

// openWithin opens the file at path, which the agent chose, under the root
// (see within), and returns the path to use and the open file, to read
// from. The file is opened once, and has to be the file path leads to
// under the root once it is open, so a link swapped in between the check
// and the open is refused; and the file may have no other name, as a
// hard link to a file outside the root would give it.
func (t *tools) openWithin(what, path string) (string, *os.File, error) {
	path, err := t.within(what, path)
	if err != nil {
		return "", nil, err
	}
	// Before opening it, which would wait on a pipe.
	if info, err := os.Stat(path); err == nil && !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s %q isn't a regular file", what, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	if err := t.checkOpened(what, path, f); err != nil {
		_ = f.Close()
		return "", nil, err
	}
	return path, f, nil
}

// checkOpened checks that f, opened at path, is a regular file, the file
// path leads to under the root now that it is open, and has no other name
// (see openWithin).
func (t *tools) checkOpened(what, path string, f *os.File) error {
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() {
		return fmt.Errorf("%s %q isn't a regular file", what, path)
	}
	// Checked again now that it is open, so a link swapped in before the
	// open is caught, and the handle compared with where path leads.
	if _, err := t.within(what, path); err != nil {
		return err
	}
	resolved, err := resolve(path)
	if err != nil {
		return err
	}
	current, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !os.SameFile(opened, current) {
		return fmt.Errorf("%s %q changed while it was opened", what, path)
	}
	linked, err := hardLinked(f, opened)
	if err != nil {
		return err
	}
	if linked {
		return fmt.Errorf("%s %q has another name, a hard link, which could be outside %s", what, path, t.root)
	}
	return nil
}

// writeWithin writes data to the file at path under the root, as
// fsutil.WriteFile does, through a temporary file in the same directory
// renamed over path once complete, but through the root opened as an
// os.Root: a symbolic link swapped in for path or a directory on the way
// to it, after path was checked, can't lead the write out of the root.
func (t *tools) writeWithin(path string, data []byte, perm fs.FileMode) (err error) {
	root, err := filepath.Abs(t.root)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	temp := filepath.Join(filepath.Dir(rel), fmt.Sprintf(".%s.%d.tmp", filepath.Base(rel), rand.Uint64())) //nolint:gosec // G404: a collision-avoiding temp name, not a secret
	f, err := r.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = r.Remove(temp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// After closing: Windows can't change an open file's mode.
	if err = r.Chmod(temp, perm); err != nil {
		return err
	}
	return r.Rename(temp, rel)
}

// linksStayUnder checks that no symbolic link under dir, which an import
// writes into, leads out of the root, as one a cloned repository plants
// could: the import would write there; nor is any a junction. Terraform's
// working directories are skipped, where terraform init links providers
// from the plugin cache.
func (t *tools) linksStayUnder(what, dir string) error {
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".terraform" {
			return filepath.SkipDir
		}
		// A junction on Windows is fs.ModeIrregular, which within
		// refuses (see noJunctionsBelow).
		if d.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			_, err := t.within(what, path)
			return err
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
