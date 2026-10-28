// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/gate"
)

// claimFor claims the root in dir, under out, for generating it from the
// fingerprint "fp", as a run with resume and regenerate does: refused, as
// earlier imports left it (see rootDecision.generated), or got ready (see
// claimRoot).
func claimFor(out, dir string, resume, regenerate bool) (*claim, error) {
	d := rootDecision{}.generated(out, dir, dir, resume, regenerate)
	if d.mode == rootRefused {
		return nil, d.reason
	}
	return claimRoot(out, dir, "fp", d, nil)
}

func TestCheckpoints(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "111122223333", "eu-west-2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	imports := []engine.Import{{Type: "aws_vpc", Name: "main", ID: "vpc-0abc1234"}}
	opts := engine.Options{Config: map[string][]byte{engine.VersionsFileName: []byte("terraform {}\n")}}
	fp, err := fingerprint("1.16.5", imports, opts)
	if err != nil {
		t.Fatal(err)
	}
	result := &engine.Result{Imported: imports, Gate: gate.Gate{{Name: gate.CheckPlan, Passed: true}}}

	// Nothing recorded yet.
	if got, err := resumed(out, dir, fp); err != nil || got != nil {
		t.Fatalf("before a checkpoint: %v, %v", got, err)
	}
	if err := saveCheckpoint(out, dir, fp, result); err != nil {
		t.Fatal(err)
	}
	// The checkpoint counts only while the root's files are there.
	if got, err := resumed(out, dir, fp); err != nil || got != nil {
		t.Fatalf("without the root's files: %v, %v", got, err)
	}
	for _, name := range []string{engine.GeneratedFileName, engine.ImportsFileName, engine.VersionsFileName} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := resumed(out, dir, fp)
	if err != nil || !reflect.DeepEqual(got, result) {
		t.Errorf("resumed: got %+v, %v, want %+v", got, err, result)
	}

	// Other imports or options make another root.
	other, err := fingerprint("1.16.5", append(imports, engine.Import{Type: "aws_subnet", Name: "a", ID: "subnet-1"}), opts)
	if err != nil {
		t.Fatal(err)
	}
	newerEngine, err := fingerprint("1.17.0", imports, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{other, newerEngine} {
		if f == fp {
			t.Error("fingerprint unchanged")
		}
		if got, err := resumed(out, dir, f); err != nil || got != nil {
			t.Errorf("resumed with another fingerprint: %v, %v", got, err)
		}
	}
}

// Listing order varies between runs; the fingerprint doesn't.
func TestFingerprintIgnoresOrder(t *testing.T) {
	a := []engine.Import{{Type: "aws_vpc", Name: "a", ID: "vpc-1"}, {Type: "aws_subnet", Name: "b", ID: "subnet-1"}}
	b := []engine.Import{a[1], a[0]}
	fa, err := fingerprint("1.16.5", a, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fb, err := fingerprint("1.16.5", b, engine.Options{})
	if err != nil || fa != fb {
		t.Errorf("fingerprints differ: %s, %s, %v", fa, fb, err)
	}
	if a[0].Type != "aws_vpc" {
		t.Error("fingerprint reordered the caller's imports")
	}
}

func TestClearGenerated(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "123", "eu-west-2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// generated_2.tf is what --incremental added to the root; network.tf
	// what the checkpoint says the configuration was split into.
	for _, name := range []string{engine.GeneratedFileName, engine.ImportsFileName, engine.VersionsFileName, engine.AddedFileName(2), "network.tf", "notes.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveCheckpoint(out, dir, "fp", &engine.Result{Files: []string{"network.tf", "../outside.tf"}}); err != nil {
		t.Fatal(err)
	}
	if err := clearGenerated(out, dir); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{engine.GeneratedFileName: false, engine.ImportsFileName: false, engine.AddedFileName(2): false, "network.tf": false, engine.VersionsFileName: true, "notes.md": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != want {
			t.Errorf("%s: exists=%v, want %v", name, err == nil, want)
		}
	}
}

// writeRoot writes empty files of names into dir.
func writeRoot(t *testing.T, dir string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// A root is only generated into a directory an import wrote, or one
// without .tf files; --resume clears only what an import wrote.
func TestClaimRoot(t *testing.T) {
	out := t.TempDir()

	// An unrelated root keeps its files, whatever the flags.
	unrelated := filepath.Join(out, "aws", "111", "eu-west-2")
	writeRoot(t, unrelated, "main.tf", engine.VariablesFileName, engine.ReadmeFileName)
	for _, flags := range [][2]bool{{false, false}, {true, false}, {false, true}} {
		_, err := claimFor(out, unrelated, flags[0], flags[1])
		if err == nil || !strings.Contains(err.Error(), unrelated) || !strings.Contains(err.Error(), "main.tf") {
			t.Errorf("resume, regenerate=%v: want an error naming the directory and main.tf, got %v", flags, err)
		}
	}
	for _, name := range []string{"main.tf", engine.VariablesFileName, engine.ReadmeFileName} {
		if !exists(filepath.Join(unrelated, name)) {
			t.Errorf("%s was removed from the unrelated root", name)
		}
	}
	if exists(checkpointPath(out, unrelated)) {
		t.Error("the unrelated root got a checkpoint")
	}

	// A new root is claimed: its checkpoint records that it started, which
	// --resume doesn't take as a generated root.
	fresh := filepath.Join(out, "aws", "222", "eu-west-2")
	if _, err := claimFor(out, fresh, false, false); err != nil {
		t.Fatal(err)
	}
	if !exists(checkpointPath(out, fresh)) {
		t.Fatal("no checkpoint for the new root")
	}
	writeRoot(t, fresh, engine.ImportsFileName, engine.VersionsFileName, engine.GeneratedFileName)
	if got, err := resumed(out, fresh, "fp"); err != nil || got != nil {
		t.Errorf("resumed a root that didn't finish: %v, %v", got, err)
	}

	// Without a flag, a root whose import didn't finish is refused.
	if _, err := claimFor(out, fresh, false, false); err == nil || !strings.Contains(err.Error(), "--resume") {
		t.Errorf("an unfinished root without --resume: got %v", err)
	}

	// --resume clears what the import wrote there, and only that.
	writeRoot(t, fresh, engine.VariablesFileName, engine.ReadmeFileName, "notes.md")
	if _, err := claimFor(out, fresh, true, false); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{engine.ImportsFileName: false, engine.GeneratedFileName: false, engine.VariablesFileName: false, engine.ReadmeFileName: false, "notes.md": true} {
		if got := exists(filepath.Join(fresh, name)); got != want {
			t.Errorf("after --resume, %s: exists=%v, want %v", name, got, want)
		}
	}
}

// A root an earlier import finished may be in use: without --regenerate,
// it is refused, with what to pass, and stays as it is, checkpoint and
// all. That holds for --resume too, and once the root's configuration is
// split into files and imports.tf deleted after applying it, when no
// generated.tf or imports.tf says so.
func TestClaimRootRefusesARootAnImportFinished(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "333", "eu-west-2")
	done := &engine.Result{Imported: []engine.Import{{Type: "aws_vpc", Name: "main", ID: "vpc-1"}}, Files: []string{"network.tf"}}
	if err := saveCheckpoint(out, dir, "earlier", done); err != nil {
		t.Fatal(err)
	}
	for name, files := range map[string][]string{
		"with imports.tf":             {engine.ImportsFileName, engine.VersionsFileName, "network.tf"},
		"applied, imports.tf deleted": {engine.VersionsFileName, engine.ProvidersFileName, "network.tf"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			writeRoot(t, dir, files...)
			for _, resume := range []bool{false, true} {
				_, err := claimFor(out, dir, resume, false)
				if err == nil || !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "--regenerate") || !strings.Contains(err.Error(), "--incremental") {
					t.Errorf("resume=%v: want an error naming the root, --regenerate and --incremental, got %v", resume, err)
				}
			}
			for _, name := range files {
				if !exists(filepath.Join(dir, name)) {
					t.Errorf("%s was removed", name)
				}
			}
			if c, err := readCheckpoint(out, dir); err != nil || c.Fingerprint != "earlier" || !reflect.DeepEqual(c.Result, done) {
				t.Errorf("the earlier checkpoint changed: %+v, %v", c, err)
			}
		})
	}

	// With --regenerate it is cleared, but for what no import writes.
	writeRoot(t, dir, "notes.md")
	if _, err := claimFor(out, dir, false, true); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"network.tf": false, engine.VersionsFileName: true, "notes.md": true} {
		if got := exists(filepath.Join(dir, name)); got != want {
			t.Errorf("after --regenerate, %s: exists=%v, want %v", name, got, want)
		}
	}

	// A root of which no .tf file is left is generated again without one.
	gone := filepath.Join(out, "aws", "444", "eu-west-2")
	if err := saveCheckpoint(out, gone, "earlier", done); err != nil {
		t.Fatal(err)
	}
	if _, err := claimFor(out, gone, false, false); err != nil {
		t.Errorf("a root with no .tf file left: %v", err)
	}
}

const importsOfTwo = `import {
  to = module.network.aws_vpc.this[0]
  id = "vpc-1"
}

import {
  to = aws_subnet.a
  id = "subnet-1"
}
`

// The previous addresses come from the checkpoint, then the root's
// import blocks, then what the checkpoint says it imported; a root whose
// import didn't finish has none.
func TestPreviousAddresses(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "555", "eu-west-2")
	writeRoot(t, dir)
	if got, err := previousAddresses(out, dir); err != nil || got != nil {
		t.Fatalf("without a checkpoint: %v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, engine.ImportsFileName), []byte(importsOfTwo), 0o644); err != nil {
		t.Fatal(err)
	}
	fromImports := []engine.Managed{
		{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.a"},
		{Type: "aws_vpc", ID: "vpc-1", Address: "module.network.aws_vpc.this[0]"},
	}
	recorded := []engine.Managed{{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.private"}}
	older := &engine.Result{
		Imported: []engine.Import{{Type: "aws_vpc", Name: "main", ID: "vpc-1"}, {Type: "aws_subnet", Name: "a", ID: "subnet-1"}},
		Modules:  []engine.ModuleCall{{Name: "network", Resources: []string{"aws_vpc.main"}}},
	}
	inferred := []engine.Managed{
		{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.a", Unverified: true},
		{Type: "aws_vpc", ID: "vpc-1", Address: "module.network", Unverified: true},
	}
	pending := []engine.Managed{{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.pending"}}
	for name, tc := range map[string]struct {
		checkpoint checkpoint
		imports    bool
		want       []engine.Managed
	}{
		"recorded":                {checkpoint{Result: &engine.Result{Imported: older.Imported, Addresses: recorded}}, true, recorded},
		"from imports.tf":         {checkpoint{Result: older}, true, fromImports},
		"inferred":                {checkpoint{Result: older}, false, inferred},
		"pending":                 {checkpoint{Pending: pending}, true, pending},
		"an import didn't finish": {checkpoint{}, true, nil},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, engine.ImportsFileName)
			if tc.imports {
				if err := os.WriteFile(path, []byte(importsOfTwo), 0o644); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := writeCheckpoint(out, dir, tc.checkpoint); err != nil {
				t.Fatal(err)
			}
			got, err := previousAddresses(out, dir)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, %v, want %+v", got, err, tc.want)
			}
		})
	}
}

// Generating a root again saves where its resources were in its
// checkpoint before anything is removed, so that a run interrupted on the
// way loses neither them nor the files to remove: the next --resume
// finds both.
func TestClaimRootKeepsThePreviousAddressesUntilTheRootIsGenerated(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "666", "eu-west-2")
	writeRoot(t, dir, engine.VersionsFileName, "network.tf")
	addresses := []engine.Managed{{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.a"}}
	done := &engine.Result{Imported: []engine.Import{{Type: "aws_subnet", Name: "a", ID: "subnet-1"}}, Files: []string{"network.tf"}, Addresses: addresses}
	if err := saveCheckpoint(out, dir, "earlier", done); err != nil {
		t.Fatal(err)
	}

	claimed, err := claimFor(out, dir, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(claimed.previous, addresses) {
		t.Errorf("previous: got %+v, want %+v", claimed.previous, addresses)
	}
	c, err := readCheckpoint(out, dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Result != nil || c.Fingerprint != "fp" || !reflect.DeepEqual(c.Pending, addresses) || !reflect.DeepEqual(c.PendingFiles, []string{"network.tf"}) {
		t.Errorf("checkpoint while generating again: %+v", c)
	}
	if unfinished, err := unfinishedRoot(out, dir); err != nil || !unfinished {
		t.Errorf("unfinished: %v, %v", unfinished, err)
	}

	// The run stops here, with network.tf written again: --resume clears
	// it, and keeps the addresses.
	writeRoot(t, dir, "network.tf")
	claimed, err = claimFor(out, dir, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(claimed.previous, addresses) {
		t.Errorf("previous after the interruption: got %+v, want %+v", claimed.previous, addresses)
	}
	if exists(filepath.Join(dir, "network.tf")) {
		t.Error("network.tf wasn't removed")
	}
	if c, err := readCheckpoint(out, dir); err != nil || !reflect.DeepEqual(c.Pending, addresses) {
		t.Errorf("checkpoint after the interruption: %+v, %v", c, err)
	}

	// Once generated, the checkpoint records the result only.
	if err := saveCheckpoint(out, dir, "fp", done); err != nil {
		t.Fatal(err)
	}
	if c, err := readCheckpoint(out, dir); err != nil || c.Pending != nil || c.PendingFiles != nil {
		t.Errorf("checkpoint once generated: %+v, %v", c, err)
	}
}

// Generating a root again with the same addresses is kept; with other
// addresses, it is refused, naming them, and the root, files and
// checkpoint, is put back as it was.
func TestRefuseReaddressed(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "777", "eu-west-2")
	writeRoot(t, dir, engine.VersionsFileName, "network.tf", "notes.md")
	if err := os.WriteFile(filepath.Join(dir, "network.tf"), []byte("# as applied\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	addresses := []engine.Managed{{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.a"}}
	done := &engine.Result{Imported: []engine.Import{{Type: "aws_subnet", Name: "a", ID: "subnet-1"}}, Files: []string{"network.tf"}, Addresses: addresses}
	if err := saveCheckpoint(out, dir, "earlier", done); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(checkpointPath(out, dir))
	if err != nil {
		t.Fatal(err)
	}

	claimed, err := claimFor(out, dir, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := claimed.refuseReaddressed(dir, &engine.Result{Addresses: addresses}, nil, nil); err != nil {
		t.Errorf("the same addresses: %v", err)
	}

	// What generating it wrote.
	if err := os.WriteFile(filepath.Join(dir, "network.tf"), []byte("# generated again\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRoot(t, dir, engine.ImportsFileName, engine.ReadmeFileName)
	err = claimed.refuseReaddressed(dir, &engine.Result{Addresses: []engine.Managed{{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.private"}}}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "aws_subnet.a is now aws_subnet.private") || !strings.Contains(err.Error(), dir) {
		t.Fatalf("want an error naming the root and the new address, got %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "network.tf")); err != nil || string(got) != "# as applied\n" {
		t.Errorf("network.tf: %q, %v", got, err)
	}
	for name, want := range map[string]bool{engine.VersionsFileName: true, "notes.md": true, engine.ImportsFileName: false, engine.ReadmeFileName: false} {
		if got := exists(filepath.Join(dir, name)); got != want {
			t.Errorf("%s: exists=%v, want %v", name, got, want)
		}
	}
	if after, err := os.ReadFile(checkpointPath(out, dir)); err != nil || string(after) != string(before) {
		t.Errorf("checkpoint: %s, %v, want it as it was:\n%s", after, err, before)
	}
}

// A resource the root had that the run now puts in another root, or that
// generating the root leaves out, is refused too; one the run no longer
// imports at all, such as one deleted in the cloud, isn't.
func TestRefuseReaddressedCountsWhatLeavesTheRoot(t *testing.T) {
	claimed := &claim{previous: []engine.Managed{
		{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.a"},
		{Type: "aws_subnet", ID: "subnet-2", Address: "aws_subnet.b"},
		{Type: "aws_subnet", ID: "subnet-3", Address: "aws_subnet.c"},
		{Type: "aws_subnet", ID: "gone", Address: "aws_subnet.gone"},
	}}
	imports := []engine.Import{{Type: "aws_subnet", Name: "a", ID: "subnet-1"}, {Type: "aws_subnet", Name: "c", ID: "subnet-3"}}
	result := &engine.Result{Addresses: []engine.Managed{{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.a"}}}
	elsewhere := map[engine.External]string{{Type: "aws_subnet", ID: "subnet-2"}: "envs/prod/aws/111/eu-west-2"}

	err := claimed.refuseReaddressed("root", result, imports, elsewhere)
	if err == nil {
		t.Fatal("want the root refused")
	}
	for _, want := range []string{"2 of its resources", "aws_subnet.b is now in envs/prod/aws/111/eu-west-2", "aws_subnet.c is left out"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%v misses %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "aws_subnet.gone") {
		t.Errorf("%v names what the run doesn't import", err)
	}
}

// A root whose result moves resources resumes with moves.tf beside
// imports.tf, or, once applied, without both.
func TestResumedRequiresMovesWithImports(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "888", "eu-west-2")
	writeRoot(t, dir, engine.GeneratedFileName, engine.ImportsFileName, engine.VersionsFileName)
	result := &engine.Result{Moves: []engine.Move{{From: "aws_subnet.a", To: "aws_subnet.private"}}}
	if err := saveCheckpoint(out, dir, "fp", result); err != nil {
		t.Fatal(err)
	}
	if got, err := resumed(out, dir, "fp"); err != nil || got != nil {
		t.Errorf("without moves.tf: %+v, %v, want the root generated again", got, err)
	}
	writeRoot(t, dir, engine.MovesFileName)
	if got, err := resumed(out, dir, "fp"); err != nil || !reflect.DeepEqual(got, result) {
		t.Errorf("with moves.tf: %+v, %v", got, err)
	}
	for _, name := range []string{engine.ImportsFileName, engine.MovesFileName} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := resumed(out, dir, "fp"); err != nil || !reflect.DeepEqual(got, result) {
		t.Errorf("applied, without imports.tf and moves.tf: %+v, %v", got, err)
	}
}

// Generating a root again takes the moved blocks of its moves.tf, and
// the labels its resources had; its checkpoint keeps the blocks, which
// the root no longer has, until the root is generated.
func TestClaimRootKeepsTheMovesOfMovesTF(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "999", "eu-west-2")
	writeRoot(t, dir, engine.GeneratedFileName, engine.ImportsFileName, engine.VersionsFileName)
	moves := "moved {\n  from = aws_subnet.a\n  to   = aws_subnet.private\n}\n"
	if err := os.WriteFile(filepath.Join(dir, engine.MovesFileName), []byte(moves), 0o644); err != nil {
		t.Fatal(err)
	}
	done := &engine.Result{
		Imported:  []engine.Import{{Type: "aws_subnet", Name: "private", ID: "subnet-1"}},
		Addresses: []engine.Managed{{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.private"}},
	}
	if err := saveCheckpoint(out, dir, "earlier", done); err != nil {
		t.Fatal(err)
	}
	want := []engine.Move{{From: "aws_subnet.a", To: "aws_subnet.private"}}

	claimed, err := claimFor(out, dir, false, true)
	if err != nil {
		t.Fatal(err)
	}
	previous := claimed.previousRoot()
	if previous == nil || !reflect.DeepEqual(previous.Moved, want) || previous.Labels[engine.External{Type: "aws_subnet", ID: "subnet-1"}] != "private" {
		t.Fatalf("previous root: %+v", previous)
	}
	if exists(filepath.Join(dir, engine.MovesFileName)) {
		t.Errorf("%s left in the root", engine.MovesFileName)
	}

	// A run that stopped on the way: the checkpoint still has them.
	again, err := claimFor(out, dir, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.previousRoot(); got == nil || !reflect.DeepEqual(got.Moved, want) || !reflect.DeepEqual(got.Addresses, done.Addresses) {
		t.Errorf("after a run that stopped: %+v", got)
	}
}

// With moved blocks for what moved, a root is still refused for what
// leaves it, is of another type now, but for another name of its type,
// or the engine couldn't move.
func TestRefuseReaddressedWithMoves(t *testing.T) {
	claimed := &claim{previous: []engine.Managed{
		{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.a"},
		{Type: "aws_subnet", ID: "subnet-2", Address: "aws_subnet.b"},
		{Type: "aws_s3_bucket", ID: "data", Address: "aws_s3_bucket.data"},
		{Type: "aws_alb", ID: "arn:lb", Address: "aws_alb.web"},
		{Type: "aws_alb", ID: "arn:edge", Address: "aws_alb.edge"},
	}}
	result := &engine.Result{
		Addresses: []engine.Managed{
			{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.private"},
			{Type: "aws_lb", ID: "arn:lb", Address: "aws_lb.web"},
			{Type: "aws_s3_bucket_policy", ID: "data", Address: "aws_s3_bucket_policy.data"},
		},
		Moves:   []engine.Move{{From: "aws_subnet.a", To: "aws_subnet.private"}},
		Retyped: []engine.Retype{{Type: "aws_alb", ID: "arn:lb", From: "aws_alb.web", To: "aws_lb.web", Removed: "aws_alb.web"}},
	}
	// An aws_alb another root imports as an aws_lb is in that root now.
	elsewhere := map[engine.External]string{{Type: "aws_subnet", ID: "subnet-2"}: "envs/prod/aws/111/eu-west-2", {Type: "aws_lb", ID: "arn:edge"}: "envs/prod/aws/111/eu-west-2"}
	if err := claimed.refuseReaddressed("root", &engine.Result{Addresses: result.Addresses[:2], Moves: result.Moves, Retyped: result.Retyped}, nil, nil); err != nil {
		t.Errorf("only what moved and removed blocks follow: %v", err)
	}

	err := claimed.refuseReaddressed("root", result, nil, elsewhere)
	if err == nil {
		t.Fatal("want the root refused")
	}
	for _, want := range []string{"3 of its resources", "aws_subnet.b is now in envs/prod/aws/111/eu-west-2", "aws_alb.edge is now in envs/prod/aws/111/eu-west-2", "aws_s3_bucket.data is now aws_s3_bucket_policy.data, of another type"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%v misses %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "aws_subnet.a") || strings.Contains(err.Error(), "aws_alb.web") {
		t.Errorf("%v names what a moved or removed block follows", err)
	}

	result.Unmoved = []string{"the moves from aws_subnet.x come back to aws_subnet.x"}
	if err := claimed.refuseReaddressed("root", &engine.Result{Addresses: result.Addresses[:1], Moves: result.Moves, Unmoved: result.Unmoved}, nil, nil); err == nil || !strings.Contains(err.Error(), "come back") {
		t.Errorf("with moves the engine couldn't write: %v", err)
	}
}

// A root applied without a backend keeps its local state when it is
// generated again and put back: claiming it leaves the state, and putting
// it back neither writes back the state it had, which Terraform may have
// written since, nor removes the state files Terraform wrote.
func TestPutBackKeepsLocalState(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "777", "eu-west-2")
	writeRootFiles(t, dir, map[string]string{"network.tf": "# as applied\n", "terraform.tfstate": "serial 1"})
	addresses := []engine.Managed{{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.a"}}
	done := &engine.Result{Imported: []engine.Import{{Type: "aws_subnet", Name: "a", ID: "subnet-1"}}, Files: []string{"network.tf"}, Addresses: addresses}
	if err := saveCheckpoint(out, dir, "earlier", done); err != nil {
		t.Fatal(err)
	}

	claimed, err := claimFor(out, dir, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "terraform.tfstate")); err != nil || string(got) != "serial 1" {
		t.Fatalf("terraform.tfstate once claimed: %q, %v", got, err)
	}
	// What generating it wrote, and Terraform since.
	writeRootFiles(t, dir, map[string]string{"network.tf": "# generated again\n", "terraform.tfstate": "serial 2", "terraform.tfstate.backup": "serial 1"})
	if err := claimed.putBack(dir); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{"network.tf": "# as applied\n", "terraform.tfstate": "serial 2", "terraform.tfstate.backup": "serial 1"} {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != want {
			t.Errorf("%s: %q, %v, want %q", name, got, err, want)
		}
	}
}
