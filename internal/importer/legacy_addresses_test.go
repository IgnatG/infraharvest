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
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/managed"
)

// legacy records dir as a root an older release generated and applied: a
// checkpoint without addresses, of things a and b, with configuration
// declaring configured.
func (m *memoryOutput) legacy(dir string, configured ...string) {
	r := m.root(dir)
	r.configured = true
	r.checkpoint = &checkpoint{Fingerprint: "fp", Result: &engine.Result{Imported: []rootfile.Import{
		{Type: "fake_thing", ID: "a", Name: "a"}, {Type: "fake_thing", ID: "b", Name: "b"},
	}}}
	r.seen = observedRoot{Resources: map[string]bool{}}
	for _, a := range configured {
		r.seen.Resources[a] = true
	}
}

// Addresses a checkpoint without any are inferred for are verified
// against the root's configuration: the handover goes ahead, and the
// checkpoint is rewritten with them.
func TestPlanHandoversVerifiesAddressesOfALegacyCheckpoint(t *testing.T) {
	out := t.TempDir()
	m := newMemoryOutput(out)
	old, next := filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next")
	m.legacy(old, "fake_thing.a", "fake_thing.b")
	c := regenerating(out, map[string][]rootfile.Import{old: {thingImport("b")}, next: {thingImport("a")}})

	if err := planHandovers(m.index(), []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	if err := c.handovers.refusal(old); err != nil {
		t.Fatalf("old refused: %v", err)
	}
	want := []rootfile.Removal{{From: "fake_thing.a", Resources: []rootfile.Departure{{Type: "fake_thing", ID: "a", Address: "fake_thing.a", Root: "fake/next"}}}}
	if got := c.handovers.removalsOf(old); !reflect.DeepEqual(got, want) {
		t.Errorf("old's removals: got %+v, want %+v", got, want)
	}
	if got := c.handovers.arrivalsOf(next); len(got) != 1 || got[0].From != "fake_thing.a" {
		t.Errorf("next's arrivals: %+v", got)
	}
	saved := m.root(old).saved
	wantAddresses := []engine.Managed{thing("a", "fake_thing.a"), thing("b", "fake_thing.b")}
	if saved == nil || saved.Fingerprint != "fp" || !reflect.DeepEqual(saved.Result.Addresses, wantAddresses) {
		t.Fatalf("saved checkpoint: %+v, want addresses %+v", saved, wantAddresses)
	}
	// The checkpoint the index read is as it was.
	if len(m.root(old).checkpoint.Result.Addresses) != 0 {
		t.Errorf("the checkpoint it read changed: %+v", m.root(old).checkpoint.Result.Addresses)
	}
}

// An address the configuration doesn't declare stays only inferred: the
// handover is refused, and the checkpoint isn't rewritten.
func TestPlanHandoversRefusesAnAddressOfALegacyCheckpointNotVerified(t *testing.T) {
	out := t.TempDir()
	m := newMemoryOutput(out)
	old, next := filepath.Join(out, "fake", "old"), filepath.Join(out, "fake", "next")
	m.legacy(old, "fake_thing.renamed", "fake_thing.b")
	c := regenerating(out, map[string][]rootfile.Import{old: {thingImport("b")}, next: {thingImport("a")}})

	if err := planHandovers(m.index(), []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	if err := c.handovers.refusal(old); err == nil || !strings.Contains(err.Error(), "only inferred") {
		t.Errorf("old's refusal: %v", err)
	}
	if len(c.handovers.removalsOf(old)) != 0 {
		t.Errorf("removals: %+v", c.handovers.removalsOf(old))
	}
	if m.root(old).saved != nil {
		t.Errorf("rewrote the checkpoint: %+v", m.root(old).saved)
	}
}

// A local state says where a resource is, a module call's too, and is
// what counts over the configuration.
func TestVerifyAddressesFromLocalState(t *testing.T) {
	state := managed.Resources{}
	if err := state.Parse([]byte(`{"version":4,"resources":[
		{"mode":"managed","module":"module.logs","type":"fake_thing","name":"this","instances":[{"index_key":0,"attributes":{"id":"a"}}]},
		{"mode":"managed","type":"fake_thing","name":"renamed","instances":[{"attributes":{"id":"b"}}]}]}`), "terraform.tfstate"); err != nil {
		t.Fatal(err)
	}
	inferred := []engine.Managed{
		{Type: "fake_thing", ID: "a", Address: "module.logs", Unverified: true},
		{Type: "fake_thing", ID: "b", Address: "fake_thing.b", Unverified: true},
		{Type: "fake_thing", ID: "c", Address: "fake_thing.c", Unverified: true},
	}
	got := verifyAddresses(inferred, observedRoot{Resources: map[string]bool{"fake_thing.c": true}, State: state})
	want := []engine.Managed{
		thing("a", "module.logs.fake_thing.this[0]"),
		thing("b", "fake_thing.renamed"),
		{Type: "fake_thing", ID: "c", Address: "fake_thing.c", Unverified: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// Without a state, a module call can't be told.
	if got := verifyAddresses(inferred[:1], observedRoot{Resources: map[string]bool{"module.logs": true}}); !got[0].Unverified {
		t.Errorf("a module call verified by the configuration: %+v", got)
	}
}

// observeRoot reads the configuration and local state of the root, and
// leaves a root with a backend's local state out.
func TestObserveRoot(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("generated.tf", "resource \"fake_thing\" \"a\" {}\nmodule \"logs\" {\n  source = \"x\"\n}\n")
	obs, err := observeRoot(dir)
	if err != nil || !obs.Resources["fake_thing.a"] || len(obs.Resources) != 1 || obs.State != nil {
		t.Fatalf("without state: %+v, %v", obs, err)
	}
	write("terraform.tfstate", `{"version":4,"resources":[{"mode":"managed","type":"fake_thing","name":"a","instances":[{"attributes":{"id":"a"}}]}]}`)
	if obs, err = observeRoot(dir); err != nil || obs.State == nil {
		t.Fatalf("with state: %+v, %v", obs, err)
	}
	write(BackendFileName, "terraform {\n  backend \"local\" {}\n}\n")
	if obs, err = observeRoot(dir); err != nil || obs.State != nil {
		t.Fatalf("with a backend: %+v, %v", obs, err)
	}
}

// On disk, the verified addresses are in the root's checkpoint, which the
// next run reads as recorded.
func TestPreviousKeepsVerifiedAddressesInTheCheckpoint(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "fake", "old")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generated.tf"), []byte("resource \"fake_thing\" \"a\" {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := &engine.Result{Imported: []rootfile.Import{{Type: "fake_thing", ID: "a", Name: "a"}}}
	if err := saveCheckpoint(out, dir, "fp", result); err != nil {
		t.Fatal(err)
	}
	got, err := newDiskIndex(out).root(dir).previous()
	if err != nil || !reflect.DeepEqual(got, []engine.Managed{thing("a", "fake_thing.a")}) {
		t.Fatalf("previous: %+v, %v", got, err)
	}
	c, err := rootCheckpoints.read(out, dir)
	if err != nil || c == nil || c.Fingerprint != "fp" || !reflect.DeepEqual(c.Result.Addresses, got) {
		t.Fatalf("checkpoint: %+v, %v", c, err)
	}
	if got, err := newDiskIndex(out).root(dir).previous(); err != nil || got[0].Unverified {
		t.Errorf("next run: %+v, %v", got, err)
	}
}
