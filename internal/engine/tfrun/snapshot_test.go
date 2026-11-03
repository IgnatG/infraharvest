// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

// fakeState is a StateTerraform that records its commands. Its plans
// write a change summary with imports imports, and show plan.
type fakeState struct {
	Terraform
	imports int
	plan    *tfjson.Plan
	planErr error

	inits, applies int
	planOpts       int
	commands       []string
	failApply      error
	failState      error
}

func (f *fakeState) Init(context.Context, ...tfexec.InitOption) error {
	f.inits++
	return nil
}

func (f *fakeState) PlanJSON(_ context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	f.planOpts = len(opts)
	fmt.Fprintf(w, `{"@level":"info","@message":"Plan","type":"change_summary","changes":{"add":0,"change":1,"import":%d,"remove":0,"operation":"plan"}}`+"\n", f.imports)
	return true, f.planErr
}

func (f *fakeState) ShowPlanFile(context.Context, string, ...tfexec.ShowOption) (*tfjson.Plan, error) {
	return f.plan, nil
}

func (f *fakeState) SetStdout(io.Writer) {}

func (f *fakeState) Apply(context.Context, ...tfexec.ApplyOption) error {
	f.applies++
	return f.failApply
}

func (f *fakeState) StateMv(_ context.Context, from, to string, _ ...tfexec.StateMvCmdOption) error {
	f.commands = append(f.commands, "mv "+from+" "+to)
	return f.failState
}

func (f *fakeState) StateRm(_ context.Context, address string, _ ...tfexec.StateRmCmdOption) error {
	f.commands = append(f.commands, "rm "+address)
	return f.failState
}

func importPlan(addresses ...string) *tfjson.Plan {
	p := &tfjson.Plan{}
	for _, a := range addresses {
		p.ResourceChanges = append(p.ResourceChanges, &tfjson.ResourceChange{
			Address: a, Mode: tfjson.ManagedResourceMode,
			Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionNoop}, Importing: &tfjson.Importing{ID: "x"}},
		})
	}
	return p
}

func TestCheckImportOnly(t *testing.T) {
	t.Parallel()
	update := importPlan("aws_s3_bucket.a")
	update.ResourceChanges[0].Change.Actions = tfjson.Actions{tfjson.ActionUpdate}
	create := importPlan("aws_s3_bucket.a")
	create.ResourceChanges[0].Change.Actions = tfjson.Actions{tfjson.ActionCreate}
	notImporting := importPlan("aws_s3_bucket.a")
	notImporting.ResourceChanges[0].Change.Importing = nil
	invokes := importPlan("aws_s3_bucket.a")
	invokes.ActionInvocations = []*tfjson.ActionInvocation{{}}
	deferred := importPlan("aws_s3_bucket.a")
	deferred.DeferredChanges = []*tfjson.DeferredResourceChange{{}}
	dataWrites := importPlan("aws_s3_bucket.a")
	dataWrites.ResourceChanges = append(dataWrites.ResourceChanges, &tfjson.ResourceChange{
		Address: "data.aws_caller_identity.me", Mode: tfjson.DataResourceMode,
		Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionCreate}},
	})
	dataReads := importPlan("aws_s3_bucket.a")
	dataReads.ResourceChanges = append(dataReads.ResourceChanges, &tfjson.ResourceChange{
		Address: "data.aws_caller_identity.me", Mode: tfjson.DataResourceMode,
		Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionRead}},
	})

	for name, tc := range map[string]struct {
		plan *tfjson.Plan
		ok   bool
	}{
		"imports only":       {importPlan("aws_s3_bucket.a", "aws_s3_bucket.b"), true},
		"reads data sources": {dataReads, true},
		"no plan":            {nil, false},
		"an update":          {update, false},
		"a create":           {create, false},
		"not an import":      {notImporting, false},
		"an action":          {invokes, false},
		"a deferred change":  {deferred, false},
		"a data write":       {dataWrites, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := CheckImportOnly(tc.plan); (err == nil) != tc.ok {
				t.Errorf("got %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestSnapshotOverride(t *testing.T) {
	t.Parallel()
	got := string(SnapshotOverride([]string{"aws_s3_bucket.a", "aws_iam_role.b", "bad"}))
	for _, want := range []string{`resource "aws_s3_bucket" "a" {`, `resource "aws_iam_role" "b" {`, "ignore_changes = all"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "bad") {
		t.Errorf("a non-address was written:\n%s", got)
	}
}

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

func TestResourceType(t *testing.T) {
	t.Parallel()
	for address, want := range map[string]string{
		"aws_s3_bucket.main":                        "aws_s3_bucket",
		`module.a["x.y"].aws_s3_bucket.main[0]`:     "aws_s3_bucket",
		"module.a.module.b.aws_iam_role.r":          "aws_iam_role",
		`aws_s3_bucket_versioning.v["k"]`:           "aws_s3_bucket_versioning",
		"unexpected":                                "unexpected",
		"module.m.aws_s3_bucket_versioning.main[1]": "aws_s3_bucket_versioning",
	} {
		if got := resourceType(address); got != want {
			t.Errorf("%s: got %q, want %q", address, got, want)
		}
	}
}

func snapshotOf(ids map[string]string) *Snapshot {
	return &Snapshot{state: "s.tfstate", ids: ids}
}

func TestReconcile(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		ids      map[string]string
		blocks   []ImportBlock
		commands []string
		kept     map[string]string
	}{
		"unchanged": {
			ids:    map[string]string{"aws_s3_bucket.a": "a"},
			blocks: []ImportBlock{{To: "aws_s3_bucket.a", ID: "a"}, {To: "aws_s3_bucket.new", ID: "n"}},
			kept:   map[string]string{"aws_s3_bucket.a": "a"},
		},
		"a resource moved into a module is moved by ID": {
			ids:      map[string]string{"aws_s3_bucket.a": "a"},
			blocks:   []ImportBlock{{To: "module.m.aws_s3_bucket.main", ID: "a"}},
			commands: []string{"mv aws_s3_bucket.a module.m.aws_s3_bucket.main"},
			kept:     map[string]string{"module.m.aws_s3_bucket.main": "a"},
		},
		"the resource type must match": {
			ids:      map[string]string{"aws_s3_bucket.a": "same"},
			blocks:   []ImportBlock{{To: "module.m.aws_s3_bucket_versioning.main", ID: "same"}},
			commands: []string{"rm aws_s3_bucket.a"},
			kept:     map[string]string{},
		},
		"a resource with no import block is removed": {
			ids:      map[string]string{"aws_s3_bucket.a": "a", "aws_s3_bucket.b": "b"},
			blocks:   []ImportBlock{{To: "aws_s3_bucket.b", ID: "b"}},
			commands: []string{"rm aws_s3_bucket.a"},
			kept:     map[string]string{"aws_s3_bucket.b": "b"},
		},
		"another ID at the address is removed": {
			ids:      map[string]string{"aws_s3_bucket.a": "a"},
			blocks:   []ImportBlock{{To: "aws_s3_bucket.a", ID: "other"}},
			commands: []string{"rm aws_s3_bucket.a"},
			kept:     map[string]string{},
		},
		"two resources of an ID are not both moved to one address": {
			ids:      map[string]string{"aws_s3_bucket.a": "same", "aws_s3_bucket.b": "same"},
			blocks:   []ImportBlock{{To: "module.m.aws_s3_bucket.main", ID: "same"}},
			commands: []string{"mv aws_s3_bucket.a module.m.aws_s3_bucket.main", "rm aws_s3_bucket.b"},
			kept:     map[string]string{"module.m.aws_s3_bucket.main": "same"},
		},
		"an ID that isn't a literal is never matched": {
			ids:      map[string]string{"aws_s3_bucket.a": ""},
			blocks:   []ImportBlock{{To: "module.m.aws_s3_bucket.main", ID: ""}},
			commands: []string{"rm aws_s3_bucket.a"},
			kept:     map[string]string{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tf := &fakeState{}
			s := snapshotOf(tc.ids)
			kept, err := s.reconcile(context.Background(), tf, tc.blocks, 0)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(tf.commands, tc.commands) {
				t.Errorf("commands: got %v, want %v", tf.commands, tc.commands)
			}
			if fmt.Sprint(kept) != fmt.Sprint(tc.kept) {
				t.Errorf("kept: got %v, want %v", kept, tc.kept)
			}
		})
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

func TestBuildSnapshot(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // snapshots go to the system's temporary directory
	build := func(plan *tfjson.Plan, imports int) (*fakeState, *Snapshot, string, error) {
		dir := t.TempDir()
		tf := &fakeState{imports: imports, plan: plan}
		s, err := BuildSnapshot(context.Background(), SnapshotBuild{
			Plan: tf, Apply: tf, Dir: dir,
			Resources: []string{"aws_s3_bucket.a"},
			Imports:   []ImportBlock{{To: "aws_s3_bucket.a", ID: "a"}},
		})
		return tf, s, dir, err
	}

	tf, s, dir, err := build(importPlan("aws_s3_bucket.a"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Remove()
	if tf.inits != 1 || tf.applies != 1 || s.Len() != 1 {
		t.Errorf("inits %d, applies %d, entries %d", tf.inits, tf.applies, s.Len())
	}
	override, err := os.ReadFile(filepath.Join(dir, SnapshotOverrideFile))
	if err != nil || !strings.Contains(string(override), "ignore_changes = all") {
		t.Errorf("override: %q, %v", override, err)
	}
	if info, err := os.Stat(filepath.Dir(s.Path())); err != nil || info.Mode().Perm() != 0o700 && runtime.GOOS != "windows" {
		t.Errorf("the snapshot directory is not private: %v, %v", info, err)
	}
	if !strings.Contains(filepath.Base(filepath.Dir(s.Path())), SnapshotPattern) {
		t.Errorf("the snapshot is in %s", s.Path())
	}
	s.Remove()
	if _, err := os.Stat(filepath.Dir(s.Path())); !os.IsNotExist(err) {
		t.Errorf("Remove leaves the directory: %v", err)
	}

	// A plan that does more than import is never applied.
	update := importPlan("aws_s3_bucket.a")
	update.ResourceChanges[0].Change.Actions = tfjson.Actions{tfjson.ActionUpdate}
	tf, _, _, err = build(update, 1)
	if err == nil || tf.applies != 0 {
		t.Errorf("an update plan: error %v, applies %d", err, tf.applies)
	}
	// Neither is one that doesn't import every import block.
	tf, _, _, err = build(importPlan(), 0)
	if err == nil || tf.applies != 0 {
		t.Errorf("a plan without the imports: error %v, applies %d", err, tf.applies)
	}
	// A failed apply leaves nothing behind.
	failing := &fakeState{imports: 1, plan: importPlan("aws_s3_bucket.a"), failApply: errors.New("boom")}
	if _, err := BuildSnapshot(context.Background(), SnapshotBuild{Plan: failing, Apply: failing, Dir: t.TempDir(), Imports: []ImportBlock{{To: "aws_s3_bucket.a"}}}); err == nil {
		t.Error("a failed apply built a snapshot")
	}
	left, _ := filepath.Glob(filepath.Join(os.TempDir(), SnapshotPattern+"*"))
	if len(left) != 0 {
		t.Errorf("snapshots left behind: %v", left)
	}
}

func TestRemoveStaleSnapshots(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	old, young, other := filepath.Join(tmp, SnapshotPattern+"old"), filepath.Join(tmp, SnapshotPattern+"young"), filepath.Join(tmp, "other-old")
	for _, d := range []string{old, young, other} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	for _, d := range []string{old, other} {
		if err := os.Chtimes(d, past, past); err != nil {
			t.Fatal(err)
		}
	}
	removeStaleSnapshots(tmp, time.Now(), staleSnapshotAge)
	for dir, want := range map[string]bool{old: false, young: true, other: true} {
		if _, err := os.Stat(dir); (err == nil) != want {
			t.Errorf("%s exists: %v, want %v", filepath.Base(dir), err == nil, want)
		}
	}
}
