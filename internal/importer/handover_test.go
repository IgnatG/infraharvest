// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// earlierRoot records dir, under out, as a root an earlier import
// generated with addresses, as it is once applied: with configuration,
// but without its import blocks.
func earlierRoot(t *testing.T, out, dir string, addresses ...engine.Managed) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, engine.GeneratedFileName), []byte("# generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := &engine.Result{Addresses: addresses}
	for _, m := range addresses {
		result.Imported = append(result.Imported, engine.Import{Type: m.Type, ID: m.ID, Name: m.ID})
	}
	if err := saveCheckpoint(out, dir, "fp", result); err != nil {
		t.Fatal(err)
	}
}

func thing(id, address string) engine.Managed {
	return engine.Managed{Type: "fake_thing", ID: id, Address: address}
}

func thingImport(id string) engine.Import {
	return engine.Import{Type: "fake_thing", ID: id, Name: id}
}

// regenerating returns a listed call of an import with --regenerate into
// out, of byDir.
func regenerating(out string, byDir map[string][]engine.Import) *listedCall {
	return &listedCall{run: NewRun(), options: Options{Out: out, Regenerate: true}, scope: "scope", byDir: byDir}
}

// excludedIDs are the IDs run excludes as held by another root, with the
// reason's root.
func excludedIDs(run *Run) []string {
	var ids []string
	for _, e := range run.report.Excluded {
		if root, ok := strings.CutPrefix(e.Reason, InOtherRootReason+", "); ok {
			ids = append(ids, e.ID+" "+root)
		}
	}
	slices.Sort(ids)
	return ids
}

// A resource that goes from one root of the run to another is handed
// over: the root it leaves gets a removed block, the root it goes to
// imports it, knowing where from.
func TestPlanHandoversBetweenRootsOfTheRun(t *testing.T) {
	out := t.TempDir()
	old, next := filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next")
	earlierRoot(t, out, old, thing("a", "fake_thing.a"), thing("b", "fake_thing.b"))
	c := regenerating(out, map[string][]engine.Import{old: {thingImport("b")}, next: {thingImport("a")}})

	if err := planHandovers(out, []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	h := c.handovers
	if err := h.refusal(old); err != nil {
		t.Fatalf("old refused: %v", err)
	}
	want := []engine.Removal{{From: "fake_thing.a", Resources: []engine.Departure{{Type: "fake_thing", ID: "a", Address: "fake_thing.a", Root: "fake/next"}}}}
	if got := h.removalsOf(old); !reflect.DeepEqual(got, want) {
		t.Errorf("old's removals: got %+v, want %+v", got, want)
	}
	if got, want := h.arrivalsOf(next), []engine.Arrival{{Type: "fake_thing", ID: "a", Root: "fake/old", From: "fake_thing.a"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("next's arrivals: got %+v, want %+v", got, want)
	}
	if len(c.byDir[next]) != 1 || len(c.run.report.Excluded) != 0 {
		t.Errorf("next imports %+v, excluded %+v, want a", c.byDir[next], c.run.report.Excluded)
	}
}

// What a root the run doesn't generate has stays there, applied or not:
// a removed block would need it edited. It is left out of the root it
// would go to.
func TestPlanHandoversLeavesOutWhatARootNotInTheRunHas(t *testing.T) {
	out := t.TempDir()
	old, next := filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next")
	earlierRoot(t, out, old, thing("a", "fake_thing.a"), thing("b", "fake_thing.b"))
	c := regenerating(out, map[string][]engine.Import{next: {thingImport("a"), thingImport("c")}})

	if err := planHandovers(out, []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	if got := excludedIDs(c.run); !slices.Equal(got, []string{"a fake/old"}) {
		t.Errorf("excluded %v, want a, held by fake/old", got)
	}
	if got := c.byDir[next]; len(got) != 1 || got[0].ID != "c" {
		t.Errorf("next imports %+v, want c only", got)
	}
	if _, ok := c.byDir[old]; ok || len(c.handovers.removalsOf(old)) != 0 {
		t.Errorf("old is generated: %+v", c.byDir)
	}
}

// A root all of whose resources go to roots of the run is generated with
// its removed blocks alone, by the call of the first root they go to.
func TestPlanHandoversVacatesARootThatHasNothingLeft(t *testing.T) {
	out := t.TempDir()
	old := filepath.Join(out, "fake", "old")
	alpha, beta := filepath.Join(out, "fake", "alpha"), filepath.Join(out, "fake", "beta")
	earlierRoot(t, out, old, thing("a", "fake_thing.a"), thing("b", "fake_thing.b"))
	first := regenerating(out, map[string][]engine.Import{beta: {thingImport("b")}})
	second := regenerating(out, map[string][]engine.Import{alpha: {thingImport("a")}})

	if err := planHandovers(out, []*listedCall{first, second}); err != nil {
		t.Fatal(err)
	}
	if imports, ok := second.byDir[old]; !ok || len(imports) != 0 {
		t.Errorf("the call of alpha, where fake_thing.a goes, doesn't generate old alone: %+v", second.byDir)
	}
	if _, ok := first.byDir[old]; ok {
		t.Errorf("two calls generate old")
	}
	if got := first.handovers.removalsOf(old); len(got) != 2 || got[0].From != "fake_thing.a" || got[1].From != "fake_thing.b" {
		t.Errorf("old's removals: %+v", got)
	}
}

// A removed block takes no instance keys and a whole module call: a root
// of which only some instances, or part of a module call, go is left as
// it was, and what would go is left out of the roots it goes to.
func TestPlanHandoversRefusesWhatARemovedBlockCantTake(t *testing.T) {
	for name, tt := range map[string]struct {
		had  []engine.Managed
		want string
	}{
		"instances": {
			had:  []engine.Managed{thing("a", "fake_thing.group[0]"), thing("b", "fake_thing.group[1]")},
			want: "only some instances of fake_thing.group go to fake/next, and a removed block takes no instance keys",
		},
		"module call": {
			had:  []engine.Managed{thing("a", "module.m.fake_thing.x"), thing("b", "module.m.fake_thing.y")},
			want: "only part of module.m goes to fake/next, and a removed block takes the whole module call",
		},
		"inferred address": {
			had:  []engine.Managed{{Type: "fake_thing", ID: "a", Address: "fake_thing.a", Unverified: true}, thing("b", "fake_thing.b")},
			want: "only inferred",
		},
	} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			old, next := filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next")
			earlierRoot(t, out, old, tt.had...)
			c := regenerating(out, map[string][]engine.Import{old: {thingImport("b")}, next: {thingImport("a"), thingImport("c")}})

			if err := planHandovers(out, []*listedCall{c}); err != nil {
				t.Fatal(err)
			}
			if err := c.handovers.refusal(old); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("old's refusal: %v, want %q", err, tt.want)
			}
			if len(c.handovers.removalsOf(old)) != 0 || len(c.handovers.arrivalsOf(next)) != 0 {
				t.Errorf("handed over all the same: %+v", c.handovers)
			}
			if got := excludedIDs(c.run); !slices.Equal(got, []string{"a fake/old"}) {
				t.Errorf("excluded %v, want a, held by fake/old", got)
			}
		})
	}
}

// A whole module call that goes gets one removed block, for the call.
func TestPlanHandoversRemovesAWholeModuleCall(t *testing.T) {
	out := t.TempDir()
	old, next := filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next")
	earlierRoot(t, out, old, thing("a", "module.m.fake_thing.x[0]"), thing("b", "module.m.fake_thing.x[1]"), thing("c", "fake_thing.c"))
	c := regenerating(out, map[string][]engine.Import{old: {thingImport("c")}, next: {thingImport("a"), thingImport("b")}})

	if err := planHandovers(out, []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	if got := c.handovers.removalsOf(old); len(got) != 1 || got[0].From != "module.m" || len(got[0].Resources) != 2 {
		t.Errorf("old's removals: %+v", got)
	}
}

// A moved block of moves.tf that a plan may not have applied yet, which
// moves what would go, keeps it from going; and a root that hands a
// resource over to a root left as it was is left as it was too.
func TestPlanHandoversRefusesWhatMovesTFStillMoves(t *testing.T) {
	out := t.TempDir()
	old, next, other := filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next"), filepath.Join(out, "fake", "other")
	earlierRoot(t, out, old, thing("a", "fake_thing.a"), thing("b", "fake_thing.b"))
	if err := os.WriteFile(filepath.Join(old, engine.MovesFileName), []byte("moved {\n  from = fake_thing.was\n  to   = fake_thing.a\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	earlierRoot(t, out, other, thing("c", "fake_thing.c"), thing("d", "fake_thing.d"))
	c := regenerating(out, map[string][]engine.Import{old: {thingImport("b"), thingImport("c")}, next: {thingImport("a")}, other: {thingImport("d")}})

	if err := planHandovers(out, []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	if err := c.handovers.refusal(old); err == nil || !strings.Contains(err.Error(), "moves.tf still moves it") {
		t.Errorf("old's refusal: %v", err)
	}
	if err := c.handovers.refusal(other); err == nil || !strings.Contains(err.Error(), "fake_thing.c goes to fake/old, which is left as it was") {
		t.Errorf("other's refusal: %v", err)
	}
	if got := excludedIDs(c.run); !slices.Equal(got, []string{"a fake/old", "c fake/other"}) {
		t.Errorf("excluded %v", got)
	}
}

// Without --regenerate, nothing is handed over.
func TestPlanHandoversOnlyWithRegenerate(t *testing.T) {
	out := t.TempDir()
	old, next := filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next")
	earlierRoot(t, out, old, thing("a", "fake_thing.a"))
	c := regenerating(out, map[string][]engine.Import{next: {thingImport("a")}})
	c.options.Regenerate = false
	if err := planHandovers(out, []*listedCall{c, nil}); err != nil {
		t.Fatal(err)
	}
	if c.handovers != nil || len(c.byDir[next]) != 1 {
		t.Errorf("handovers %+v, next %+v", c.handovers, c.byDir[next])
	}
}

// With --regenerate and a layout that puts every resource in one root,
// the roots they were in hand them over: each is left with its removed
// blocks alone, the new root imports them, and the report says what went
// where.
func TestListCallRegenerateHandsResourcesOver(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	importPath(t, pathOptions(out, &fakeEngine{}, "alpha", "beta"))
	alpha := filepath.Join(out, "fake", "alpha")

	f := &fakeEngine{}
	options := pathOptions(out, f, "alpha", "beta")
	options.Layout = "{out}/{provider}/all/"
	options.Regenerate = true
	run := importPath(t, options)

	if len(run.failures) != 0 {
		t.Fatalf("failures %v", run.failures)
	}
	if !slices.Equal(f.generated, []string{"all"}) {
		t.Errorf("generated %v, want the new root, and the others vacated", f.generated)
	}
	if got := f.opts["all"].Arrivals; len(got) != 2 || got[0].Root != "fake/alpha" || got[1].Root != "fake/beta" {
		t.Errorf("all's arrivals: %+v", got)
	}
	if got := directoryPaths(run); !slices.Equal(got, []string{"fake/all", "fake/alpha", "fake/beta"}) {
		t.Errorf("reported directories %v", got)
	}
	moves := readFileIn(t, alpha, engine.MovesFileName)
	if !strings.Contains(moves, "removed {\n  from = fake_thing.alpha\n") || !strings.Contains(moves, "destroy = false") {
		t.Errorf("alpha's %s:\n%s", engine.MovesFileName, moves)
	}
	if readme := readFileIn(t, alpha, engine.ReadmeFileName); !strings.Contains(readme, "`fake/all`") {
		t.Errorf("alpha's README doesn't say where its resource goes:\n%s", readme)
	}
	for _, name := range []string{engine.GeneratedFileName, engine.ImportsFileName} {
		if _, err := os.Stat(filepath.Join(alpha, name)); err == nil {
			t.Errorf("alpha keeps %s", name)
		}
	}
	c, err := readCheckpoint(out, alpha)
	if err != nil || c == nil || c.Result == nil || len(c.Result.Removed) != 1 {
		t.Fatalf("alpha's checkpoint: %+v, %v", c, err)
	}

	run.report.Finish(run.discovered, run.failed, false)
	want := []report.Move{
		{Type: "fake_thing", ID: "id-alpha", FromRoot: "fake/alpha", From: "fake_thing.alpha", ToRoot: "fake/all", To: "fake_thing.alpha"},
		{Type: "fake_thing", ID: "id-beta", FromRoot: "fake/beta", From: "fake_thing.beta", ToRoot: "fake/all", To: "fake_thing.beta"},
	}
	if !reflect.DeepEqual(run.report.Departed, want) {
		t.Errorf("departed:\ngot  %+v\nwant %+v", run.report.Departed, want)
	}
	if md := run.report.Markdown(); !strings.Contains(md, "## Handed over") || !strings.Contains(md, "- `fake_thing.alpha` in `fake/alpha` to `fake_thing.alpha` in `fake/all`") {
		t.Errorf("report.md:\n%s", md)
	}

	// Generated again, the vacated roots keep their removed blocks while
	// nothing goes from them, and the new root imports the same.
	f = &fakeEngine{}
	options.tools = f.tools()
	run = importPath(t, options)
	if len(run.failures) != 0 || !slices.Equal(f.generated, []string{"all"}) {
		t.Errorf("generated %v, failures %v", f.generated, run.failures)
	}
	if _, err := os.Stat(filepath.Join(alpha, engine.MovesFileName)); err != nil {
		t.Errorf("alpha's %s: %v", engine.MovesFileName, err)
	}
}

// With --regenerate, a root the run doesn't generate keeps what it has,
// and a root that would take it imports the rest.
func TestListCallRegenerateLeavesARootOutOfTheRunAsItIs(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	first := pathOptions(out, &fakeEngine{}, "alpha", "beta")
	first.Layout = "{out}/{provider}/all/"
	importPath(t, first)

	f := &fakeEngine{}
	options := pathOptions(out, f, "alpha", "gamma")
	options.Regenerate = true
	run := importPath(t, options)

	if !slices.Equal(f.generated, []string{"gamma"}) {
		t.Errorf("generated %v, want gamma only", f.generated)
	}
	if got := excludedIDs(run); !slices.Equal(got, []string{"id-alpha fake/all"}) {
		t.Errorf("excluded %v", got)
	}
}

func readFileIn(t *testing.T, dir, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
