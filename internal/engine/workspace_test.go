// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// rootWorkspace opens the workspace of the root in dir, for a test.
func rootWorkspace(t testing.TB, dir string) *workspace {
	t.Helper()
	ws, err := openWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

// flushed writes ws to its directory, for a test that reads the files there.
func flushed(t testing.TB, ws *workspace) {
	t.Helper()
	if err := ws.Flush(); err != nil {
		t.Fatal(err)
	}
}

// memoryWorkspace is a workspace of files, by name, that no directory has:
// for a test of an edit that never needs Terraform.
func memoryWorkspace(files map[string]string) *workspace {
	contents := map[string][]byte{}
	for name, content := range files {
		contents[name] = []byte(content)
	}
	return newWorkspace("", contents)
}

// wsFile returns the file name of ws, which it must have.
func wsFile(t testing.TB, ws *workspace, name string) string {
	t.Helper()
	content, ok := ws.files[name]
	if !ok {
		t.Fatalf("the workspace has no %s", name)
	}
	return string(content)
}

// appendComment appends comment to generated.tf in ws.
func appendComment(ws *workspace, comment string) (bool, error) {
	content, ok := ws.files[rootfile.GeneratedFileName]
	if !ok {
		return false, errors.New("no " + rootfile.GeneratedFileName)
	}
	ws.Write(rootfile.GeneratedFileName, append(content, []byte("# "+comment+"\n")...))
	return true, nil
}

// commentEdit is an edit that appends comment to generated.tf in ws.
func commentEdit(ws *workspace, comment string) *tidyEdit {
	return &tidyEdit{edit: plainEdit(func() (bool, error) { return appendComment(ws, comment) })}
}

// A workspace reads a root's .tf files, keeps edits in memory and writes
// only the files that changed, removing those it no longer has.
func TestWorkspaceFlushWritesWhatChanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
	writeConfig(t, dir, rootfile.LocalsFileName, "locals {}\n")
	if err := os.WriteFile(filepath.Join(dir, tfrun.LockFileName), []byte("# lock\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := rootWorkspace(t, dir)
	if _, ok := ws.files[tfrun.LockFileName]; ok {
		t.Errorf("the workspace holds %s, which Terraform writes", tfrun.LockFileName)
	}

	if _, err := appendComment(ws, "edited"); err != nil {
		t.Fatal(err)
	}
	ws.Write(rootfile.DataFileName, []byte("# data\n"))
	delete(ws.files, rootfile.LocalsFileName)
	if got := readFile(t, dir, rootfile.GeneratedFileName); got != networkGenerated {
		t.Errorf("generated.tf written before a flush:\n%s", got)
	}
	flushed(t, ws)

	if got := readFile(t, dir, rootfile.GeneratedFileName); got != networkGenerated+"# edited\n" {
		t.Errorf("generated.tf:\n%s", got)
	}
	if got := readFile(t, dir, rootfile.DataFileName); got != "# data\n" {
		t.Errorf("data.tf:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, rootfile.LocalsFileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("locals.tf not removed: %v", err)
	}
	if got := readFile(t, dir, tfrun.LockFileName); got != "# lock\n" {
		t.Errorf("the lock file changed:\n%s", got)
	}
}

// Load and Save see each other's edits, as reading and writing the files
// did, and a file the workspace hasn't is one that doesn't exist.
func TestWorkspaceLoadSeesSavedEdits(t *testing.T) {
	t.Parallel()
	ws := memoryWorkspace(map[string]string{rootfile.GeneratedFileName: networkGenerated})
	f, err := ws.Load(rootfile.GeneratedFileName)
	if err != nil {
		t.Fatal(err)
	}
	f.Edit().Body().AppendNewBlock("locals", nil)
	ws.Save(f)

	again, err := ws.Load(rootfile.GeneratedFileName)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(again.Syntax.Blocks); n != len(f.Syntax.Blocks)+1 {
		t.Errorf("got %d blocks after the edit, want %d", n, len(f.Syntax.Blocks)+1)
	}
	if _, err := ws.Load(rootfile.LocalsFileName); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load of a missing file: got %v, want fs.ErrNotExist", err)
	}
	if missing, err := ws.LoadOptional(rootfile.LocalsFileName); missing != nil || err != nil {
		t.Errorf("LoadOptional of a missing file: got %v, %v", missing, err)
	}
}

// A pass whose plan rejects it leaves nothing behind: the driver restores the
// whole workspace, so a file the pass added goes and one it changed comes
// back, whichever they are.
func TestSettleRestoresEveryFileOfTheWorkspace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
	writeConfig(t, dir, "outputs.tf", "# outputs\n")
	ws := rootWorkspace(t, dir)
	tf := &fakeTerraform{dir: dir, plans: []fakePlan{{summary: tfrun.ChangeSummary{Change: 1}}}}
	e := &tidyEdit{edit: plainEdit(func() (bool, error) {
		ws.Write("outputs.tf", []byte("# changed\n"))
		ws.Write("extra.tf", []byte("# added\n"))
		return appendComment(ws, "edited")
	})}

	kept, err := tidier(tf, ws, Baseline{}).settle(tidyPass{}, e)
	if err != nil || kept {
		t.Fatalf("got kept=%v err=%v, want the edit undone", kept, err)
	}

	for name, want := range map[string]string{rootfile.GeneratedFileName: networkGenerated, "outputs.tf": "# outputs\n"} {
		if got := wsFile(t, ws, name); got != want {
			t.Errorf("%s not restored:\n%s", name, got)
		}
	}
	if _, ok := ws.files["extra.tf"]; ok {
		t.Error("extra.tf not removed from the workspace")
	}
	// The plan saw the edit; the next flush takes it back off the disk.
	if got := readFile(t, dir, "extra.tf"); got != "# added\n" {
		t.Errorf("the plan didn't see extra.tf: %q", got)
	}
	flushed(t, ws)
	if _, err := os.Stat(filepath.Join(dir, "extra.tf")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("extra.tf not removed: %v", err)
	}
	if got := readFile(t, dir, "outputs.tf"); got != "# outputs\n" {
		t.Errorf("outputs.tf not restored:\n%s", got)
	}
}

// The edits of the passes work on a workspace in memory, with no directory
// and no Terraform.
func TestPassEditsInMemory(t *testing.T) {
	t.Parallel()
	t.Run("literals", func(t *testing.T) {
		ws := memoryWorkspace(map[string]string{rootfile.GeneratedFileName: networkGenerated})
		lifted, err := liftLiterals(ws, nil)
		if err != nil || !lifted {
			t.Fatalf("want a lift, got lifted=%v err=%v", lifted, err)
		}
		if got := wsFile(t, ws, rootfile.LocalsFileName); !strings.Contains(got, `vpc_id = "vpc-0abc1234"`) {
			t.Errorf("locals.tf:\n%s", got)
		}
		if got := wsFile(t, ws, rootfile.GeneratedFileName); !strings.Contains(got, "vpc_id = local.vpc_id") {
			t.Errorf("generated.tf:\n%s", got)
		}
	})
	t.Run("references", func(t *testing.T) {
		ws := memoryWorkspace(map[string]string{rootfile.GeneratedFileName: `resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}

resource "aws_subnet" "a" {
  vpc_id = "vpc-0abc1234"
}
`})
		values := map[string]map[string]any{"aws_vpc.main": {"id": "vpc-0abc1234"}, "aws_subnet.a": {"id": "subnet-a", "vpc_id": "vpc-0abc1234"}}
		edited, err := addReferencesSkipping(ws, values, nil)
		if err != nil || !edited["aws_subnet.a"] {
			t.Fatalf("got edited=%v err=%v, want aws_subnet.a", edited, err)
		}
		if got := wsFile(t, ws, rootfile.GeneratedFileName); !strings.Contains(got, "vpc_id = aws_vpc.main.id") {
			t.Errorf("generated.tf:\n%s", got)
		}
	})
	t.Run("tag lift into a local", func(t *testing.T) {
		ws := memoryWorkspace(map[string]string{rootfile.GeneratedFileName: azureGenerated})
		changed, err := applyTagLift(ws, azureDefaultTags, nil)
		if err != nil || !changed {
			t.Fatalf("changed %v, %v", changed, err)
		}
		if got := squashed(wsFile(t, ws, rootfile.LocalsFileName)); !strings.Contains(got, `tags = { env = "prod" }`) {
			t.Errorf("locals.tf:\n%s", got)
		}
	})
	t.Run("an edit that changes nothing writes nothing", func(t *testing.T) {
		ws := memoryWorkspace(map[string]string{rootfile.GeneratedFileName: networkGenerated})
		before := ws.Snapshot()
		if edited, err := addLookups(ws, nil, nil); err != nil || len(edited) > 0 {
			t.Fatalf("got edited=%v err=%v, want none", edited, err)
		}
		if len(ws.files) != len(before) || wsFile(t, ws, rootfile.GeneratedFileName) != networkGenerated {
			t.Errorf("the workspace changed: %v", ws.files)
		}
	})
}

// Saving a file of the workspace formats it as hclfile.File.Save writes it
// to disk, so that output stays the same.
func TestWorkspaceSaveFormatsAsFileSave(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := "\nresource \"aws_vpc\" \"a\" {\n  cidr_block=\"10.0.0.0/16\"\n}\n"
	writeConfig(t, dir, rootfile.GeneratedFileName, src)
	onDisk, err := hclfile.Load(filepath.Join(dir, rootfile.GeneratedFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := onDisk.Save(); err != nil {
		t.Fatal(err)
	}
	ws := memoryWorkspace(map[string]string{rootfile.GeneratedFileName: src})
	f, err := ws.Load(rootfile.GeneratedFileName)
	if err != nil {
		t.Fatal(err)
	}
	ws.Save(f)
	if got, want := wsFile(t, ws, rootfile.GeneratedFileName), readFile(t, dir, rootfile.GeneratedFileName); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A root may link a shared file in, as providers.tf often is: the workspace
// reads it through the link, as hclfile.ConfigFiles does, so an edit keeps
// what it holds.
func TestOpenWorkspaceReadsSymlinkedFiles(t *testing.T) {
	t.Parallel()
	shared := filepath.Join(t.TempDir(), "providers.tf")
	const providers = "provider \"aws\" {\n  region = \"eu-west-1\"\n}\n"
	if err := os.WriteFile(shared, []byte(providers), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(shared, filepath.Join(dir, "providers.tf")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.tf"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws := rootWorkspace(t, dir)
	if got := string(ws.files["providers.tf"]); got != providers {
		t.Errorf("providers.tf: got %q, want %q", got, providers)
	}
	if _, ok := ws.files["dir.tf"]; ok {
		t.Error("a directory named dir.tf was read as a file")
	}
}
