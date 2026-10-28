// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/report"
)

// memoryOutput is an outputSource held in memory: what an output holds of
// what earlier imports left, without writing a tree of checkpoints. It
// counts what the index asks of it.
type memoryOutput struct {
	out string
	at  map[string]*memoryRoot
	// reads counts the questions asked of the source, by method.
	reads map[string]int
}

// memoryRoot is what a memoryOutput has of a root, plain or merged.
type memoryRoot struct {
	// checkpoint is the root's checkpoint, unless it has none, or
	// unreadable says it can't be read.
	checkpoint *checkpoint
	unreadable error
	// configured says the root has .tf files, and imports where its
	// import blocks import.
	configured bool
	imports    []engine.Managed
	moves      []rootfile.Move
	removed    []rootfile.Removal
	// merge is the merge checkpoint of a merged root, mergeErr why it
	// can't be read.
	merge    *mergeCheckpoint
	mergeErr error
	// envs and locations are what the files of a root environments share
	// tell: its environments, and where each one's state is.
	envs      []string
	locations map[string][]string
}

func newMemoryOutput(out string) *memoryOutput {
	return &memoryOutput{out: out, at: map[string]*memoryRoot{}, reads: map[string]int{}}
}

// index returns an index of what m holds.
func (m *memoryOutput) index() *outputIndex { return newOutputIndex(m.out, m) }

// root returns what m has of the root in dir, which it makes.
func (m *memoryOutput) root(dir string) *memoryRoot {
	dir = filepath.Clean(dir)
	if m.at[dir] == nil {
		m.at[dir] = &memoryRoot{}
	}
	return m.at[dir]
}

// earlier records dir as a root an earlier import generated with
// addresses, as it is once applied: with configuration, but without its
// import blocks, and with a checkpoint of them.
func (m *memoryOutput) earlier(dir string, addresses ...engine.Managed) {
	result := &engine.Result{Addresses: addresses}
	for _, a := range addresses {
		result.Imported = append(result.Imported, rootfile.Import{Type: a.Type, ID: a.ID, Name: a.ID})
	}
	r := m.root(dir)
	r.configured, r.checkpoint = true, &checkpoint{Fingerprint: "fp", Result: result}
}

// mergedEarlier records, as an earlier import left them, the merged root
// fake/stack that merged the roots of dev and prod, which it set aside,
// with imports, the import IDs of each environment's resources in it by
// address. It returns the merged root's directory and dev's root.
func (m *memoryOutput) mergedEarlier(imports map[string]map[string]string) (stack, dev string) {
	stack = filepath.Join(m.out, "fake", "stack")
	dev = filepath.Join(m.out, "envs", "dev", "fake")
	r := m.root(stack)
	r.configured = true
	r.merge = &mergeCheckpoint{
		Members: []mergeCheckpointMember{{Env: "dev", Path: "envs/dev/fake"}, {Env: "prod", Path: "envs/prod/fake"}},
		Merge:   report.Merge{Path: "fake/stack", Merged: true},
		Imports: imports,
	}
	return stack, dev
}

func (m *memoryOutput) listing(kind string, has func(*memoryRoot) bool) ([]string, error) {
	m.reads[kind]++
	var dirs []string
	for dir, r := range m.at {
		if has(r) {
			dirs = append(dirs, dir)
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

func (m *memoryOutput) roots() ([]string, error) {
	return m.listing("roots", func(r *memoryRoot) bool { return r.checkpoint != nil || r.unreadable != nil })
}

func (m *memoryOutput) recorded(dir string) (bool, error) {
	m.reads["recorded"]++
	r := m.root(dir)
	return r.checkpoint != nil || r.unreadable != nil, nil
}

func (m *memoryOutput) checkpoint(dir string) (*checkpoint, error) {
	m.reads["checkpoint"]++
	r := m.root(dir)
	return r.checkpoint, r.unreadable
}

func (m *memoryOutput) configured(dir string) (bool, error) {
	m.reads["configured"]++
	return m.root(dir).configured, nil
}

func (m *memoryOutput) importBlocks(dir string) ([]engine.Managed, error) {
	m.reads["importBlocks"]++
	return m.root(dir).imports, nil
}

func (m *memoryOutput) moves(dir string) ([]rootfile.Move, error) {
	m.reads["moves"]++
	return m.root(dir).moves, nil
}

func (m *memoryOutput) removed(dir string) ([]rootfile.Removal, error) {
	m.reads["removed"]++
	return m.root(dir).removed, nil
}

func (m *memoryOutput) stacks() ([]string, error) {
	return m.listing("stacks", func(r *memoryRoot) bool { return r.merge != nil || r.mergeErr != nil })
}

func (m *memoryOutput) mergeCheckpoint(stack string) (*mergeCheckpoint, error) {
	m.reads["mergeCheckpoint"]++
	r := m.root(stack)
	return r.merge, r.mergeErr
}

func (m *memoryOutput) mergedRoots() ([]string, error) {
	return m.listing("mergedRoots", func(r *memoryRoot) bool { return r.configured && len(r.envs) > 0 })
}

func (m *memoryOutput) mergedEnvironments(dir string) ([]string, error) {
	return m.root(dir).envs, nil
}

func (m *memoryOutput) mergedStateLocations(dir, env string) ([]string, error) {
	return m.root(dir).locations[env], nil
}

// The index reads each thing once, as it was when first asked for, and
// only what it is asked for.
func TestOutputIndexReadsOnce(t *testing.T) {
	m := newMemoryOutput("out")
	a, b := filepath.Join("out", "fake", "a"), filepath.Join("out", "fake", "b")
	m.earlier(a, thing("x", "fake_thing.x"))
	m.earlier(b, thing("y", "fake_thing.y"))
	x := m.index()

	for range 3 {
		if held, err := x.heldElsewhere(map[string][]rootfile.Import{b: nil}); err != nil || len(held) != 1 {
			t.Fatalf("held %v, %v", held, err)
		}
		if previous, err := x.root(a).previous(); err != nil || len(previous) != 1 {
			t.Fatalf("previous %v, %v", previous, err)
		}
	}
	if got := m.reads; got["roots"] != 1 || got["stacks"] != 1 || got["checkpoint"] != 1 || got["configured"] != 1 || got["moves"] != 0 {
		t.Errorf("source read %v: a's checkpoint once, and nothing of b's, which is the run's own", got)
	}
	// What is written to the source afterwards isn't seen: the index is
	// what earlier imports left.
	m.root(a).checkpoint = nil
	if previous, err := x.root(a).previous(); err != nil || len(previous) != 1 {
		t.Errorf("previous after the source changed: %v, %v", previous, err)
	}
}

// A resource is held by the first root that imports it, as the checkpoints
// record; the roots being imported, a root whose import blocks are gone
// but with no configuration left either, and what a root left out don't
// count.
func TestOutputIndexHeldElsewhere(t *testing.T) {
	m := newMemoryOutput("out")
	global, regional, gone := filepath.Join("out", "google", "global"), filepath.Join("out", "google", "us"), filepath.Join("out", "google", "eu")
	network := rootfile.Import{Type: "google_compute_network", Name: "main", ID: "n"}
	subnet := rootfile.Import{Type: "google_compute_subnetwork", Name: "app", ID: "s"}
	eu := rootfile.Import{Type: "google_compute_subnetwork", Name: "eu", ID: "e"}
	m.root(global).checkpoint = &checkpoint{Result: &engine.Result{Imported: []rootfile.Import{network}}}
	m.root(regional).checkpoint = &checkpoint{Result: &engine.Result{Imported: []rootfile.Import{subnet}, Rejected: []engine.Rejection{{Address: "google_compute_subnetwork.bad", ID: "bad"}}}}
	m.root(gone).checkpoint = &checkpoint{Result: &engine.Result{Imported: []rootfile.Import{eu}}}
	for _, dir := range []string{global, regional} {
		m.root(dir).configured = true
	}
	m.root(regional).imports = []engine.Managed{{Type: subnet.Type, ID: subnet.ID, Address: "google_compute_subnetwork.app"}}

	held, err := m.index().heldElsewhere(map[string][]rootfile.Import{global: {network, subnet, eu}})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[engine.External]string{{Type: subnet.Type, ID: subnet.ID}: "google/us"}; !reflect.DeepEqual(held, want) {
		t.Errorf("held elsewhere: got %v, want %v", held, want)
	}
}

// With no checkpoints, as before an output's first import, nothing is
// held elsewhere.
func TestOutputIndexHeldElsewhereWithoutCheckpoints(t *testing.T) {
	held, err := newMemoryOutput("out").index().heldElsewhere(map[string][]rootfile.Import{filepath.Join("out", "aws", "123"): nil})
	if err != nil || len(held) != 0 {
		t.Errorf("got %v, %v; want nothing", held, err)
	}
}

// A root applied since, its import blocks deleted, still holds what its
// checkpoint records, and a root whose checkpoint can't be read is passed
// over.
func TestOutputIndexHeldElsewhereCountsAnAppliedRoot(t *testing.T) {
	m := newMemoryOutput("out")
	applied, broken, next := filepath.Join("out", "aws", "applied"), filepath.Join("out", "aws", "broken"), filepath.Join("out", "aws", "next")
	queue := rootfile.Import{Type: "aws_sqs_queue", Name: "orders", ID: "https://sqs/orders"}
	m.earlier(applied, engine.Managed{Type: queue.Type, ID: queue.ID, Address: "aws_sqs_queue.orders"})
	m.root(broken).configured, m.root(broken).unreadable = true, errors.New("corrupt")

	held, err := m.index().heldElsewhere(map[string][]rootfile.Import{next: {queue}})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[engine.External]string{{Type: queue.Type, ID: queue.ID}: "aws/applied"}; !reflect.DeepEqual(held, want) {
		t.Errorf("held: got %v, want %v", held, want)
	}
}

// What the merged roots environments share have is held by them, unless
// the run imports one of their roots, which adds to it.
func TestOutputIndexHeldElsewhereCountsMergedRoots(t *testing.T) {
	m := newMemoryOutput("out")
	stack := filepath.Join("out", "aws", "eu-west-2")
	r := m.root(stack)
	r.configured = true
	r.merge = &mergeCheckpoint{
		Merge:   report.Merge{Path: "aws/eu-west-2", Merged: true},
		Members: []mergeCheckpointMember{{Env: "dev", Path: "envs/dev/aws/111111111111/eu-west-2"}},
		Imports: map[string]map[string]string{"dev": {`aws_sqs_queue.this["orders"]`: "q1"}},
	}
	x := m.index()
	held, err := x.heldElsewhere(map[string][]rootfile.Import{filepath.Join("out", "aws", "333333333333", "eu-west-2"): nil})
	if err != nil {
		t.Fatal(err)
	}
	if got := held[engine.External{Type: "aws_sqs_queue", ID: "q1"}]; got != "aws/eu-west-2" {
		t.Errorf("held by %q", got)
	}
	held, err = x.heldElsewhere(map[string][]rootfile.Import{filepath.Join("out", "envs", "dev", "aws", "111111111111", "eu-west-2"): nil})
	if err != nil || len(held) != 0 {
		t.Errorf("held from its own run: %v %v", held, err)
	}

	// Not one with no configuration left, nor one that wasn't merged.
	r.configured = false
	x = m.index()
	if held, err := x.heldElsewhere(nil); err != nil || len(held) != 0 {
		t.Errorf("a merged root with no configuration: %v %v", held, err)
	}
	r.configured, r.merge.Merge.Merged = true, false
	if held, err := m.index().heldElsewhere(nil); err != nil || len(held) != 0 {
		t.Errorf("a root that wasn't merged: %v %v", held, err)
	}
}

// generatedResources counts the roots still being generated again, and
// fails on a root whose checkpoint can't be read.
func TestOutputIndexGeneratedResources(t *testing.T) {
	m := newMemoryOutput("out")
	a, again, bare := filepath.Join("out", "fake", "a"), filepath.Join("out", "fake", "again"), filepath.Join("out", "fake", "bare")
	m.earlier(a, thing("x", "fake_thing.x"))
	m.root(again).configured = true
	m.root(again).checkpoint = &checkpoint{Pending: []engine.Managed{thing("y", "fake_thing.y")}}
	m.earlier(bare, thing("z", "fake_thing.z"))
	m.root(bare).configured = false

	held, dirs, err := m.index().generatedResources()
	if err != nil {
		t.Fatal(err)
	}
	if want := map[engine.External]string{{Type: "fake_thing", ID: "x"}: a, {Type: "fake_thing", ID: "y"}: again}; !reflect.DeepEqual(held, want) {
		t.Errorf("held: got %v, want %v", held, want)
	}
	if !slices.Equal(dirs, []string{a, again}) {
		t.Errorf("roots %v, want those with configuration", dirs)
	}

	m.root(a).unreadable = errors.New("corrupt")
	if _, _, err := m.index().generatedResources(); err == nil || !strings.Contains(err.Error(), "fake/a: corrupt: with --managed-state") {
		t.Errorf("an unreadable checkpoint: %v", err)
	}
}

// The previous addresses come from the checkpoint, then the root's
// import blocks, then what the checkpoint says it imported; a root whose
// import didn't finish has none.
func TestOutputIndexPrevious(t *testing.T) {
	dir := filepath.Join("out", "aws", "555", "eu-west-2")
	fromImports := []engine.Managed{
		{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.a"},
		{Type: "aws_vpc", ID: "vpc-1", Address: "module.network.aws_vpc.this[0]"},
	}
	recorded := []engine.Managed{{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.private"}}
	older := &engine.Result{
		Imported: []rootfile.Import{{Type: "aws_vpc", Name: "main", ID: "vpc-1"}, {Type: "aws_subnet", Name: "a", ID: "subnet-1"}},
		Modules:  []engine.ModuleCall{{Name: "network", Resources: []string{"aws_vpc.main"}}},
	}
	inferred := []engine.Managed{
		{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.a", Unverified: true},
		{Type: "aws_vpc", ID: "vpc-1", Address: "module.network", Unverified: true},
	}
	pending := []engine.Managed{{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.pending"}}
	for name, tc := range map[string]struct {
		checkpoint *checkpoint
		imports    []engine.Managed
		want       []engine.Managed
	}{
		"without a checkpoint":    {nil, fromImports, nil},
		"recorded":                {&checkpoint{Result: &engine.Result{Imported: older.Imported, Addresses: recorded}}, fromImports, recorded},
		"from imports.tf":         {&checkpoint{Result: older}, fromImports, fromImports},
		"inferred":                {&checkpoint{Result: older}, nil, inferred},
		"pending":                 {&checkpoint{Pending: pending}, fromImports, pending},
		"an import didn't finish": {&checkpoint{}, fromImports, nil},
	} {
		t.Run(name, func(t *testing.T) {
			m := newMemoryOutput("out")
			m.root(dir).checkpoint, m.root(dir).imports = tc.checkpoint, tc.imports
			got, err := m.index().root(dir).previous()
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, %v, want %+v", got, err, tc.want)
			}
		})
	}
	m := newMemoryOutput("out")
	m.root(dir).unreadable = errors.New("corrupt")
	if got, err := m.index().root(dir).previous(); err == nil {
		t.Errorf("an unreadable checkpoint: %v", got)
	}
}

// The moved blocks a root may not have applied yet are those of its
// moves.tf, or, while a run that stopped generates it again, those its
// checkpoint kept.
func TestOutputIndexPendingMoves(t *testing.T) {
	m := newMemoryOutput("out")
	dir := filepath.Join("out", "fake", "a")
	file, kept := []rootfile.Move{{From: "fake_thing.was", To: "fake_thing.a"}}, []rootfile.Move{{From: "fake_thing.b", To: "fake_thing.c"}}
	m.root(dir).moves = file
	if got, err := m.index().root(dir).pendingMoves(); err != nil || got != nil {
		t.Errorf("without a checkpoint: %v, %v", got, err)
	}
	m.root(dir).checkpoint = &checkpoint{Result: &engine.Result{}, PendingMoves: kept}
	if got, err := m.index().root(dir).pendingMoves(); err != nil || !reflect.DeepEqual(got, file) {
		t.Errorf("generated: %v, %v, want moves.tf's", got, err)
	}
	m.root(dir).checkpoint = &checkpoint{PendingMoves: kept}
	if got, err := m.index().root(dir).pendingMoves(); err != nil || !reflect.DeepEqual(got, kept) {
		t.Errorf("generating again: %v, %v, want the checkpoint's", got, err)
	}
}

// The states of the environments of a merged root are that root's; with
// unrecorded, only those of the roots no merge checkpoint records count.
func TestMergedOwnStates(t *testing.T) {
	m := newMemoryOutput("out")
	stack := filepath.Join("out", "aws", "eu-west-2")
	r := m.root(stack)
	r.configured, r.envs = true, []string{"dev", "prod"}
	dev, prod := stateLocation("/state/dev"), stateLocation("/state/prod")
	r.locations = map[string][]string{"dev": {dev}, "prod": {prod, "s3://state/prod"}}
	want := map[string]mergedState{
		dev: {stack, "dev"}, prod: {stack, "prod"}, "s3://state/prod": {stack, "prod"},
	}
	if own, err := mergedOwnStates(m.index(), true); err != nil || !reflect.DeepEqual(own, want) {
		t.Errorf("got %v, %v, want %v", own, err, want)
	}
	r.merge = &mergeCheckpoint{Merge: report.Merge{Merged: true}}
	if own, err := mergedOwnStates(m.index(), true); err != nil || len(own) != 0 {
		t.Errorf("recorded: %v %v", own, err)
	}
	if own, err := mergedOwnStates(m.index(), false); err != nil || !reflect.DeepEqual(own, want) {
		t.Errorf("every merged root: %v %v", own, err)
	}
	r.merge, r.mergeErr = nil, errors.New("corrupt")
	if own, err := mergedOwnStates(m.index(), true); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("a corrupt checkpoint: %v %v", own, err)
	}
}

// The disk adapter reads real trees of checkpoints: those of roots, and
// the merged roots', with what the roots' directories hold.
func TestDiskIndexReadsCheckpointTrees(t *testing.T) {
	out := t.TempDir()
	applied, unfinished, again := filepath.Join(out, "aws", "applied"), filepath.Join(out, "aws", "unfinished"), filepath.Join(out, "envs", "dev", "aws")
	stack := filepath.Join(out, "aws", "eu-west-2")
	earlierRoot(t, out, applied, thing("a", "fake_thing.a"))
	if err := os.WriteFile(filepath.Join(applied, rootfile.MovesFileName), []byte("moved {\n  from = fake_thing.was\n  to   = fake_thing.a\n}\nremoved {\n  from = fake_thing.gone\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rootCheckpoints.write(out, unfinished, checkpoint{Fingerprint: "fp"}); err != nil {
		t.Fatal(err)
	}
	writeRoot(t, again, rootfile.ImportsFileName)
	if err := os.WriteFile(filepath.Join(again, rootfile.ImportsFileName), []byte(importsOfTwo), 0o644); err != nil {
		t.Fatal(err)
	}
	older := &engine.Result{Imported: []rootfile.Import{{Type: "aws_vpc", Name: "main", ID: "vpc-1"}, {Type: "aws_subnet", Name: "a", ID: "subnet-1"}}}
	if err := saveCheckpoint(out, again, "fp", older); err != nil {
		t.Fatal(err)
	}
	writeRootFiles(t, stack, map[string]string{"providers.tf": "# providers\n"})
	merged := mergeCheckpoint{
		Merge:   report.Merge{Path: "aws/eu-west-2", Merged: true},
		Members: []mergeCheckpointMember{{Env: "dev", Path: "envs/dev/aws"}},
		Imports: map[string]map[string]string{"dev": {"aws_sqs_queue.orders": "q1"}},
	}
	if err := mergeCheckpoints.write(out, stack, merged); err != nil {
		t.Fatal(err)
	}
	x := newDiskIndex(out)

	roots, err := x.recordedRoots()
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, r := range roots {
		dirs = append(dirs, r.dir)
	}
	if want := []string{applied, unfinished, again}; !slices.Equal(dirs, want) {
		t.Errorf("roots %v, want %v", dirs, want)
	}
	if stacks, err := x.mergedStacks(); err != nil || len(stacks) != 1 || stacks[0].dir != stack {
		t.Errorf("stacks %v, %v", stacks, err)
	}
	if recorded, err := x.root(applied).recorded(); err != nil || !recorded {
		t.Errorf("applied recorded: %v, %v", recorded, err)
	}
	if recorded, err := x.root(filepath.Join(out, "aws", "new")).recorded(); err != nil || recorded {
		t.Errorf("a new root recorded: %v, %v", recorded, err)
	}
	if has, err := x.root(unfinished).configured(); err != nil || has {
		t.Errorf("a root without files has configuration: %v, %v", has, err)
	}
	if moved, err := x.root(applied).pendingMoves(); err != nil || !reflect.DeepEqual(moved, []rootfile.Move{{From: "fake_thing.was", To: "fake_thing.a"}}) {
		t.Errorf("pending moves %v, %v", moved, err)
	}
	if removed, err := x.root(applied).removedTF(); err != nil || len(removed) != 1 || removed[0].From != "fake_thing.gone" {
		t.Errorf("removed %v, %v", removed, err)
	}
	if previous, err := x.root(applied).previous(); err != nil || len(previous) != 1 || previous[0].Address != "fake_thing.a" {
		t.Errorf("applied previous %v, %v", previous, err)
	}
	fromImports := []engine.Managed{
		{Type: "aws_subnet", ID: "subnet-1", Address: "aws_subnet.a"},
		{Type: "aws_vpc", ID: "vpc-1", Address: "module.network.aws_vpc.this[0]"},
	}
	if previous, err := x.root(again).previous(); err != nil || !reflect.DeepEqual(previous, fromImports) {
		t.Errorf("previous from the import blocks %+v, %v", previous, err)
	}
	if previous, err := x.root(unfinished).previous(); err != nil || previous != nil {
		t.Errorf("a root whose import didn't finish: %v, %v", previous, err)
	}
	held, err := x.heldElsewhere(map[string][]rootfile.Import{filepath.Join(out, "aws", "next"): nil})
	if err != nil {
		t.Fatal(err)
	}
	if got := held[engine.External{Type: "aws_sqs_queue", ID: "q1"}]; got != "aws/eu-west-2" {
		t.Errorf("merged root's holding: %q", got)
	}
	if got := held[engine.External{Type: "fake_thing", ID: "a"}]; got != "aws/applied" {
		t.Errorf("applied root's holding: %q", got)
	}

	// A checkpoint that can't be read is an error naming its file, and
	// a root with none, nothing.
	path := rootCheckpoints.path(out, applied)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c, err := newDiskIndex(out).root(applied).checkpoint(); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("a corrupt checkpoint: %v, %v", c, err)
	}
	if c, err := newDiskIndex(out).root(filepath.Join(out, "aws", "new")).checkpoint(); err != nil || c != nil {
		t.Errorf("no checkpoint: %v, %v", c, err)
	}
	if roots, err := newDiskIndex(filepath.Join(out, "missing")).recordedRoots(); err != nil || len(roots) != 0 {
		t.Errorf("a missing output: %v, %v", roots, err)
	}
}
