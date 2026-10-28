// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
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
	if err := os.WriteFile(filepath.Join(dir, rootfile.GeneratedFileName), []byte("# generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := &engine.Result{Addresses: addresses}
	for _, m := range addresses {
		result.Imported = append(result.Imported, rootfile.Import{Type: m.Type, ID: m.ID, Name: m.ID})
	}
	if err := saveCheckpoint(out, dir, "fp", result); err != nil {
		t.Fatal(err)
	}
}

func thing(id, address string) engine.Managed {
	return engine.Managed{Type: "fake_thing", ID: id, Address: address}
}

func thingImport(id string) rootfile.Import {
	return rootfile.Import{Type: "fake_thing", ID: id, Name: id}
}

// regenerating returns a listed call of an import with --regenerate into
// out, of byDir.
func regenerating(out string, byDir map[string][]rootfile.Import) *listedCall {
	return &listedCall{run: NewRun(), options: Options{Out: out, Regenerate: true}, scope: "scope", byDir: byDir}
}

// excludedIDs are the IDs run excludes as held by another root, with the
// reason's root.
func excludedIDs(run *Run) []string {
	var ids []string
	for _, e := range run.report.Excluded {
		if root, ok := strings.CutPrefix(e.Reason, whyText(report.InOtherRoot(""))); ok {
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
	m := newMemoryOutput(out)
	old, next := filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next")
	m.earlier(old, thing("a", "fake_thing.a"), thing("b", "fake_thing.b"))
	c := regenerating(out, map[string][]rootfile.Import{old: {thingImport("b")}, next: {thingImport("a")}})

	if err := planHandovers(m.index(), []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	h := c.handovers
	if err := h.refusal(old); err != nil {
		t.Fatalf("old refused: %v", err)
	}
	want := []rootfile.Removal{{From: "fake_thing.a", Resources: []rootfile.Departure{{Type: "fake_thing", ID: "a", Address: "fake_thing.a", Root: "fake/next"}}}}
	if got := h.removalsOf(old); !reflect.DeepEqual(got, want) {
		t.Errorf("old's removals: got %+v, want %+v", got, want)
	}
	if got, want := h.arrivalsOf(next), []rootfile.Arrival{{Type: "fake_thing", ID: "a", Root: "fake/old", From: "fake_thing.a"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("next's arrivals: got %+v, want %+v", got, want)
	}
	if len(c.byDir[next]) != 1 || len(c.run.report.Excluded) != 0 {
		t.Errorf("next imports %+v, excluded %+v, want a", c.byDir[next], c.run.report.Excluded)
	}
}

// A resource that goes to another root as another name of its type, such
// as an aws_alb that is an aws_lb now, is handed over too: the root it
// leaves forgets it as it was, the root it goes to imports it as it is,
// and the two are told to be one handover.
func TestPlanHandoversAcrossAnotherNameOfItsType(t *testing.T) {
	out := t.TempDir()
	m := newMemoryOutput(out)
	old, next := filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next")
	m.earlier(old, engine.Managed{Type: "aws_alb", ID: "arn:lb", Address: "aws_alb.web"}, thing("b", "fake_thing.b"))
	c := regenerating(out, map[string][]rootfile.Import{old: {thingImport("b")}, next: {{Type: "aws_lb", ID: "arn:lb", Name: "web"}}})

	if err := planHandovers(m.index(), []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	h := c.handovers
	if err := h.refusal(old); err != nil {
		t.Fatalf("old refused: %v", err)
	}
	departure := rootfile.Departure{Type: "aws_alb", ID: "arn:lb", Address: "aws_alb.web", Root: "fake/next"}
	if got, want := h.removalsOf(old), []rootfile.Removal{{From: "aws_alb.web", Resources: []rootfile.Departure{departure}}}; !reflect.DeepEqual(got, want) {
		t.Errorf("old's removals: got %+v, want %+v", got, want)
	}
	arrival := rootfile.Arrival{Type: "aws_lb", ID: "arn:lb", Root: "fake/old", From: "aws_alb.web", FromType: "aws_alb"}
	if got := h.arrivalsOf(next); !reflect.DeepEqual(got, []rootfile.Arrival{arrival}) {
		t.Errorf("next's arrivals: got %+v, want %+v", got, arrival)
	}
	arrival.To = "aws_lb.web"
	h.record(next, &engine.Result{Arrived: []rootfile.Arrival{arrival}}, nil)
	h.record(old, &engine.Result{Removed: h.removalsOf(old)}, nil)
	if got := h.incomplete(); len(got) != 0 || h.refuseUnreceived(old) != nil {
		t.Errorf("incomplete handovers %+v", got)
	}

	// Left out of a root the run doesn't generate, it stays there.
	c = regenerating(out, map[string][]rootfile.Import{next: {{Type: "aws_lb", ID: "arn:lb", Name: "web"}, thingImport("c")}})
	if err := planHandovers(m.index(), []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	if got := excludedIDs(c.run); !slices.Equal(got, []string{"arn:lb fake/old"}) {
		t.Errorf("excluded %v, want arn:lb, held by fake/old", got)
	}
}

// What a root the run doesn't generate has stays there, applied or not:
// a removed block would need it edited. It is left out of the root it
// would go to.
func TestPlanHandoversLeavesOutWhatARootNotInTheRunHas(t *testing.T) {
	out := t.TempDir()
	m := newMemoryOutput(out)
	old, next := filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next")
	m.earlier(old, thing("a", "fake_thing.a"), thing("b", "fake_thing.b"))
	c := regenerating(out, map[string][]rootfile.Import{next: {thingImport("a"), thingImport("c")}})

	if err := planHandovers(m.index(), []*listedCall{c}); err != nil {
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
	m := newMemoryOutput(out)
	old := filepath.Join(out, "fake", "old")
	alpha, beta := filepath.Join(out, "fake", "alpha"), filepath.Join(out, "fake", "beta")
	m.earlier(old, thing("a", "fake_thing.a"), thing("b", "fake_thing.b"))
	first := regenerating(out, map[string][]rootfile.Import{beta: {thingImport("b")}})
	second := regenerating(out, map[string][]rootfile.Import{alpha: {thingImport("a")}})

	if err := planHandovers(m.index(), []*listedCall{first, second}); err != nil {
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

// A removed block takes no instance keys and only a whole module call:
// of a root of which only some instances, or part of a module call, go,
// each resource that goes gets a removed block of its own, of a resource
// of the root alone that a moved block moves it to first, named so that
// it takes no address the root has.
func TestPlanHandoversHandsOverSomeInstancesAlone(t *testing.T) {
	for name, tt := range map[string]struct {
		had  []engine.Managed
		want rootfile.Removal
	}{
		"instances": {
			had:  []engine.Managed{thing("a", "fake_thing.group[0]"), thing("b", "fake_thing.group[1]"), thing("c", "fake_thing.departed_group_0")},
			want: rootfile.Removal{From: "fake_thing.departed_group_0_2", Instance: "fake_thing.group[0]"},
		},
		"module call": {
			had:  []engine.Managed{thing("a", `module.m["k"].fake_thing.x`), thing("b", `module.m["k"].fake_thing.y`)},
			want: rootfile.Removal{From: "fake_thing.departed_m_k_x", Instance: `module.m["k"].fake_thing.x`},
		},
	} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			m := newMemoryOutput(out)
			old, next := filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next")
			m.earlier(old, tt.had...)
			c := regenerating(out, map[string][]rootfile.Import{old: {thingImport("b"), thingImport("c")}, next: {thingImport("a")}})

			if err := planHandovers(m.index(), []*listedCall{c}); err != nil {
				t.Fatal(err)
			}
			if err := c.handovers.refusal(old); err != nil {
				t.Fatalf("old refused: %v", err)
			}
			tt.want.Resources = []rootfile.Departure{{Type: "fake_thing", ID: "a", Address: tt.want.Instance, Root: "fake/next"}}
			if got := c.handovers.removalsOf(old); !reflect.DeepEqual(got, []rootfile.Removal{tt.want}) {
				t.Errorf("old's removals: got %+v, want %+v", got, tt.want)
			}
			if got := c.handovers.arrivalsOf(next); len(got) != 1 || got[0].From != tt.want.Instance {
				t.Errorf("next's arrivals: %+v", got)
			}
		})
	}
}

// What removed blocks can't take leaves a root as it was, and what would
// go is left out of the roots it goes to: a resource whose address is
// only inferred, and part of a call that a module from the registry
// makes, which a moved block can't reach.
func TestPlanHandoversRefusesWhatARemovedBlockCantTake(t *testing.T) {
	for name, tt := range map[string]struct {
		had  []engine.Managed
		want string
	}{
		"inferred address": {
			had:  []engine.Managed{{Type: "fake_thing", ID: "a", Address: "fake_thing.a", Unverified: true}, thing("b", "fake_thing.b")},
			want: "only inferred",
		},
		"registry module call": {
			had:  []engine.Managed{thing("a", "module.eks.module.kms.fake_thing.x"), thing("b", "module.eks.fake_thing.y")},
			want: "only part of module.eks goes to fake/next: a removed block takes only the whole module call, and a moved block can't move module.eks.module.kms.fake_thing.x out of a call module.eks (terraform-aws-modules/eks/aws) makes",
		},
	} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			m := newMemoryOutput(out)
			old, next := filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next")
			m.earlier(old, tt.had...)
			c := regenerating(out, map[string][]rootfile.Import{old: {thingImport("b")}, next: {thingImport("a"), thingImport("c")}})
			m.root(old).checkpoint = &checkpoint{Fingerprint: "fp", Result: &engine.Result{Addresses: tt.had, Modules: []engine.ModuleCall{{Name: "eks", Source: "terraform-aws-modules/eks/aws"}}}}

			if err := planHandovers(m.index(), []*listedCall{c}); err != nil {
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
	m := newMemoryOutput(out)
	old, next := filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next")
	m.earlier(old, thing("a", "module.m.fake_thing.x[0]"), thing("b", "module.m.fake_thing.x[1]"), thing("c", "fake_thing.c"))
	c := regenerating(out, map[string][]rootfile.Import{old: {thingImport("c")}, next: {thingImport("a"), thingImport("b")}})

	if err := planHandovers(m.index(), []*listedCall{c}); err != nil {
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
	m := newMemoryOutput(out)
	old, next, other := filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next"), filepath.Join(out, "fake", "other")
	m.earlier(old, thing("a", "fake_thing.a"), thing("b", "fake_thing.b"))
	m.root(old).moves = []rootfile.Move{{From: "fake_thing.was", To: "fake_thing.a"}}
	m.earlier(other, thing("c", "fake_thing.c"), thing("d", "fake_thing.d"))
	c := regenerating(out, map[string][]rootfile.Import{old: {thingImport("b"), thingImport("c")}, next: {thingImport("a")}, other: {thingImport("d")}})

	if err := planHandovers(m.index(), []*listedCall{c}); err != nil {
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
	m := newMemoryOutput(out)
	old, next := filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next")
	m.earlier(old, thing("a", "fake_thing.a"))
	c := regenerating(out, map[string][]rootfile.Import{next: {thingImport("a")}})
	c.options.Regenerate = false
	if err := planHandovers(m.index(), []*listedCall{c, nil}); err != nil {
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
	moves := readFileIn(t, alpha, rootfile.MovesFileName)
	if !strings.Contains(moves, "removed {\n  from = fake_thing.alpha\n") || !strings.Contains(moves, "destroy = false") {
		t.Errorf("alpha's %s:\n%s", rootfile.MovesFileName, moves)
	}
	if readme := readFileIn(t, alpha, rootfile.ReadmeFileName); !strings.Contains(readme, "`fake/all`") {
		t.Errorf("alpha's README doesn't say where its resource goes:\n%s", readme)
	}
	for _, name := range []string{rootfile.GeneratedFileName, rootfile.ImportsFileName} {
		if _, err := os.Stat(filepath.Join(alpha, name)); err == nil {
			t.Errorf("alpha keeps %s", name)
		}
	}
	c, err := rootCheckpoints.read(out, alpha)
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
	options.engine = f
	run = importPath(t, options)
	if len(run.failures) != 0 || !slices.Equal(f.generated, []string{"all"}) {
		t.Errorf("generated %v, failures %v", f.generated, run.failures)
	}
	if _, err := os.Stat(filepath.Join(alpha, rootfile.MovesFileName)); err != nil {
		t.Errorf("alpha's %s: %v", rootfile.MovesFileName, err)
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

// A root that hands resources over is generated after the roots they go
// to, unless those hand resources over to it too.
func TestHandoversOrderRootsAfterWhereTheirResourcesGo(t *testing.T) {
	out := t.TempDir()
	m := newMemoryOutput(out)
	a, b, c := filepath.Join(out, "fake", "a"), filepath.Join(out, "fake", "b"), filepath.Join(out, "fake", "c")
	m.earlier(a, thing("x", "fake_thing.x"), thing("y", "fake_thing.y"))
	call := regenerating(out, map[string][]rootfile.Import{a: {thingImport("y")}, b: {thingImport("z")}, c: {thingImport("x")}})
	if err := planHandovers(m.index(), []*listedCall{call}); err != nil {
		t.Fatal(err)
	}
	if got, want := call.handovers.order([]string{a, b, c}), []string{b, c, a}; !slices.Equal(got, want) {
		t.Errorf("order %v, want %v", got, want)
	}
	var none *handovers
	if got := none.order([]string{a, b}); !slices.Equal(got, []string{a, b}) {
		t.Errorf("order without handovers %v", got)
	}
}

// halfHandedOver plans the handover of fake_thing.a from old to next, as
// TestPlanHandoversBetweenRootsOfTheRun does, and returns the call.
func halfHandedOver(t *testing.T) (c *listedCall, old, next string) {
	t.Helper()
	out := t.TempDir()
	m := newMemoryOutput(out)
	old, next = filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next")
	m.earlier(old, thing("a", "fake_thing.a"), thing("b", "fake_thing.b"))
	c = regenerating(out, map[string][]rootfile.Import{old: {thingImport("b")}, next: {thingImport("a")}})
	if err := planHandovers(m.index(), []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	return c, old, next
}

// A root that fails after the root it hands a resource over to imported
// it leaves the resource in both: the run warns, and says so in the
// report, with the root that failed.
func TestWarnIncompleteWhenTheRootALeavesFails(t *testing.T) {
	c, old, next := halfHandedOver(t)
	h := c.handovers
	h.record(next, &engine.Result{Arrived: []rootfile.Arrival{{Type: "fake_thing", ID: "a", Root: "fake/old", From: "fake_thing.a", To: "fake_thing.a"}}}, nil)
	h.record(old, &engine.Result{Removed: h.removalsOf(old)}, errors.New("plan failed"))

	warnIncomplete(c.run, []*listedCall{c})
	got := c.run.report.IncompleteHandovers
	if len(got) != 1 {
		t.Fatalf("incomplete handovers %+v, want one", got)
	}
	want := report.Move{Type: "fake_thing", ID: "a", FromRoot: "fake/old", From: "fake_thing.a", ToRoot: "fake/next", To: "fake_thing.a"}
	if got[0].Move != want || got[0].FailedRoot != "fake/old" || !strings.Contains(got[0].Warning, "generate fake/old again with --regenerate before applying either root") {
		t.Errorf("incomplete handover %+v", got[0])
	}
	if md := c.run.report.Markdown(); !strings.Contains(md, "## Handovers not finished") || !strings.Contains(md, "- `fake_thing.a` from `fake/old` to `fake/next`: ") {
		t.Errorf("report.md:\n%s", md)
	}
}

// A root that fails after the root that hands a resource over to it was
// generated with its removed block leaves the resource in neither, once
// that is applied: the run warns not to apply it.
func TestWarnIncompleteWhenTheRootAGoesToFails(t *testing.T) {
	c, old, next := halfHandedOver(t)
	h := c.handovers
	h.record(old, &engine.Result{Removed: h.removalsOf(old)}, nil)
	h.record(next, nil, errors.New("plan failed"))

	warnIncomplete(c.run, []*listedCall{c})
	got := c.run.report.IncompleteHandovers
	if len(got) != 1 || got[0].FailedRoot != "fake/next" || got[0].To != "" || !strings.Contains(got[0].Warning, "don't apply fake/old until fake/next") {
		t.Errorf("incomplete handovers %+v", got)
	}
}

// A handover both roots finished, or both failed, is complete: the
// resource is in one root.
func TestWarnIncompleteOnlyForHalfAHandover(t *testing.T) {
	for name, failed := range map[string]bool{"finished": false, "both failed": true} {
		t.Run(name, func(t *testing.T) {
			c, old, next := halfHandedOver(t)
			h := c.handovers
			var err error
			if failed {
				err = errors.New("plan failed")
			}
			h.record(old, &engine.Result{Removed: h.removalsOf(old)}, err)
			h.record(next, &engine.Result{Arrived: []rootfile.Arrival{{Type: "fake_thing", ID: "a", To: "fake_thing.a"}}}, err)
			warnIncomplete(c.run, []*listedCall{c})
			if got := c.run.report.IncompleteHandovers; len(got) != 0 {
				t.Errorf("incomplete handovers %+v", got)
			}
		})
	}
}

// A root whose resource goes to a root generated before it that didn't
// import it, as it rejected it, is left as it was.
func TestRefuseUnreceived(t *testing.T) {
	c, old, next := halfHandedOver(t)
	h := c.handovers
	if err := h.refuseUnreceived(old); err != nil {
		t.Fatalf("refused before next is generated: %v", err)
	}
	h.record(next, &engine.Result{}, nil)
	if err := h.refuseUnreceived(old); err == nil || !strings.Contains(err.Error(), "fake_thing.a goes to fake/next, which didn't import it") {
		t.Errorf("old's refusal: %v", err)
	}
}

// With --regenerate, a root whose resource goes to a root that fails is
// generated after it, and left as it was: its state keeps the resource,
// and nothing is half handed over.
func TestListCallRegenerateLeavesARootAsItWasWhenWhereItsResourceGoesFails(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	first := pathOptions(out, &fakeEngine{}, "zeta", "all")
	first.Layout = "{out}/{provider}/all/"
	importPath(t, first)
	all := filepath.Join(out, "fake", "all")

	f := &fakeEngine{fail: "zeta"}
	options := pathOptions(out, f, "zeta", "all")
	options.Regenerate = true
	run := importPath(t, options)

	if !slices.Equal(f.generated, []string{"zeta"}) {
		t.Errorf("generated %v, want zeta, and all left as it was", f.generated)
	}
	if got := directoryPaths(run); !slices.Equal(got, []string{"fake/all (failed)", "fake/zeta (failed)"}) {
		t.Errorf("reported directories %v", got)
	}
	for _, d := range run.report.Directories {
		if d.Path == "fake/all" && !strings.Contains(d.Error, "fake_thing.zeta goes to fake/zeta, which didn't import it") {
			t.Errorf("all's error: %s", d.Error)
		}
	}
	if _, err := os.Stat(filepath.Join(all, rootfile.GeneratedFileName)); err != nil {
		t.Errorf("all isn't left as it was: %v", err)
	}
	if _, err := os.Stat(filepath.Join(all, rootfile.MovesFileName)); err == nil {
		t.Errorf("all has a %s", rootfile.MovesFileName)
	}
	if len(run.report.Departed) != 0 || len(run.report.IncompleteHandovers) != 0 {
		t.Errorf("departed %+v, incomplete %+v", run.report.Departed, run.report.IncompleteHandovers)
	}
}

// With --regenerate, a root that fails after the root its resource goes
// to imported it is reported as leaving the resource in both.
func TestListCallRegenerateWarnsWhenTheRootAResourceLeavesFails(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	first := pathOptions(out, &fakeEngine{}, "zeta", "all")
	first.Layout = "{out}/{provider}/all/"
	importPath(t, first)

	f := &fakeEngine{fail: "all"}
	options := pathOptions(out, f, "zeta", "all")
	options.Regenerate = true
	run := importPath(t, options)

	if !slices.Equal(f.generated, []string{"zeta", "all"}) {
		t.Errorf("generated %v, want zeta, then all", f.generated)
	}
	got := run.report.IncompleteHandovers
	if len(got) != 1 || got[0].ID != "id-zeta" || got[0].FromRoot != "fake/all" || got[0].ToRoot != "fake/zeta" || got[0].FailedRoot != "fake/all" {
		t.Errorf("incomplete handovers %+v", got)
	}
}

// whyText is what the report says of a resource an import left out for
// why.
func whyText(why report.Why) string {
	var r report.Report
	r.Exclude("", "", "", why)
	return r.Excluded[0].Reason
}
