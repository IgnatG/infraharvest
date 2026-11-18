// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

func TestWithImports(t *testing.T) {
	t.Parallel()
	out := `{"@level":"info","type":"version"}` + "\n" + `{"@level":"info","type":"change_summary","changes":{"add":1,"change":0,"import":2,"remove":0,"operation":"plan"}}` + "\n"
	edited, err := withImports([]byte(out), 3)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := parseChangeSummary(edited)
	if err != nil {
		t.Fatal(err)
	}
	if want := (ChangeSummary{Add: 1, Import: 5}); summary == nil || *summary != want {
		t.Errorf("got %+v, want %+v", summary, want)
	}
	if !bytes.Contains(edited, []byte(`"type":"version"`)) {
		t.Errorf("another message was lost: %s", edited)
	}
	if same, _ := withImports([]byte(out), 0); string(same) != out {
		t.Errorf("nothing skipped changed the output: %s", same)
	}
}

func TestCachedPlans(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	state := filepath.Join(dir, LocalStateFileName)
	snap := &Snapshot{dir: dir, state: state, ids: map[string]string{"aws_s3_bucket.a": "a", "aws_s3_bucket.gone": "g"}}
	plain := &fakeState{imports: 1}
	raw := &fakeState{imports: 1, plan: importPlan("aws_s3_bucket.a"), planOpts: -1}
	raw.plan.ResourceChanges[0].Change.Importing = nil
	raw.plan.ResourceChanges = append(raw.plan.ResourceChanges, &tfjson.ResourceChange{Address: "aws_s3_bucket.new", Mode: tfjson.ManagedResourceMode, Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionNoop}, Importing: &tfjson.Importing{ID: "n"}}})
	plain.plan = raw.plan
	c := NewCached(plain, raw, time.Minute, func() ([]ImportBlock, error) {
		return []ImportBlock{{To: "aws_s3_bucket.a", ID: "a"}, {To: "aws_s3_bucket.new", ID: "n"}}, nil
	})

	// Without a snapshot, it plans as plain does.
	if _, err := c.PlanJSON(context.Background(), io.Discard, tfexec.Out("p")); err != nil {
		t.Fatal(err)
	}
	if plain.planOpts != 1 || raw.planOpts != -1 || c.CachedPlans() != 0 {
		t.Errorf("plain plan: plain %d, raw %d, cached %d", plain.planOpts, raw.planOpts, c.CachedPlans())
	}

	c.Use(snap)
	if !c.Active() {
		t.Fatal("not active")
	}
	var out bytes.Buffer
	if _, err := c.PlanJSON(context.Background(), &out, tfexec.Out("p")); err != nil {
		t.Fatal(err)
	}
	if raw.planOpts != 4 || c.CachedPlans() != 1 {
		t.Errorf("cached plan: %d options, %d cached", raw.planOpts, c.CachedPlans())
	}
	if want := []string{"rm aws_s3_bucket.gone"}; !slices.Equal(raw.commands, want) {
		t.Errorf("commands: got %v, want %v", raw.commands, want)
	}
	// The plan imported one resource and skipped one: two imports, as
	// plain plans have.
	if summary, err := parseChangeSummary(out.Bytes()); err != nil || summary == nil || summary.Import != 2 {
		t.Errorf("summary: %+v, %v", summary, err)
	}
	plan, err := c.ShowPlanFile(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	for _, rc := range plan.ResourceChanges {
		if rc.Change.Importing == nil {
			t.Errorf("%s is not importing", rc.Address)
		}
	}

	// A snapshot that can't follow the root is dropped, and the plan goes
	// on as plain.
	raw.failState = errors.New("boom")
	c.Use(snap)
	snap.ids["aws_s3_bucket.gone"] = "g"
	cachedBefore := c.CachedPlans()
	if _, err := c.PlanJSON(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	if c.Active() || c.CachedPlans() != cachedBefore {
		t.Errorf("the snapshot wasn't dropped: active %v, cached %d", c.Active(), c.CachedPlans())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the snapshot directory stays: %v", err)
	}
}

// Plans follow the provider configuration of the root: one a snapshot wasn't
// built for runs plain, the second plan of a configuration builds a snapshot
// for it, and one kept from before an edit that was undone is used again.
func TestCachedFollowsProviders(t *testing.T) {
	t.Parallel()
	newSnapshot := func() *Snapshot {
		dir := t.TempDir()
		return &Snapshot{dir: dir, state: filepath.Join(dir, LocalStateFileName), ids: map[string]string{"aws_s3_bucket.a": "a"}}
	}
	plain := &fakeState{imports: 1}
	raw := &fakeState{imports: 1, plan: importPlan("aws_s3_bucket.a")}
	c := NewCached(plain, raw, time.Minute, func() ([]ImportBlock, error) {
		return []ImportBlock{{To: "aws_s3_bucket.a", ID: "a"}}, nil
	})
	config, built, buildErr := "one", 0, error(nil)
	var snapshots []*Snapshot
	c.Providers(func() (string, error) { return config, nil }, func(context.Context) (*Snapshot, error) {
		built++
		if buildErr != nil {
			return nil, buildErr
		}
		s := newSnapshot()
		snapshots = append(snapshots, s)
		return s, nil
	})
	first := newSnapshot()
	c.Use(first)
	plan := func() {
		t.Helper()
		if _, err := c.PlanJSON(context.Background(), io.Discard, tfexec.Out("p")); err != nil {
			t.Fatal(err)
		}
	}
	step := func(want string, cached, builds int) {
		t.Helper()
		if c.CachedPlans() != cached || built != builds {
			t.Fatalf("%s: %d cached plans, %d builds; want %d and %d", want, c.CachedPlans(), built, cached, builds)
		}
	}
	plan()
	step("the configuration the snapshot was built for", 1, 0)

	config = "two"
	plan()
	step("the first plan of another configuration", 1, 0)
	plan()
	step("the second", 2, 1)
	plan()
	step("the third", 3, 1)

	// Back to the first, whose snapshot is kept: nothing is built.
	config = "one"
	plan()
	step("the first configuration again", 4, 1)
	config = "two"
	plan()
	step("the second configuration again", 5, 1)

	// A configuration planned once and undone costs one plain plan.
	config = "three"
	plan()
	config = "two"
	plan()
	step("an edit that was undone", 6, 1)

	// Where a snapshot can't be built, the plans run plain and no more
	// builds are tried for the configuration.
	config, buildErr = "four", errors.New("boom")
	plan()
	plan()
	plan()
	step("a snapshot that can't be built", 6, 2)
	if !c.Active() {
		t.Error("the snapshots were dropped")
	}

	// At most maxSnapshots are kept.
	buildErr = nil
	for _, name := range []string{"five", "six"} {
		config = name
		plan()
		plan()
	}
	if got := 1 + len(c.parked); got > maxSnapshots {
		t.Errorf("%d snapshots are kept, at most %d", got, maxSnapshots)
	}

	c.Release()
	for _, s := range append(snapshots, first) {
		if _, err := os.Stat(s.dir); !os.IsNotExist(err) {
			t.Errorf("a snapshot's directory stays: %v", err)
		}
	}
}

// A configuration the root's provider blocks can't be read for drops the
// snapshots and plans as plain does.
func TestCachedDropsSnapshotsWhenProvidersCantBeRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	snap := &Snapshot{dir: dir, state: filepath.Join(dir, LocalStateFileName), ids: map[string]string{}}
	plain, raw := &fakeState{}, &fakeState{}
	c := NewCached(plain, raw, time.Minute, func() ([]ImportBlock, error) { return nil, nil })
	c.Providers(func() (string, error) { return "", errors.New("unreadable") }, nil)
	c.Use(snap)
	if c.Active() {
		t.Error("a snapshot is used for a configuration that can't be read")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the snapshot directory stays: %v", err)
	}
}
