// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/report"
)

// One run at a time holds an output directory's lock: another is refused,
// naming the holder, unless it forces its way in, and takes the lock once
// it is released.
func TestLockOutput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	held, err := lockOutput(out, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := lockOutput(out, false)
	if err == nil {
		_ = second.Unlock()
		t.Fatal("a second lock of the output directory was taken")
	}
	for _, want := range []string{out + " is in use by another infraharvest run", fmt.Sprintf("process %d on ", os.Getpid()), "--force-unlock"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q doesn't say %q", err, want)
		}
	}
	if lock, err := lockOutput(out, true); err != nil || lock != nil {
		t.Errorf("with force: got %v, %v; want to go on without the lock", lock, err)
	}
	if err := held.Unlock(); err != nil {
		t.Fatal(err)
	}
	again, err := lockOutput(out, false)
	if err != nil {
		t.Fatalf("after the unlock: %v", err)
	}
	if err := again.Unlock(); err != nil {
		t.Fatal(err)
	}
}

// A run writes no report into an output directory another run holds, and
// one that finishes releases the lock its Import calls took.
func TestRunLocksTheOutput(t *testing.T) {
	out := t.TempDir()
	holder := NewRun()
	if err := holder.lockOut(Options{Out: out}); err != nil {
		t.Fatal(err)
	}
	// The command's other Import calls share the lock.
	if err := holder.child().lockOut(Options{Out: out}); err != nil {
		t.Errorf("a child run of the holder: %v", err)
	}

	other := NewRun()
	other.used = true
	other.options = Options{Out: out}
	err := other.finish(nil)
	if code := ExitCode(err); code != report.ExitCouldNotRun || !strings.Contains(err.Error(), "is in use by another infraharvest run") {
		t.Errorf("exit code %d, error %v; want %d and the holder named", code, err, report.ExitCouldNotRun)
	}
	if _, statErr := os.Stat(filepath.Join(out, report.Dir)); statErr == nil {
		t.Error("report written into an output directory another run holds")
	}

	if err := holder.finish(nil); err != nil {
		t.Fatal(err)
	}
	lock, err := lockOutput(out, false)
	if err != nil {
		t.Fatalf("after the holder finished: %v", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
}

// A run refuses an output directory whose CheckpointDir holds a symbolic
// link, such as one a repository it checks commits in place of the lock,
// CheckpointDir itself or a staging directory, to a file or directory of
// the user's, --force-unlock or not, and leaves what it leads to as it
// is, and likewise one elsewhere in the output directory that leads a
// write out of it: a directory linked, or a file linked out of it. Links
// terraform init makes in a staged root's .terraform pass.
func TestLockOutputRefusesLinks(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	keys := filepath.Join(outside, "authorized_keys")
	const content = "ssh-ed25519 key\n"
	if err := os.WriteFile(keys, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(keys, filepath.Join(t.TempDir(), "probe")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}

	for _, tc := range []struct {
		name string
		// link is where, given the output directory, the link goes, to
		// keys or, with dir, to outside
		link func(out string) string
		dir  bool
		// pass is whether the run takes the lock anyway
		pass bool
	}{
		{name: "the lock", link: outputLockPath},
		{name: "the checkpoint directory", link: func(out string) string { return filepath.Join(out, CheckpointDir) }, dir: true},
		{name: "a checkpoint", link: func(out string) string { return checkpointPath(out, filepath.Join(out, "aws", "network")) }},
		{name: "a drift staging directory", link: func(out string) string { return driftStagingDir(out, "aws/network") }, dir: true},
		{name: "an incremental staging directory's parent", link: func(out string) string {
			return filepath.Dir(stagingDir(out, filepath.Join(out, "aws", "network")))
		}, dir: true},
		// Outside CheckpointDir, where the roots, modules and report go.
		{name: "the report directory", link: func(out string) string { return filepath.Join(out, report.Dir) }, dir: true},
		{name: "a root", link: func(out string) string { return filepath.Join(out, "aws", "1", "eu-west-2") }, dir: true},
		{name: "a root's file", link: func(out string) string { return filepath.Join(out, "aws", "1", "eu-west-2", "imports.tf") }},
		{name: "a module's file", link: func(out string) string { return filepath.Join(out, "modules", "bucket", "main.tf") }},
		{name: "a provider terraform init linked", link: func(out string) string {
			return filepath.Join(driftStagingDir(out, "aws/network"), ".terraform", "providers", "aws")
		}, dir: true, pass: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := t.TempDir()
			link := tc.link(out)
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			target := keys
			if tc.dir {
				target = outside
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			for _, force := range []bool{false, true} {
				lock, err := lockOutput(out, force)
				if tc.pass {
					if err != nil || lock == nil {
						t.Fatalf("force %v: got %v, %v; want the lock", force, lock, err)
					}
					if err := lock.Unlock(); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if lock != nil {
					_ = lock.Record()
					_ = lock.Unlock()
				}
				if err == nil || !strings.Contains(err.Error(), link+" is a symbolic link") {
					t.Errorf("force %v: got %v, want %s refused", force, err, link)
				}
			}
			if got, err := os.ReadFile(keys); err != nil || string(got) != content {
				t.Errorf("the link's target changed: %q, %v", got, err)
			}
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 1 {
				t.Errorf("written into the link's target: %v, %v", entries, err)
			}
		})
	}
}

// An output directory the user linked to one elsewhere, and a file linked
// to another in it, such as roots sharing a file, take the lock.
func TestLockOutputTakesLinksThatStayInside(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	real := filepath.Join(base, "real")
	common := filepath.Join(real, "common", "providers.tf")
	root := filepath.Join(real, "aws", "1", "eu-west-2")
	for _, dir := range []string{filepath.Dir(common), root} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(common, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(base, "out")
	if err := os.Symlink(real, out); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if err := os.Symlink(common, filepath.Join(root, "providers.tf")); err != nil {
		t.Fatal(err)
	}
	lock, err := lockOutput(out, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
}
