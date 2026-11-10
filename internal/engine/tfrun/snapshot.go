// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// A state snapshot is the import-once cache of a root: the state of
// importing every resource of the root, kept in a private file outside the
// output, so that the plans that follow need not import and read every
// resource again. Every plan of a root otherwise has an import block per
// resource and no state, and so reads the whole root from the cloud.
//
// The snapshot is built by applying a saved plan in a private copy of the
// root whose resources ignore all changes: the plan only imports, so the
// apply writes the state and calls no provider write. The plans that follow
// run with -state=<snapshot> -refresh=false: Terraform skips the import of
// an address that is in state. See Cached for how those plans keep the view
// the engine expects, and docs/development.md for the security model.

// SnapshotPattern names the private temporary directories of snapshots.
const SnapshotPattern = "infraharvest-snapshot-"

// staleSnapshotAge is how old a snapshot directory must be to count as left
// behind by a run that was killed, rather than in use by another run.
const staleSnapshotAge = 12 * time.Hour

// SnapshotOverrideFile is the override file of the private copy a snapshot
// is built in.
const SnapshotOverrideFile = "infraharvest_snapshot_override.tf"

// ImportBlock is an import block of a root: the address it imports into and
// the ID it imports with, "" where that isn't a literal.
type ImportBlock struct {
	To, ID string
}

// StateTerraform is a Terraform that also runs the commands a snapshot is
// built and kept with: *Runner is one.
type StateTerraform interface {
	Terraform
	Apply(ctx context.Context, opts ...tfexec.ApplyOption) error
	StateMv(ctx context.Context, source, destination string, opts ...tfexec.StateMvCmdOption) error
	StateRm(ctx context.Context, address string, opts ...tfexec.StateRmCmdOption) error
}

var _ StateTerraform = (*Runner)(nil)

var staleOnce sync.Once

// RemoveStaleSnapshots removes the directories of snapshots, saved plans
// and empty states in the system's temporary directory that a run killed
// before it could remove them left behind, once per process: snapshots and
// saved plans hold secret values. Only directories older than a run can
// last are removed, as another run may be using a younger one.
func RemoveStaleSnapshots() {
	staleOnce.Do(func() { removeStaleSnapshots(os.TempDir(), time.Now(), staleSnapshotAge) })
}

// stalePatterns name the temporary directories RemoveStaleSnapshots
// removes.
var stalePatterns = []string{SnapshotPattern, PlanPattern, EmptyStatePattern}

func removeStaleSnapshots(tmp string, now time.Time, maxAge time.Duration) {
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !slices.ContainsFunc(stalePatterns, func(p string) bool { return strings.HasPrefix(name, p) }) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < maxAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(tmp, e.Name()))
	}
}

// Snapshot is the state of a root's imports, in a private file (see the
// comment above). It holds every attribute of every resource in the clear,
// secrets included: it lives in a directory of its own in the system's
// temporary directory, and is removed by Remove.
type Snapshot struct {
	dir   string
	state string
	// ids are the import IDs of the entries by address, as they were
	// imported or moved to: state doesn't keep them.
	ids map[string]string
}

// Path returns the snapshot's state file.
func (s *Snapshot) Path() string { return s.state }

// Len returns how many resources the snapshot holds.
func (s *Snapshot) Len() int { return len(s.ids) }

// Remove removes the snapshot, state and directory. It may be called more
// than once.
func (s *Snapshot) Remove() {
	if s != nil && s.dir != "" {
		_ = os.RemoveAll(s.dir)
	}
}

// SnapshotBuild is what BuildSnapshot needs.
type SnapshotBuild struct {
	// Plan runs the commands that don't write state in the private copy of
	// the root (see gate.VerifyCopy), with the bounds and the redaction the
	// run's own plans have; Apply is the same runner, unwrapped, for the
	// one command that does. The copy is not initialised yet.
	Plan  Terraform
	Apply StateTerraform
	// Dir is the copy's directory.
	Dir string
	// Resources are the copy's resources, as "<type>.<name>".
	Resources []string
	// Imports are the import blocks of the root: the plan must import them
	// all.
	Imports []ImportBlock
	// Vars are the plan options that give the copy's variables values.
	Vars []tfexec.PlanOption
	// Timeout bounds the apply.
	Timeout time.Duration
}

// BuildSnapshot builds a snapshot of the root b describes: it adds an
// override file that has every resource ignore all changes to the copy,
// plans the copy into a saved plan, checks the plan only imports (see
// CheckImportOnly), and applies it into a new private state file. It
// returns an error, and leaves nothing behind, if the plan isn't just
// imports: the caller then goes on without a snapshot.
func BuildSnapshot(ctx context.Context, b SnapshotBuild) (*Snapshot, error) {
	if len(b.Imports) == 0 {
		return nil, errors.New("no import blocks")
	}
	RemoveStaleSnapshots()
	dir, err := os.MkdirTemp("", SnapshotPattern)
	if err != nil {
		return nil, err
	}
	// The mode is 0700 already; this also covers Windows, which the mode
	// doesn't.
	if _, err := fsutil.MakePrivate(dir); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	s := &Snapshot{dir: dir, state: filepath.Join(dir, LocalStateFileName), ids: map[string]string{}}
	if err := s.build(ctx, b); err != nil {
		s.Remove()
		return nil, err
	}
	return s, nil
}

func (s *Snapshot) build(ctx context.Context, b SnapshotBuild) error {
	if err := fsutil.WriteFile(filepath.Join(b.Dir, SnapshotOverrideFile), SnapshotOverride(b.Resources), 0o600); err != nil {
		return err
	}
	// The lock file in the copy pins the providers, so that the state
	// the plan is applied to is of the release the root's plans use.
	if err := Init(ctx, b.Plan, tfexec.Backend(false)); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}
	planFile := filepath.Join(s.dir, "plan")
	p, _, diags, err := showPlanTo(ctx, b.Plan, planFile, b.Vars)
	if err != nil {
		return err
	}
	if len(diags) > 0 {
		return fmt.Errorf("the copy doesn't plan: %w", DiagnosticsError(diags))
	}
	if err := CheckImportOnly(p); err != nil {
		return err
	}
	importing := 0
	for _, rc := range p.ResourceChanges {
		if rc.Mode == tfjson.ManagedResourceMode {
			importing++
		}
	}
	if importing != len(b.Imports) {
		return fmt.Errorf("the plan imports %d resources, the root has %d import blocks", importing, len(b.Imports))
	}
	applyCtx := ctx
	if b.Timeout > 0 {
		var cancel context.CancelFunc
		applyCtx, cancel = context.WithTimeout(ctx, b.Timeout)
		defer cancel()
	}
	// -backup=- keeps a second copy of the state from being written. -state
	// is a legacy flag that Terraform and OpenTofu still take for the local
	// backend, as statelessTerraform uses it.
	state := tfexec.State(s.state) //nolint:staticcheck // see above
	if err := b.Apply.Apply(applyCtx, tfexec.DirOrPlan(planFile), state, tfexec.StateOut(s.state), tfexec.Lock(false), tfexec.Backup("-")); err != nil {
		return fmt.Errorf("terraform apply of the import-only plan: %w", err)
	}
	_ = os.Remove(planFile)
	for _, imp := range b.Imports {
		s.ids[imp.To] = imp.ID
	}
	return nil
}

// SnapshotOverride returns the override file that has every one of
// resources, as "<type>.<name>", ignore all changes: the plan of the copy
// then imports the resources and changes nothing.
func SnapshotOverride(resources []string) []byte {
	var b bytes.Buffer
	for _, r := range resources {
		typ, name, ok := strings.Cut(r, ".")
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "resource %q %q {\n  lifecycle {\n    ignore_changes = all\n  }\n}\n\n", typ, name)
	}
	return b.Bytes()
}

// CheckImportOnly checks that p only imports: every managed resource
// change is a no-op that imports, data sources only read, and there is
// nothing to add, change, remove or invoke. Applying such a plan writes
// state and calls no provider write, which is what makes a snapshot safe
// to build; a Terraform or OpenTofu release that changes what an import
// plans fails here, not in the cloud.
func CheckImportOnly(p *tfjson.Plan) error {
	if p == nil {
		return errors.New("no plan")
	}
	if len(p.ActionInvocations) > 0 {
		return errors.New("the plan invokes actions")
	}
	if len(p.DeferredChanges) > 0 {
		return errors.New("the plan defers changes")
	}
	for _, rc := range p.ResourceChanges {
		if rc.Change == nil {
			continue
		}
		switch rc.Mode {
		case tfjson.DataResourceMode:
			if !rc.Change.Actions.Read() && !rc.Change.Actions.NoOp() {
				return fmt.Errorf("%s: the plan does more than read it", rc.Address)
			}
		default:
			if !rc.Change.Actions.NoOp() || rc.Change.Importing == nil {
				return fmt.Errorf("%s: the plan does more than import it (%s)", rc.Address, strings.Join(ActionNames(rc.Change.Actions), ","))
			}
		}
	}
	return nil
}

// Cached is a Terraform whose plans, while it has a snapshot (see Use), run
// against it: with -state=<snapshot> -refresh=false -lock=false, so that
// the resources the snapshot holds are not imported and read again. The
// snapshot and the root's import blocks are reconciled before each plan,
// and the plan's output and its shown plan get back what the import left
// out, so that what reads them sees the plans as before: the summary's
// import count, and importing on the changes of the imports. Without a
// snapshot, or once it is released, it runs the plain Terraform it was
// made with.
type Cached struct {
	// Terraform runs every command but a cached plan: it is the plain
	// runner, as the engine plans without a snapshot.
	Terraform

	raw     StateTerraform
	timeout time.Duration
	imports func() ([]ImportBlock, error)
	// copies makes the private copy many moves are made in at once, if
	// set (see Snapshot.reconcile).
	copies MoveCopier

	snap *Snapshot
	// kept are the imports the last cached plan skipped, by address.
	kept   map[string]string
	cached int
}

// NewCached returns plain, with plans against a snapshot once it is given
// one (see Use). raw is the runner the cached plans and the snapshot's
// state commands run with, without anything that sets another -state, such
// as Stateless; timeout bounds each of those commands, if set, and imports
// returns the root's import blocks as they are.
func NewCached(plain Terraform, raw StateTerraform, timeout time.Duration, imports func() ([]ImportBlock, error)) *Cached {
	return &Cached{Terraform: plain, raw: raw, timeout: timeout, imports: imports}
}

// MoveIn has the snapshot's moves made in copies that copies makes, where
// a reconcile moves many resources at once (see Snapshot.reconcile).
func (c *Cached) MoveIn(copies MoveCopier) {
	c.copies = copies
}

// Use has the plans that follow run against s.
func (c *Cached) Use(s *Snapshot) {
	c.snap, c.kept = s, nil
}

// Release removes the snapshot, if there is one, and has the plans that
// follow run plain. It may be called more than once.
func (c *Cached) Release() {
	if c == nil {
		return
	}
	c.snap.Remove()
	c.snap, c.kept = nil, nil
}

// Active reports whether plans run against a snapshot.
func (c *Cached) Active() bool { return c != nil && c.snap != nil }

// CachedPlans returns how many plans ran against a snapshot.
func (c *Cached) CachedPlans() int {
	if c == nil {
		return 0
	}
	return c.cached
}

func (c *Cached) PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	if c.snap == nil {
		return c.Terraform.PlanJSON(ctx, w, opts...)
	}
	blocks, err := c.imports()
	if err == nil {
		c.kept, err = c.snap.reconcile(ctx, c.raw, blocks, c.timeout, c.copies)
	}
	if err != nil {
		// A snapshot that can't follow the root is of no use: plan as
		// before, and never from a state that may not match it.
		log.Printf("import-once: dropping the snapshot: %v", err)
		c.Release()
		return c.Terraform.PlanJSON(ctx, w, opts...)
	}
	plan := append(append([]tfexec.PlanOption(nil), opts...),
		tfexec.State(c.snap.state), tfexec.Refresh(false), tfexec.Lock(false)) //nolint:staticcheck // -state: see statelessTerraform
	var out bytes.Buffer
	changed, planErr := WithTimeout(c.raw, c.timeout).PlanJSON(ctx, &out, plan...)
	c.cached++
	data, err := withImports(out.Bytes(), len(c.kept))
	if err != nil {
		data = out.Bytes()
	}
	if _, werr := w.Write(data); werr != nil && planErr == nil {
		planErr = werr
	}
	return changed, planErr
}

// ShowPlanFile shows the plan, with importing set on the changes of the
// imports the cached plan skipped.
func (c *Cached) ShowPlanFile(ctx context.Context, planPath string, opts ...tfexec.ShowOption) (*tfjson.Plan, error) {
	p, err := c.Terraform.ShowPlanFile(ctx, planPath, opts...)
	if err != nil || p == nil || c.snap == nil {
		return p, err
	}
	for _, rc := range p.ResourceChanges {
		if id, ok := c.kept[rc.Address]; ok && rc.Mode == tfjson.ManagedResourceMode && rc.Change != nil && rc.Change.Importing == nil {
			rc.Change.Importing = &tfjson.Importing{ID: id}
		}
	}
	return p, nil
}

// withImports returns out, Terraform's machine-readable output of a plan,
// with skipped more imports in its change summary.
func withImports(out []byte, skipped int) ([]byte, error) {
	if skipped == 0 {
		return out, nil
	}
	var result bytes.Buffer
	err := eachUIMessage(out, func(line []byte) {
		if bytes.Contains(line, []byte(`"change_summary"`)) {
			if edited, ok := summaryWithImports(line, skipped); ok {
				line = edited
			}
		}
		result.Write(line)
		result.WriteByte('\n')
	})
	return result.Bytes(), err
}

func summaryWithImports(line []byte, skipped int) ([]byte, bool) {
	var msg map[string]json.RawMessage
	if json.Unmarshal(line, &msg) != nil {
		return nil, false
	}
	var typ string
	if json.Unmarshal(msg["type"], &typ) != nil || typ != "change_summary" {
		return nil, false
	}
	var changes map[string]json.RawMessage
	if json.Unmarshal(msg["changes"], &changes) != nil {
		return nil, false
	}
	var imported int
	if raw, ok := changes["import"]; ok && json.Unmarshal(raw, &imported) != nil {
		return nil, false
	}
	count, err := json.Marshal(imported + skipped)
	if err != nil {
		return nil, false
	}
	changes["import"] = count
	if msg["changes"], err = json.Marshal(changes); err != nil {
		return nil, false
	}
	edited, err := json.Marshal(msg)
	return edited, err == nil
}

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
			log.Printf("import-once: moving %d resources in one apply didn't work, moving them one at a time: %v", len(moves), err)
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
	if err := fsutil.WriteFile(filepath.Join(c.Dir, SnapshotOverrideFile), SnapshotOverride(c.Resources), 0o600); err != nil {
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
