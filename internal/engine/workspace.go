// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// workspace is the configuration files of a root, held in memory: the edits
// of the tidying passes read and change them here, and only Terraform, which
// reads them from disk, needs them written (see Flush). A snapshot holds
// every file, so restoring one puts back the files an edit changed and
// removes those it added, whichever they are.
type workspace struct {
	dir string
	// files are the contents of the root's files, by name in dir.
	files map[string][]byte
	// disk is what dir has of them: as read, or as Flush last wrote them.
	disk map[string][]byte
}

// workspaceSnapshot is the files of a workspace at one time (see
// workspace.Snapshot).
type workspaceSnapshot map[string][]byte

// openWorkspace reads the configuration files of the root in dir, its .tf
// files. The other files of dir, such as the lock file Terraform writes,
// stay on disk.
func openWorkspace(dir string) (*workspace, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".tf") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		files[e.Name()] = content
	}
	return newWorkspace(dir, files), nil
}

// newWorkspace is a workspace of the root in dir whose files are files, as
// dir has them.
func newWorkspace(dir string, files map[string][]byte) *workspace {
	return &workspace{dir: dir, files: files, disk: maps.Clone(files)}
}

// path is the path of the file name of the root.
func (w *workspace) path(name string) string {
	return filepath.Join(w.dir, name)
}

// Load parses the file name, as the edits so far left it. Like
// hclfile.Load, it returns an error that is fs.ErrNotExist where there is
// no such file.
func (w *workspace) Load(name string) (*hclfile.File, error) {
	content, ok := w.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: w.path(name), Err: fs.ErrNotExist}
	}
	return hclfile.Parse(w.path(name), content)
}

// LoadOptional is Load, or nil where there is no file name.
func (w *workspace) LoadOptional(name string) (*hclfile.File, error) {
	f, err := w.Load(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return f, err
}

// ConfigFiles parses the files Terraform reads as the configuration, as
// hclfile.ConfigFiles does from a directory: by name, but for override
// files.
func (w *workspace) ConfigFiles() ([]*hclfile.File, error) {
	names := slices.Sorted(maps.Keys(w.files))
	var files []*hclfile.File
	for _, name := range names {
		if name == "override.tf" || strings.HasSuffix(name, "_override.tf") {
			continue
		}
		f, err := w.Load(name)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

// Save keeps the edited file f, formatted, as hclfile.File.Save writes it.
func (w *workspace) Save(f *hclfile.File) {
	w.files[filepath.Base(f.Path)] = bytes.TrimLeft(hclwrite.Format(f.Edit().Bytes()), "\n")
}

// Write keeps content as the file name.
func (w *workspace) Write(name string, content []byte) {
	w.files[name] = bytes.Clone(content)
}

// Snapshot returns the files as they are, for Restore. The contents are
// never changed in place, only replaced, so the snapshot shares them.
func (w *workspace) Snapshot() workspaceSnapshot {
	return maps.Clone(w.files)
}

// Restore puts every file back as s has it, and removes those s hasn't.
func (w *workspace) Restore(s workspaceSnapshot) {
	w.files = maps.Clone(s)
}

// Flush writes the files that differ from what dir has to it, and removes
// from it those the workspace no longer has.
func (w *workspace) Flush() error {
	for name, content := range w.files {
		if on, ok := w.disk[name]; ok && bytes.Equal(on, content) {
			continue
		}
		if err := fsutil.WriteFile(w.path(name), content, 0o644); err != nil {
			return err
		}
		w.disk[name] = content
	}
	for name := range w.disk {
		if _, ok := w.files[name]; ok {
			continue
		}
		if err := os.Remove(w.path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		delete(w.disk, name)
	}
	return nil
}

// terraform returns tf, which runs in the workspace's root, writing the
// workspace's files before each command (see Flush): Terraform then reads
// the configuration as the edits left it.
func (w *workspace) terraform(tf tfrun.Terraform) tfrun.Terraform {
	return flushingTerraform{Terraform: tf, ws: w}
}

// flushingTerraform is the Terraform of a workspace (see
// workspace.terraform).
type flushingTerraform struct {
	tfrun.Terraform
	ws *workspace
}

func (f flushingTerraform) flush() error {
	if err := f.ws.Flush(); err != nil {
		return fmt.Errorf("write %s: %w", f.ws.dir, err)
	}
	return nil
}

func (f flushingTerraform) Init(ctx context.Context, opts ...tfexec.InitOption) error {
	if err := f.flush(); err != nil {
		return err
	}
	return f.Terraform.Init(ctx, opts...)
}

func (f flushingTerraform) PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	if err := f.flush(); err != nil {
		return false, err
	}
	return f.Terraform.PlanJSON(ctx, w, opts...)
}

func (f flushingTerraform) Validate(ctx context.Context) (*tfjson.ValidateOutput, error) {
	if err := f.flush(); err != nil {
		return nil, err
	}
	return f.Terraform.Validate(ctx)
}

func (f flushingTerraform) ProvidersSchema(ctx context.Context) (*tfjson.ProviderSchemas, error) {
	if err := f.flush(); err != nil {
		return nil, err
	}
	return f.Terraform.ProvidersSchema(ctx)
}

func (f flushingTerraform) ShowPlanFile(ctx context.Context, planPath string, opts ...tfexec.ShowOption) (*tfjson.Plan, error) {
	if err := f.flush(); err != nil {
		return nil, err
	}
	return f.Terraform.ShowPlanFile(ctx, planPath, opts...)
}

func (f flushingTerraform) FormatCheck(ctx context.Context, opts ...tfexec.FormatOption) (bool, []string, error) {
	if err := f.flush(); err != nil {
		return false, nil, err
	}
	return f.Terraform.FormatCheck(ctx, opts...)
}
