// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// appliedFixture is a fixture under testdata, applied in a directory of
// its own, which a test applies again with other variables.
type appliedFixture struct {
	name string
	tf   *tfrun.Runner
}

// seedApplied applies testdata/<name> in the emulator, with opts, such as
// its variables.
func seedApplied(ctx context.Context, t *testing.T, name, execPath, plugins string, opts ...tfexec.ApplyOption) *appliedFixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(readFile(t, filepath.Join("testdata", name, "main.tf"))), 0o644); err != nil {
		t.Fatal(err)
	}
	tf, err := tfrun.New(dir, execPath, plugins, nil)
	if err != nil {
		t.Fatal(err)
	}
	tf.SetStdout(os.Stdout)
	tf.SetStderr(os.Stderr)
	if err := tf.Init(ctx); err != nil {
		t.Fatalf("seed: terraform init: %v", err)
	}
	f := &appliedFixture{name: name, tf: tf}
	f.apply(ctx, t, opts...)
	return f
}

// apply applies the fixture with opts, such as its variables.
func (f *appliedFixture) apply(ctx context.Context, t *testing.T, opts ...tfexec.ApplyOption) {
	t.Helper()
	if err := f.tf.Apply(ctx, opts...); err != nil {
		t.Fatalf("seed: terraform apply: %v", err)
	}
}

// resources returns the resources the fixture's state has now.
func (f *appliedFixture) resources(ctx context.Context, t *testing.T) []*tfjson.StateResource {
	t.Helper()
	state, err := f.tf.Show(ctx)
	if err != nil {
		t.Fatalf("seed: terraform show: %v", err)
	}
	if state.Values == nil || state.Values.RootModule == nil {
		t.Fatalf("testdata/%s has no resources in its state", f.name)
	}
	return state.Values.RootModule.Resources
}

// runMovesImport runs infraharvest with args, failing the test if it
// fails.
func runMovesImport(ctx context.Context, t *testing.T, args ...string) {
	t.Helper()
	root := cli.NewCmdRoot()
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("infraharvest %s: %v", strings.Join(args, " "), err)
	}
}

// importedAt returns the address the root at path, as coverage reports
// it, imports the resource with id at; it fails the test if it doesn't.
func importedAt(t *testing.T, coverage report.Report, path, id string) string {
	t.Helper()
	for _, d := range coverage.Directories {
		if d.Path != path {
			continue
		}
		for _, r := range d.Imported {
			if r.ID == id {
				return r.Address
			}
		}
	}
	t.Fatalf("%s doesn't import %s: %+v", path, id, coverage.Directories)
	return ""
}

// copyRootFiles copies the files of the root in dir, but no state or
// Terraform working directory, into dst, as a team copies a root
// generated again into its repository.
func copyRootFiles(t *testing.T, dir, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || strings.Contains(e.Name(), ".tfstate") {
			continue
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), []byte(readFile(t, filepath.Join(dir, e.Name()))), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// clearRootFiles removes the .tf files of the root in dir, keeping its
// state, for the files of a root generated again.
func clearRootFiles(t *testing.T, dir string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

// handover is a test of a resource that a root generated again hands over
// to another root, for a provider whose resources keep their names, and
// so their addresses, as long as they exist: the AWS test, whose queues
// are named by a tag, covers moved blocks too.
type handover struct {
	ctx               context.Context
	execPath, plugins string
	// typ is the type of handed and kept, the import IDs of the resource
	// handed over and of one that stays where it is, at its address.
	typ, handed, kept string
	// args import the fixture, with roots per environment, into out.
	args []string
	out  string
	// emulatorFailure, if set, reports whether an import's failure is
	// the emulator's (see checkCoverage).
	emulatorFailure func(string) bool
	// change gives handed another environment in the emulator.
	change func(t *testing.T)
}

// check imports the fixture, applies the root handed and kept are in,
// keeping its state, then changes the estate (h.change) and generates the
// roots again (--regenerate). The root it applied gets a removed block for
// handed in moves.tf, which the gate checks (G8), and keeps kept at its
// address; the root of handed's new environment imports it, and the
// READMEs and coverage.json say so. Over the state the first apply left,
// the root then forgets handed, creating and destroying nothing; once
// both roots are applied, in that order, neither plans any change.
func (h handover) check(t *testing.T) {
	t.Helper()
	first := h.applyFirst(t)
	h.change(t)
	runPartialImport(h.ctx, t, append(slices.Clone(h.args), "--regenerate")...)
	h.finish(t, first)
}

// checkHalfDone checks a handover one of whose roots fails, as check
// does but for a run of --regenerate that can't write the root handed
// leaves: the root it goes to, generated first, imports it, and the root
// it leaves fails, so its state still has it. coverage.json and the
// report say the handover isn't finished, naming that root, and that it
// is to be generated again before either root is applied. Generated
// again, the handover finishes as check's does.
func (h handover) checkHalfDone(t *testing.T) {
	t.Helper()
	first := h.applyFirst(t)
	h.change(t)
	if err := os.Chmod(first.dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(first.dir, 0o755) })
	if f, err := os.CreateTemp(first.dir, "writable"); err == nil {
		_ = f.Close()
		t.Fatalf("%s can still be written, as by root, so generating it again can't fail", first.dir)
	}
	runPartialImport(h.ctx, t, append(slices.Clone(h.args), "--regenerate", "--allow-partial")...)
	if err := os.Chmod(first.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	half := coverageOf(t, h.out)
	var incomplete []report.IncompleteHandover
	for _, i := range half.IncompleteHandovers {
		if i.ID == h.handed {
			incomplete = append(incomplete, i)
		}
	}
	if len(incomplete) != 1 {
		t.Fatalf("a failed root: incomplete handovers %+v, want one of %s", half.IncompleteHandovers, h.handed)
	}
	i := incomplete[0]
	if i.FailedRoot != first.root || i.FromRoot != first.root || i.From != first.handedFrom || i.ToRoot == first.root || i.To == "" || !strings.Contains(i.Warning, "--regenerate") {
		t.Errorf("a failed root: incomplete handover %+v, want %s failed, handing %s over", i, first.root, first.handedFrom)
	}
	if at := importedAt(t, half, i.ToRoot, h.handed); at != i.To {
		t.Errorf("a failed root: %s imports %s at %s, coverage.json hands it over to %s", i.ToRoot, h.handed, at, i.To)
	}
	if !slices.ContainsFunc(half.Directories, func(d report.Directory) bool { return d.Path == first.root && d.Error != "" }) {
		t.Errorf("a failed root: coverage.json doesn't report %s failed: %+v", first.root, half.Directories)
	}
	if _, err := os.Stat(filepath.Join(first.dir, engine.MovesFileName)); err == nil {
		t.Errorf("a failed root: %s has a %s", first.root, engine.MovesFileName)
	}
	if md := readFile(t, filepath.Join(h.out, report.Dir, "report.md")); !strings.Contains(md, "Handovers not finished") || !strings.Contains(md, i.Warning) {
		t.Errorf("a failed root: report.md doesn't list the handover with %q:\n%s", i.Warning, md)
	}

	// Generated again, as the warning says: the handover finishes.
	runPartialImport(h.ctx, t, append(slices.Clone(h.args), "--regenerate")...)
	h.finish(t, first)
}

// appliedRoot is the root the first import of a handover's test puts
// handed and kept in, applied in a working directory of its own.
type appliedRoot struct {
	// root is its path in the output directory, dir its directory there,
	// and work the directory it is applied in, which keeps its state.
	root, dir, work string
	// handedFrom and keptAt are the addresses of handed and kept in it.
	handedFrom, keptAt string
}

// applyFirst imports the fixture and applies the root handed and kept
// are in, keeping its state, as its README says, with its import blocks
// deleted.
func (h *handover) applyFirst(t *testing.T) appliedRoot {
	t.Helper()
	runPartialImport(h.ctx, t, h.args...)
	first := coverageOf(t, h.out)
	checkCoverage(t, "the first import", first, h.emulatorFailure)
	// The IDs as the listers give them, which Azure's, unlike the state's,
	// may spell in another case.
	h.handed, h.kept = listedID(t, first, h.handed), listedID(t, first, h.kept)
	fromRoot := rootOf(t, first, h.handed)
	if keptRoot := rootOf(t, first, h.kept); keptRoot != fromRoot {
		t.Fatalf("the first import: %s is in %s, %s in %s, want both in one root", h.handed, fromRoot, h.kept, keptRoot)
	}
	applied := appliedRoot{
		root:       fromRoot,
		dir:        filepath.Join(h.out, filepath.FromSlash(fromRoot)),
		work:       filepath.Join(t.TempDir(), "from"),
		handedFrom: importedAt(t, first, fromRoot, h.handed),
		keptAt:     importedAt(t, first, fromRoot, h.kept),
	}
	copyRootFiles(t, applied.dir, applied.work)
	applyRoot(h.ctx, t, applied.work, h.execPath, h.plugins, nil)
	if err := os.Remove(filepath.Join(applied.work, engine.ImportsFileName)); err != nil {
		t.Fatal(err)
	}
	return applied
}

// finish checks the roots generated again (--regenerate) after h.change
// hand handed over from first, the root applyFirst applied, as check
// says, and applies them in order.
func (h handover) finish(t *testing.T, first appliedRoot) {
	t.Helper()
	fromRoot, handedFrom, keptAt, from, workFrom := first.root, first.handedFrom, first.keptAt, first.dir, first.work
	workTo := filepath.Join(filepath.Dir(workFrom), "to")
	second := coverageOf(t, h.out)
	checkCoverage(t, "--regenerate", second, h.emulatorFailure)

	var toRoot, handedTo string
	for _, d := range second.Departed {
		if d.ID == h.handed && d.FromRoot == fromRoot && d.From == handedFrom && d.ToRoot != fromRoot {
			toRoot, handedTo = d.ToRoot, d.To
		}
	}
	if toRoot == "" || handedTo == "" {
		t.Fatalf("--regenerate: coverage.json doesn't hand %s over from %s to another root that imports it: departed %+v", h.handed, fromRoot, second.Departed)
	}
	if at := importedAt(t, second, toRoot, h.handed); at != handedTo {
		t.Errorf("--regenerate: %s imports %s at %s, coverage.json hands it over to %s", toRoot, h.handed, at, handedTo)
	}
	if at := importedAt(t, second, fromRoot, h.kept); at != keptAt || slices.ContainsFunc(second.Moved, func(m report.Move) bool { return m.ID == h.kept }) {
		t.Errorf("--regenerate: %s is at %s, was at %s; moved %+v", h.kept, at, keptAt, second.Moved)
	}
	if len(second.IncompleteHandovers) != 0 {
		t.Errorf("--regenerate: incomplete handovers %+v", second.IncompleteHandovers)
	}
	moves := strings.Join(strings.Fields(readFile(t, filepath.Join(from, engine.MovesFileName))), " ")
	if want := "removed { from = " + handedFrom + " lifecycle { destroy = false } }"; !strings.Contains(moves, want) {
		t.Errorf("%s/%s doesn't have %q:\n%s", fromRoot, engine.MovesFileName, want, moves)
	}
	for _, d := range second.Directories {
		if d.Path == fromRoot && !slices.ContainsFunc(d.Checks, func(c report.Check) bool { return c.Name == engine.CheckMoves && c.Passed && !c.Skipped }) {
			t.Errorf("%s: the gate doesn't pass %s: %+v", fromRoot, engine.CheckMoves, d.Checks)
		}
	}
	to := filepath.Join(h.out, filepath.FromSlash(toRoot))
	// Each README names the other root, for the order to apply them in.
	for _, readme := range []struct{ dir, other string }{{from, toRoot}, {to, fromRoot}} {
		if content := readFile(t, filepath.Join(readme.dir, engine.ReadmeFileName)); !strings.Contains(content, readme.other) {
			t.Errorf("%s doesn't name %s:\n%s", filepath.Join(readme.dir, engine.ReadmeFileName), readme.other, content)
		}
	}

	// The root generated again, over the state the first apply left:
	// handed is forgotten, nothing created or destroyed.
	clearRootFiles(t, workFrom)
	copyRootFiles(t, from, workFrom)
	forgotten := false
	for _, rc := range planRoot(h.ctx, t, workFrom, h.execPath, h.plugins).ResourceChanges {
		if rc.Mode != tfjson.ManagedResourceMode {
			continue
		}
		switch {
		case rc.Change.Actions.Create() || rc.Change.Actions.Delete() || rc.Change.Actions.Replace():
			t.Errorf("%s: %s is planned to %v", fromRoot, rc.Address, rc.Change.Actions)
		case rc.Address == handedFrom && rc.Change.Actions.Forget():
			forgotten = true
		}
	}
	if !forgotten {
		t.Errorf("%s: the plan doesn't forget %s", fromRoot, handedFrom)
	}
	applyRoot(h.ctx, t, workFrom, h.execPath, h.plugins, nil)

	// Then the root it goes to imports it, and neither root changes more.
	copyRootFiles(t, to, workTo)
	if imported := checkNoChanges(h.ctx, t, workTo, h.execPath, h.plugins, nil); imported[h.typ] != 1 {
		t.Errorf("%s: imports %v, want the %s handed over", toRoot, imported, h.typ)
	}
	applyRoot(h.ctx, t, workTo, h.execPath, h.plugins, nil)
	for _, dir := range []string{workFrom, workTo} {
		for _, rc := range planRoot(h.ctx, t, dir, h.execPath, h.plugins).ResourceChanges {
			if rc.Mode == tfjson.ManagedResourceMode && (!rc.Change.Actions.NoOp() || rc.Change.Importing != nil || rc.PreviousAddress != "") {
				t.Errorf("%s: after the apply, %s is planned to %v", dir, rc.Address, rc.Change.Actions)
			}
		}
	}
}

// listedID returns the import ID of the resource coverage reports with id,
// in any case; it fails the test if it reports none.
func listedID(t *testing.T, coverage report.Report, id string) string {
	t.Helper()
	for _, d := range coverage.Directories {
		for _, r := range d.Imported {
			if strings.EqualFold(r.ID, id) {
				return r.ID
			}
		}
	}
	t.Fatalf("no root imports %s: %+v", id, coverage.Directories)
	return ""
}

// rootOf returns the root, as coverage reports it, that imports the
// resource with id; it fails the test if none does.
func rootOf(t *testing.T, coverage report.Report, id string) string {
	t.Helper()
	for _, d := range coverage.Directories {
		for _, r := range d.Imported {
			if r.ID == id {
				return d.Path
			}
		}
	}
	t.Fatalf("no root imports %s: %+v", id, coverage.Directories)
	return ""
}

// runPartialImport runs infraharvest with args, failing the test unless
// it imports everything or, with --allow-partial, some of it: the checks
// on coverage.json then fail the test for what isn't the emulator's.
func runPartialImport(ctx context.Context, t *testing.T, args ...string) {
	t.Helper()
	root := cli.NewCmdRoot()
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); importer.ExitCode(err) != report.ExitOK && importer.ExitCode(err) != report.ExitPartial {
		t.Fatalf("infraharvest %s: %v", strings.Join(args, " "), err)
	}
}
