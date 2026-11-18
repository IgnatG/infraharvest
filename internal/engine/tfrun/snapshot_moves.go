// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/logging"
)

// reconcile makes the snapshot fit the import blocks the next plan reads.
// An entry whose address has an import block with its ID stays. One whose
// address has no block now, or a block with another ID, is moved to the
// address of a block that has its ID and its resource type and no entry
// yet, as when synthesis moves a resource into a module; where there is no
// such block it is removed, and Terraform imports it again on the next
// plan, which costs one read. It never edits the state file itself: state
// rm and state mv do, or, for many moves, one apply of moved blocks in a
// private copy of the root that copies makes (see moveAll), as state may
// be encrypted (OpenTofu). With copies nil, it moves one resource at a
// time. It returns the entries that remain, by address, with their IDs.
func (s *Snapshot) reconcile(ctx context.Context, tf StateTerraform, blocks []ImportBlock, timeout time.Duration, copies MoveCopier) (map[string]string, error) {
	current := map[string]string{}
	for _, b := range blocks {
		current[b.To] = b.ID
	}
	var unmatched []string
	for address, id := range s.ids {
		if cid, ok := current[address]; !ok || cid != id {
			unmatched = append(unmatched, address)
		}
	}
	sort.Strings(unmatched)

	// The addresses an entry may move to, by resource type and ID, in
	// order.
	destinations := map[destinationKey][]string{}
	for to, id := range current {
		if id != "" {
			k := destinationKey{resourceType(to), id}
			destinations[k] = append(destinations[k], to)
		}
	}
	for _, d := range destinations {
		sort.Strings(d)
	}

	// Every step is decided first, as running them in order would leave
	// the snapshot. Entries leave and arrive at once: a destination must be
	// free of every entry there at the time, and not taken by another move.
	ids := maps.Clone(s.ids)
	claimed := map[string]bool{}
	steps := make([]stateStep, 0, len(unmatched))
	for _, from := range unmatched {
		id := ids[from]
		to := ""
		if id != "" {
			for _, d := range destinations[destinationKey{resourceType(from), id}] {
				if _, held := ids[d]; !held && !claimed[d] {
					to = d
					break
				}
			}
		}
		steps = append(steps, stateStep{from: from, to: to})
		delete(ids, from)
		if to != "" {
			claimed[to] = true
			ids[to] = id
		}
	}

	run := func(st stateStep) error {
		err := bounded(ctx, timeout, func(ctx context.Context) error {
			if st.to == "" {
				return tf.StateRm(ctx, st.from, tfexec.State(s.state), tfexec.Lock(false)) //nolint:staticcheck // see statelessTerraform
			}
			return tf.StateMv(ctx, st.from, st.to, tfexec.State(s.state), tfexec.Lock(false)) //nolint:staticcheck // see statelessTerraform
		})
		if err != nil {
			return fmt.Errorf("reconcile the snapshot at %s: %w", st.from, err)
		}
		return nil
	}
	var moves, removals []stateStep
	for _, st := range steps {
		if st.to == "" {
			removals = append(removals, st)
		} else {
			moves = append(moves, st)
		}
	}
	if copies != nil && len(moves) >= moveAllFrom && !chained(moves) {
		// The removals go first. No move needs what they free, as a move
		// goes only to an address no entry held.
		for _, st := range removals {
			if err := run(st); err != nil {
				return nil, err
			}
		}
		applied, err := s.moveAll(ctx, copies, moves, timeout)
		switch {
		case applied && err != nil:
			// The apply may have written part of the state.
			return nil, fmt.Errorf("reconcile the snapshot: %w", err)
		case err != nil:
			if ctx.Err() != nil {
				return nil, err
			}
			logging.Warnf("import-once: moving %d resources in one apply didn't work, moving them one at a time: %v", len(moves), err)
			steps = moves
		default:
			steps = nil
		}
	}
	for _, st := range steps {
		if err := run(st); err != nil {
			return nil, err
		}
	}
	s.ids = ids
	return maps.Clone(ids), nil
}

// bounded runs run with ctx, bounded by timeout if it is set.
func bounded(ctx context.Context, timeout time.Duration, run func(ctx context.Context) error) error {
	if timeout <= 0 {
		return run(ctx)
	}
	timed, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return run(timed)
}

// destinationKey is what an entry and the address it moves to share: the
// resource type and the import ID.
type destinationKey struct {
	typ, id string
}

// stateStep is a step of a reconcile: a move from from to to, or the
// removal of from where to is "".
type stateStep struct {
	from, to string
}

// chained reports whether a move goes to the address another moves from.
// Moved blocks would read the two as one move, from the first address to
// the last.
func chained(moves []stateStep) bool {
	from := make(map[string]bool, len(moves))
	for _, m := range moves {
		from[m.from] = true
	}
	return slices.ContainsFunc(moves, func(m stateStep) bool { return from[m.to] })
}

// moveAllFrom is how many moves reconcile makes in one apply rather than
// one state mv each. Each state mv rewrites the whole state: about 150 ms
// for a state of 500 small resources, and more as the state grows. The
// apply costs an init, a plan and an apply of a copy of the root: a few
// seconds.
const moveAllFrom = 10

// MoveCopy is a private copy of a root, made for one reconcile, that a
// snapshot's moves are planned and applied in.
type MoveCopy struct {
	// Dir is the copy's directory, not initialised yet. Its configuration
	// is the root's, without import blocks or moved blocks.
	Dir string
	// Plan runs init, plan and show in the copy, with the bounds and the
	// redaction of the run's own plans; Apply is the same runner,
	// unwrapped, for the apply.
	Plan  Terraform
	Apply StateTerraform
	// Resources are the copy's top-level resources, as "<type>.<name>".
	Resources []string
	// Vars are the plan options that give the copy's variables values.
	Vars []tfexec.PlanOption
	// Remove removes the copy.
	Remove func()
}

// MoveCopier makes a MoveCopy of a root as it is now.
type MoveCopier func(ctx context.Context) (*MoveCopy, error)

// SnapshotMovesFile is the file of moved blocks of the private copy a
// snapshot's moves are made in.
const SnapshotMovesFile = "infraharvest_snapshot_moves.tf"

// moveAll makes moves in one apply, in a copy of the root that copies
// makes. It writes a moved block per move, has every top-level resource
// ignore all changes (see SnapshotOverride), plans against the snapshot
// with -refresh=false and only the moves' addresses targeted, checks that
// the plan only moves (see CheckMoveOnly), and applies it to the snapshot.
// A resource of a module whose configuration differs from what the
// snapshot holds plans an update, and fails the check. applied reports
// whether the apply ran: an error before it leaves the snapshot as it was.
func (s *Snapshot) moveAll(ctx context.Context, copies MoveCopier, moves []stateStep, timeout time.Duration) (applied bool, err error) {
	c, err := copies(ctx)
	if err != nil {
		return false, err
	}
	defer c.Remove()
	// What the snapshot's build couldn't plan with the override can't
	// plan with it here either.
	if err := fsutil.WriteFile(filepath.Join(c.Dir, SnapshotOverrideFile), SnapshotOverride(c.Resources, s.partial), 0o600); err != nil {
		return false, err
	}
	pairs := make(map[string]string, len(moves))
	opts := slices.Clone(c.Vars)
	var file bytes.Buffer
	for _, m := range moves {
		pairs[m.to] = m.from
		fmt.Fprintf(&file, "moved {\n  from = %s\n  to   = %s\n}\n\n", m.from, m.to)
		// Terraform refuses a targeted plan that leaves out either end of
		// a move.
		opts = append(opts, tfexec.Target(m.from), tfexec.Target(m.to))
	}
	if err := fsutil.WriteFile(filepath.Join(c.Dir, SnapshotMovesFile), file.Bytes(), 0o600); err != nil {
		return false, err
	}
	if err := Init(ctx, c.Plan, tfexec.Backend(false)); err != nil {
		return false, fmt.Errorf("terraform init: %w", err)
	}
	// The plan holds the snapshot's values, so it goes in the snapshot's
	// private directory.
	planFile := filepath.Join(s.dir, "moves.plan")
	defer func() { _ = os.Remove(planFile) }()
	opts = append(opts, tfexec.State(s.state), tfexec.Refresh(false), tfexec.Lock(false)) //nolint:staticcheck // -state: see statelessTerraform
	p, _, diags, err := showPlanTo(ctx, c.Plan, planFile, opts)
	if err != nil {
		return false, err
	}
	if len(diags) > 0 {
		return false, fmt.Errorf("the moves don't plan: %w", DiagnosticsError(diags))
	}
	if err := CheckMoveOnly(p, pairs); err != nil {
		return false, err
	}
	err = bounded(ctx, timeout, func(ctx context.Context) error {
		state := tfexec.State(s.state) //nolint:staticcheck // see statelessTerraform
		return c.Apply.Apply(ctx, tfexec.DirOrPlan(planFile), state, tfexec.StateOut(s.state), tfexec.Lock(false), tfexec.Backup("-"))
	})
	if err != nil {
		return true, fmt.Errorf("terraform apply of the moves: %w", err)
	}
	return true, nil
}

// CheckMoveOnly checks that p only moves resources as moves says: from the
// address each has in the snapshot, the value, to the one it moves to, the
// key. Every managed resource change is a no-op that imports nothing;
// those at the moves' destinations come from their sources, every move is
// there, and nothing else moves. Data sources only read, and nothing is
// invoked or deferred. Applying such a plan writes state and calls no
// provider write.
func CheckMoveOnly(p *tfjson.Plan, moves map[string]string) error {
	if p == nil {
		return errors.New("no plan")
	}
	if len(p.ActionInvocations) > 0 {
		return errors.New("the plan invokes actions")
	}
	if len(p.DeferredChanges) > 0 {
		return errors.New("the plan defers changes")
	}
	moved := 0
	for _, rc := range p.ResourceChanges {
		if rc.Change == nil {
			continue
		}
		if rc.Mode == tfjson.DataResourceMode {
			if !rc.Change.Actions.Read() && !rc.Change.Actions.NoOp() {
				return fmt.Errorf("%s: the plan does more than read it", rc.Address)
			}
			continue
		}
		if !rc.Change.Actions.NoOp() || rc.Change.Importing != nil {
			return fmt.Errorf("%s: the plan does more than move it (%s)", rc.Address, strings.Join(ActionNames(rc.Change.Actions), ","))
		}
		from, ok := moves[rc.Address]
		switch {
		case ok && rc.PreviousAddress != from:
			return fmt.Errorf("%s: the plan moves it from %q, not from %s", rc.Address, rc.PreviousAddress, from)
		case !ok && rc.PreviousAddress != "":
			return fmt.Errorf("%s: the plan moves it from %s, which no move asked for", rc.Address, rc.PreviousAddress)
		case ok:
			moved++
		}
	}
	if moved != len(moves) {
		return fmt.Errorf("the plan moves %d resources, not %d", moved, len(moves))
	}
	return nil
}

var instanceKeys = regexp.MustCompile(`\[[^\]]*\]`)

// resourceType returns the type of the resource at address, such as
// aws_s3_bucket for module.a["x"].aws_s3_bucket.main[0].
func resourceType(address string) string {
	parts := strings.Split(instanceKeys.ReplaceAllString(address, ""), ".")
	if len(parts) < 2 {
		return address
	}
	return parts[len(parts)-2]
}
