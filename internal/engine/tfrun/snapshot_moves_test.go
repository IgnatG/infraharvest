// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tfjson "github.com/hashicorp/terraform-json"
)

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
			kept, err := s.reconcile(context.Background(), tf, tc.blocks, 0, nil)
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

// movePlan is the plan of moves, by the address each moves to.
func movePlan(moves map[string]string) *tfjson.Plan {
	p := &tfjson.Plan{}
	for _, to := range slices.Sorted(maps.Keys(moves)) {
		p.ResourceChanges = append(p.ResourceChanges, &tfjson.ResourceChange{
			Address: to, PreviousAddress: moves[to], Mode: tfjson.ManagedResourceMode,
			Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionNoop}},
		})
	}
	return p
}

func TestCheckMoveOnly(t *testing.T) {
	t.Parallel()
	moves := map[string]string{`module.m.aws_s3_bucket.main["a"]`: "aws_s3_bucket.a", `module.m.aws_s3_bucket.main["b"]`: "aws_s3_bucket.b"}
	managed := func(address, previous string, action tfjson.Action) *tfjson.ResourceChange {
		return &tfjson.ResourceChange{Address: address, PreviousAddress: previous, Mode: tfjson.ManagedResourceMode, Change: &tfjson.Change{Actions: tfjson.Actions{action}}}
	}
	data := func(address string, action tfjson.Action) *tfjson.ResourceChange {
		return &tfjson.ResourceChange{Address: address, Mode: tfjson.DataResourceMode, Change: &tfjson.Change{Actions: tfjson.Actions{action}}}
	}
	for name, tc := range map[string]struct {
		edit func(p *tfjson.Plan)
		want string
	}{
		"only the moves": {edit: func(*tfjson.Plan) {}},
		"a data source that reads and a resource that stays": {edit: func(p *tfjson.Plan) {
			p.ResourceChanges = append(p.ResourceChanges, data("data.aws_region.current", tfjson.ActionRead), managed("aws_s3_bucket.c", "", tfjson.ActionNoop))
		}},
		"an update": {
			edit: func(p *tfjson.Plan) { p.ResourceChanges[0].Change.Actions = tfjson.Actions{tfjson.ActionUpdate} },
			want: "does more than move it (update)",
		},
		"an import": {
			edit: func(p *tfjson.Plan) { p.ResourceChanges[1].Change.Importing = &tfjson.Importing{ID: "b"} },
			want: "does more than move it",
		},
		"a create": {
			edit: func(p *tfjson.Plan) {
				p.ResourceChanges = append(p.ResourceChanges, managed("aws_s3_bucket.new", "", tfjson.ActionCreate))
			},
			want: "aws_s3_bucket.new: the plan does more than move it (create)",
		},
		"a move from elsewhere": {
			edit: func(p *tfjson.Plan) { p.ResourceChanges[0].PreviousAddress = "aws_s3_bucket.z" },
			want: `from "aws_s3_bucket.z", not from aws_s3_bucket.a`,
		},
		"a move no one asked for": {
			edit: func(p *tfjson.Plan) {
				p.ResourceChanges = append(p.ResourceChanges, managed("aws_s3_bucket.y", "aws_s3_bucket.x", tfjson.ActionNoop))
			},
			want: "which no move asked for",
		},
		"a move missing": {
			edit: func(p *tfjson.Plan) { p.ResourceChanges = p.ResourceChanges[:1] },
			want: "moves 1 resources, not 2",
		},
		"a data source that changes": {
			edit: func(p *tfjson.Plan) {
				p.ResourceChanges = append(p.ResourceChanges, data("data.x.y", tfjson.ActionDelete))
			},
			want: "does more than read it",
		},
		"deferred changes": {
			edit: func(p *tfjson.Plan) { p.DeferredChanges = []*tfjson.DeferredResourceChange{{}} },
			want: "defers changes",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := movePlan(moves)
			tc.edit(p)
			err := CheckMoveOnly(p, moves)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("error: got %v, want %q", err, tc.want)
			}
		})
	}
	if err := CheckMoveOnly(nil, moves); err == nil {
		t.Error("no plan passed")
	}
}

func TestChained(t *testing.T) {
	t.Parallel()
	if chained([]stateStep{{"a.x", "m.a.x"}, {"b.x", "m.b.x"}}) {
		t.Error("two separate moves are chained")
	}
	if !chained([]stateStep{{"a.x", "b.x"}, {"b.x", "c.x"}}) {
		t.Error("a move to another's source is not chained")
	}
}

// manyMoves returns a snapshot of a bucket that goes and n buckets, the
// import blocks that move each of the n into a module, and the moves, by
// destination.
func manyMoves(t *testing.T, n int) (*Snapshot, []ImportBlock, map[string]string) {
	t.Helper()
	ids := map[string]string{"aws_s3_bucket.gone": "gone"}
	var blocks []ImportBlock
	moves := map[string]string{}
	for i := range n {
		id := fmt.Sprintf("b%03d", i)
		from, to := "aws_s3_bucket."+id, fmt.Sprintf("module.m.aws_s3_bucket.main[%q]", id)
		ids[from] = id
		blocks = append(blocks, ImportBlock{To: to, ID: id})
		moves[to] = from
	}
	dir := t.TempDir()
	return &Snapshot{dir: dir, state: filepath.Join(dir, LocalStateFileName), ids: ids}, blocks, moves
}

func TestReconcileMovesAtOnce(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		n          int
		planUpdate bool
		failApply  error
		copyErr    error
		// applies are the copy's; commands are the root's state commands.
		applies, commands int
		wantErr           bool
	}{
		"many moves, one apply":                {n: moveAllFrom, applies: 1, commands: 1},
		"few moves, one state mv each":         {n: moveAllFrom - 1, commands: moveAllFrom},
		"a plan that does more, one at a time": {n: moveAllFrom, planUpdate: true, commands: moveAllFrom + 1},
		"no copy, one at a time":               {n: moveAllFrom, copyErr: errors.New("no copy"), commands: moveAllFrom + 1},
		"a failed apply drops the snapshot":    {n: moveAllFrom, failApply: errors.New("boom"), wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, blocks, moves := manyMoves(t, tc.n)
			plan := movePlan(moves)
			if tc.planUpdate {
				plan.ResourceChanges[0].Change.Actions = tfjson.Actions{tfjson.ActionUpdate}
			}
			copied := &fakeState{plan: plan, failApply: tc.failApply}
			copyDir := t.TempDir()
			removed := false
			copies := func(context.Context) (*MoveCopy, error) {
				if tc.copyErr != nil {
					return nil, tc.copyErr
				}
				return &MoveCopy{Dir: copyDir, Plan: copied, Apply: copied, Resources: []string{"aws_s3_bucket.kept"}, Remove: func() { removed = true }}, nil
			}
			root := &fakeState{}
			kept, err := s.reconcile(context.Background(), root, blocks, time.Minute, copies)
			if tc.wantErr {
				if err == nil {
					t.Fatal("no error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if copied.applies != tc.applies || len(root.commands) != tc.commands {
				t.Errorf("%d applies in the copy, %d state commands %v; want %d and %d", copied.applies, len(root.commands), root.commands, tc.applies, tc.commands)
			}
			if !slices.Contains(root.commands, "rm aws_s3_bucket.gone") {
				t.Errorf("the bucket that goes isn't removed: %v", root.commands)
			}
			if len(kept) != tc.n || len(s.ids) != tc.n {
				t.Errorf("kept %d, the snapshot has %d, want %d", len(kept), len(s.ids), tc.n)
			}
			for to, from := range moves {
				if kept[to] != strings.TrimPrefix(from, "aws_s3_bucket.") {
					t.Errorf("%s: kept %q", to, kept[to])
				}
			}
			if tc.copyErr == nil && tc.n >= moveAllFrom && !removed {
				t.Error("the copy wasn't removed")
			}
			if tc.applies == 0 {
				return
			}
			movesFile, err := os.ReadFile(filepath.Join(copyDir, SnapshotMovesFile))
			if err != nil {
				t.Fatal(err)
			}
			if want := "moved {\n  from = aws_s3_bucket.b000\n  to   = module.m.aws_s3_bucket.main[\"b000\"]\n}\n"; !strings.Contains(string(movesFile), want) {
				t.Errorf("moves file:\n%s\nwant it to have:\n%s", movesFile, want)
			}
			override, err := os.ReadFile(filepath.Join(copyDir, SnapshotOverrideFile))
			if err != nil || !strings.Contains(string(override), `resource "aws_s3_bucket" "kept"`) {
				t.Errorf("override: %s, %v", override, err)
			}
			if _, err := os.Stat(filepath.Join(s.dir, "moves.plan")); !os.IsNotExist(err) {
				t.Errorf("the plan of the moves stays: %v", err)
			}
		})
	}
}

func TestReconcileIndexesDestinations(t *testing.T) {
	t.Parallel()
	// Many entries and destinations of one type: each finds its own by ID.
	s, blocks, moves := manyMoves(t, 500)
	delete(s.ids, "aws_s3_bucket.gone")
	root := &fakeState{}
	kept, err := s.reconcile(context.Background(), root, blocks, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.commands) != len(moves) || len(kept) != len(moves) {
		t.Fatalf("%d commands, %d kept, want %d", len(root.commands), len(kept), len(moves))
	}
	for _, c := range root.commands {
		if f := strings.Fields(c); len(f) != 3 || moves[f[2]] != f[1] {
			t.Errorf("wrong move: %s", c)
		}
	}
}
