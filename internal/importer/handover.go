// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// handovers are what a run with --regenerate hands over between the roots
// it generates (see planHandovers), by root directory: the removed blocks
// a root gets for the resources it had that now go to other roots, the
// resources a root imports that other roots hand over, and the roots left
// as they were, with why. Once generated, each root's result is recorded
// (see record), nil for a root that failed, to tell what a root that
// failed left half done (see warnIncomplete).
type handovers struct {
	out      string
	removals map[string][]engine.Removal
	arrivals map[string][]engine.Arrival
	refused  map[string]error
	// merged are the removed blocks of the merged roots environments share,
	// by the merged root's directory, which its moves.tf gets once its
	// stack merges again (see planMergedHandovers).
	merged map[string][]engine.Removal
	// vacating are, by the directory of a merged root that no root of the
	// run merges into, what leaves it for other roots of the run, of the
	// environments all of whose resources in it go (see
	// planMergedHandovers): once every environment's have gone, and the
	// roots they go to imported them, the merge phase vacates it (see
	// vacateMergedRoots).
	vacating map[string][]mergedLeaving
	// mu guards generated, as the calls of a scope may generate their
	// roots at once.
	mu        sync.Mutex
	generated map[string]*engine.Result
}

// record records what generating the root in dir gave: result, or
// nothing, with err, if it failed.
func (h *handovers) record(dir string, result *engine.Result, err error) {
	if h == nil {
		return
	}
	if err != nil {
		result = nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.generated[filepath.Clean(dir)] = result
}

// dirOf returns the directory of root, relative to the output directory.
func (h *handovers) dirOf(root string) string {
	return filepath.Join(h.out, filepath.FromSlash(root))
}

// order returns dirs, the roots a call generates, sorted, in the order
// to generate them: each root that hands resources over after the roots
// of dirs they go to, unless those hand resources over to it too, so
// that it can be left as it was should one of them not import what it
// hands over (see unreceived).
func (h *handovers) order(dirs []string) []string {
	if h == nil || len(h.removals) == 0 {
		return dirs
	}
	in := map[string]bool{}
	for _, dir := range dirs {
		in[filepath.Clean(dir)] = true
	}
	done := map[string]bool{}
	ordered := make([]string, 0, len(dirs))
	for len(ordered) < len(dirs) {
		next := ""
		for _, dir := range dirs {
			if done[filepath.Clean(dir)] {
				continue
			}
			if next == "" {
				next = dir // should the roots left hand over to each other
			}
			ready := true
			for _, r := range h.removalsOf(dir) {
				for _, d := range r.Resources {
					if to := h.dirOf(d.Root); in[to] && !done[to] {
						ready = false
					}
				}
			}
			if ready {
				next = dir
				break
			}
		}
		done[filepath.Clean(next)] = true
		ordered = append(ordered, next)
	}
	return ordered
}

// unreceived returns what the root in dir would hand over to roots
// generated before it that didn't import it, as they failed or left it
// out: its removed blocks would make its state forget what no root
// imports.
func (h *handovers) unreceived(dir string) []engine.Departure {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var missing []engine.Departure
	for _, r := range h.removalsOf(dir) {
		for _, d := range r.Resources {
			if result, done := h.generated[h.dirOf(d.Root)]; done && arrivalOf(result, d) == nil {
				missing = append(missing, d)
			}
		}
	}
	return missing
}

// refuseUnreceived returns why the root in dir is left as it was when it
// would hand over what roots generated before it didn't import (see
// unreceived); nil if it isn't.
func (h *handovers) refuseUnreceived(dir string) error {
	missing := h.unreceived(dir)
	if len(missing) == 0 {
		return nil
	}
	var why []string
	for _, d := range missing {
		why = append(why, fmt.Sprintf("%s goes to %s, which didn't import it", d.Address, d.Root))
	}
	return fmt.Errorf("generating %s again would hand %d of its resources over to roots that didn't import them, and its state would forget them, so it is left as it was: %s", dir, len(missing), strings.Join(why, "; "))
}

// arrivalOf returns how result, a root's, imports d, which another root
// hands over; nil if it doesn't, or the root failed.
func arrivalOf(result *engine.Result, d engine.Departure) *engine.Arrival {
	if result == nil {
		return nil
	}
	for i, a := range result.Arrived {
		if a.HadType() == d.Type && a.ID == d.ID {
			return &result.Arrived[i]
		}
	}
	return nil
}

// removes says whether result, a root's, has a removed block that hands
// d over; false if the root failed.
func removes(result *engine.Result, d engine.Departure) bool {
	if result == nil {
		return false
	}
	for _, r := range result.Removed {
		for _, had := range r.Resources {
			if had.Type == d.Type && had.ID == d.ID {
				return true
			}
		}
	}
	return false
}

// incomplete returns the handovers one of whose roots didn't finish, as
// it failed, or wasn't generated: the root a resource leaves, whose
// state still has it, while the root it goes to imports it; or the root
// it goes to, which didn't import it, while the removed block of the
// root it leaves would make that root's state forget it. Applied, the
// first would leave the resource in both roots' states, and the second
// in neither. A handover both roots failed leaves the resource where it
// was.
func (h *handovers) incomplete() []report.IncompleteHandover {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var incomplete []report.IncompleteHandover
	for _, dir := range sortedDirs(h.removals) {
		from := relativePath(h.out, dir)
		for _, r := range h.removals[dir] {
			for _, d := range r.Resources {
				left := removes(h.generated[dir], d)
				arrival := arrivalOf(h.generated[h.dirOf(d.Root)], d)
				m := report.Move{Type: d.Type, ID: d.ID, FromRoot: from, From: d.Address, ToRoot: d.Root}
				switch {
				case arrival != nil && !left:
					m.To = arrival.To
					incomplete = append(incomplete, report.IncompleteHandover{Move: m, FailedRoot: from, Warning: fmt.Sprintf("%s imports it, but %s failed, and its state still has it: generate %s again with --regenerate before applying either root", d.Root, from, from)})
				case arrival == nil && left:
					incomplete = append(incomplete, report.IncompleteHandover{Move: m, FailedRoot: d.Root, Warning: fmt.Sprintf("the removed block of %s makes its state forget it, but %s, where it goes, didn't import it: don't apply %s until %s, generated again with --regenerate, imports it", from, d.Root, from, d.Root)})
				}
			}
		}
	}
	return incomplete
}

// warnIncomplete warns of each handover of calls, those of one scope,
// that one of its roots didn't finish (see handovers.incomplete), and
// records it into run.
func warnIncomplete(run *Run, calls []*listedCall) {
	for _, c := range calls {
		if c == nil || c.handovers == nil {
			continue
		}
		for _, i := range c.handovers.incomplete() {
			log.Printf("warning: the handover of %s (%s) from %s to %s isn't finished: %s", i.From, i.ID, i.FromRoot, i.ToRoot, i.Warning)
			run.report.IncompleteHandovers = append(run.report.IncompleteHandovers, i)
		}
		return // the calls share their handovers
	}
}

// removalsOf returns the removed blocks the root in dir gets; none
// without handovers.
func (h *handovers) removalsOf(dir string) []engine.Removal {
	if h == nil {
		return nil
	}
	return h.removals[filepath.Clean(dir)]
}

// arrivalsOf returns what the root in dir imports that other roots hand
// over; none without handovers.
func (h *handovers) arrivalsOf(dir string) []engine.Arrival {
	if h == nil {
		return nil
	}
	return h.arrivals[filepath.Clean(dir)]
}

// refusal returns why the root in dir is left as it was; nil if it isn't.
func (h *handovers) refusal(dir string) error {
	if h == nil {
		return nil
	}
	return h.refused[filepath.Clean(dir)]
}

// departing is a resource of an earlier import's root that the run puts
// in another root, at to, as now: its type and import ID, or another name
// of its type (see engine.AliasTypes).
type departing struct {
	had engine.Managed
	to  string
	now engine.External
}

// planHandovers works out, for an import with --regenerate whose calls,
// those of one scope, are listed, which resources of the roots earlier
// imports generated go to another root of the run now (see departures),
// and records it in each call. A root of the run hands them over: it
// gets a removed block for each (see engine.Removal), and the root they
// go to imports them as it would any other. A root the run doesn't
// generate, which a removed block needs edited, keeps them: they are left
// out of the roots they would go to, as InOtherRootReason, as an
// incremental import does (see leaveOutHeld). But a root whose every
// resource goes to roots of the run is generated with removed blocks
// alone (see engine.Vacate), by the call of the first root they go to.
// A root of the run whose resources removed blocks can't hand over,
// such as part of a call a module from the registry makes (see
// handOver), is left as it was and reported as failed, and they are left
// out of the roots they would go to too.
func planHandovers(out string, calls []*listedCall) error {
	var live []*listedCall
	for _, c := range calls {
		if c != nil {
			live = append(live, c)
		}
	}
	if len(live) == 0 || !live[0].options.Regenerate {
		return nil
	}
	callOf := map[string]*listedCall{}
	assigned := map[engine.External]string{}
	for _, c := range live {
		for _, dir := range sortedDirs(c.byDir) {
			callOf[filepath.Clean(dir)] = c
			for _, imp := range c.byDir[dir] {
				key := engine.ResourceKey(imp.Type, imp.ID)
				if _, ok := assigned[key]; !ok {
					assigned[key] = filepath.Clean(dir)
				}
			}
		}
	}
	// What the roots' own state says they have, if --managed-state read it.
	var s *shared
	if live[0].run != nil {
		s = live[0].run.shared
	}
	gone, err := departures(out, assigned, s)
	if err != nil {
		return err
	}
	h := &handovers{out: out, removals: map[string][]engine.Removal{}, arrivals: map[string][]engine.Arrival{}, refused: map[string]error{}, merged: map[string][]engine.Removal{}, vacating: map[string][]mergedLeaving{}, generated: map[string]*engine.Result{}}
	// What stays in the root it was in, by type and import ID, with that
	// root relative to out.
	stays := map[engine.External]string{}
	problems := map[string][]string{}
	for _, dir := range sortedDirs(gone) {
		leaving := gone[dir]
		if _, inRun := callOf[dir]; !inRun {
			previous, err := s.previousAddressesOf(out, dir)
			if err != nil {
				return err
			}
			if len(leaving) < len(previous) {
				for _, d := range leaving {
					stays[d.now] = relativePath(out, dir)
				}
				continue
			}
			// Every resource of the root goes: it is generated with its
			// removed blocks alone, by the call of the first root they go
			// to.
			owner := callOf[leaving[0].to]
			owner.byDir[dir] = nil
			callOf[dir] = owner
		}
		removals, why, err := handOver(out, dir, leaving, s)
		if err != nil {
			return err
		}
		if len(why) > 0 {
			problems[dir] = why
			continue
		}
		h.removals[dir] = removals
	}
	// A root that hands resources over to a root left as it was would
	// leave them in neither.
	for changed := true; changed; {
		changed = false
		for _, dir := range sortedDirs(h.removals) {
			for _, d := range gone[dir] {
				if _, ok := problems[d.to]; ok {
					problems[dir] = append(problems[dir], fmt.Sprintf("%s goes to %s, which is left as it was", d.had.Address, relativePath(out, d.to)))
					delete(h.removals, dir)
					changed = true
					break
				}
			}
		}
	}
	for _, dir := range sortedDirs(problems) {
		h.refused[dir] = fmt.Errorf("generating %s again would hand %d of its resources over to other roots where no removed block can take them, and a plan on its state would destroy them, so it is left as it was, and they are left out of the roots they go to: %s", dir, len(gone[dir]), strings.Join(problems[dir], "; "))
		for _, d := range gone[dir] {
			stays[d.now] = relativePath(out, dir)
		}
	}
	// And what goes from the roots environments share to other roots.
	if err := planMergedHandovers(out, h, callOf, assigned, stays); err != nil {
		return err
	}
	for _, dir := range sortedDirs(h.removals) {
		root := relativePath(out, dir)
		for _, d := range gone[dir] {
			log.Printf("%s: %s goes to %s, and a removed block hands it over (--regenerate)", root, d.had.Address, relativePath(out, d.to))
			arrival := engine.Arrival{Type: d.now.Type, ID: d.had.ID, Root: root, From: d.had.Address}
			if d.now.Type != d.had.Type {
				arrival.FromType = d.had.Type
			}
			h.arrivals[d.to] = append(h.arrivals[d.to], arrival)
		}
	}
	if len(h.vacating) > 0 && live[0].run != nil {
		live[0].run.vacating = append(live[0].run.vacating, h)
	}
	for _, c := range live {
		c.handovers = h
		c.run.leaveOutStaying(c.scope, c.byDir, stays, h.removals)
	}
	return nil
}

// leaveOutStaying removes from byDir the imports of what stays in the
// root an earlier import put it in (see planHandovers), recording each
// as excluded in scope, with the root, and drops the roots it leaves with
// nothing to import, but for those with removals, which are generated
// with their removed blocks alone.
func (r *Run) leaveOutStaying(scope string, byDir map[string][]engine.Import, stays map[engine.External]string, removals map[string][]engine.Removal) {
	if len(stays) == 0 {
		return
	}
	for _, dir := range sortedDirs(byDir) {
		imports := byDir[dir]
		if len(imports) == 0 {
			continue // a root that hands over all it had
		}
		var kept []engine.Import
		for _, imp := range imports {
			if root, ok := stays[engine.ResourceKey(imp.Type, imp.ID)]; ok {
				r.report.Excluded = append(r.report.Excluded, report.Excluded{Type: imp.Type, ID: imp.ID, Reason: InOtherRootReason + ", " + root, Scope: scope})
				continue
			}
			kept = append(kept, imp)
		}
		if len(kept) == 0 && len(removals[filepath.Clean(dir)]) > 0 {
			byDir[dir] = nil
			continue
		}
		if len(kept) == 0 {
			log.Printf("%s: each of its resources stays in another root of the output (--regenerate)", dir)
			delete(byDir, dir)
			continue
		}
		byDir[dir] = kept
	}
}

// departures returns, by the directory of each root the checkpoints under
// out record, the resources an earlier import put in it, by where it put
// them, or where its own state has them (see shared.previousAddressesOf,
// with s), that assigned, the root of the run each resource goes to by
// type and import ID, puts in another root now, as their type or, if
// assigned has none of that, another name of it (see engine.AliasTypes),
// such as an aws_alb that is an aws_lb now. Only
// roots that still have configuration count, applied ones too, with
// their import blocks deleted; a root whose import didn't finish counts
// only while it is generated again (see checkpoint.Pending). A checkpoint
// that can't be read is logged and passed over.
func departures(out string, assigned map[engine.External]string, s *shared) (map[string][]departing, error) {
	dirs, err := checkpointDirs(out)
	if err != nil {
		return nil, err
	}
	gone := map[string][]departing{}
	for _, dir := range dirs {
		dir = filepath.Clean(dir)
		names, err := tfFiles(dir)
		if err != nil {
			return nil, err
		}
		if len(names) == 0 {
			continue
		}
		c, err := readCheckpoint(out, dir)
		if err != nil {
			log.Printf("%s: %v; what that root has isn't handed over to the others (--regenerate)", relativePath(out, dir), err)
			continue
		}
		if c == nil || (c.Result == nil && len(c.Pending) == 0) {
			continue
		}
		previous, err := s.previousAddressesOf(out, dir)
		if err != nil {
			return nil, err
		}
		for _, p := range previous {
			if now, to, ok := assignedAs(assigned, p); ok && to != dir {
				gone[dir] = append(gone[dir], departing{had: p, to: to, now: now})
			}
		}
		sort.Slice(gone[dir], func(i, j int) bool { return gone[dir][i].had.Address < gone[dir][j].had.Address })
	}
	return gone, nil
}

// assignedAs returns the type and import ID assigned, the root of the
// run each resource goes to, has p, a resource of an earlier import, as:
// its own, or another name of its type (see engine.AliasTypes); and the
// root.
func assignedAs(assigned map[engine.External]string, p engine.Managed) (engine.External, string, bool) {
	for _, typ := range append([]string{p.Type}, engine.AliasTypes(p.Type)...) {
		key := engine.External{Type: typ, ID: p.ID}
		if to, ok := assigned[key]; ok {
			return key, to, true
		}
	}
	return engine.External{}, "", false
}

// handOver returns the removed blocks the root in dir, an earlier
// import's, gets for leaving, those of its resources that go to other
// roots now, or what keeps them from it. A removed block takes a whole
// resource, with no instance key, or a whole module call: when only some
// of it goes, each resource that goes gets a removed block of its own,
// of a resource of the root alone that a moved block moves it to first
// (see engine.Removal.Instance), which a resource in a call that a module
// from the registry makes can't be, as moved blocks stay within a module
// package. A moved block of its moves.tf that a plan may not have applied
// yet mustn't move it, nor an address only inferred name it. Where the
// root's own state has its resources, s says (see
// shared.previousAddressesOf).
func handOver(out, dir string, leaving []departing, s *shared) ([]engine.Removal, []string, error) {
	previous, err := s.previousAddressesOf(out, dir)
	if err != nil {
		return nil, nil, err
	}
	moved, err := pendingMoves(out, dir)
	if err != nil {
		return nil, nil, err
	}
	c, err := readCheckpoint(out, dir)
	if err != nil {
		return nil, nil, err
	}
	var calls []engine.ModuleCall
	if c != nil && c.Result != nil {
		calls = c.Result.Modules
	}
	pending, err := engine.ReadRemoved(dir)
	if err != nil {
		return nil, nil, err
	}
	members := map[string]int{}
	var addresses []string
	for _, p := range previous {
		members[engine.RemovedFrom(p.Address)]++
		addresses = append(addresses, p.Address)
	}
	for _, m := range moved {
		addresses = append(addresses, m.From, m.To)
	}
	for _, r := range pending {
		addresses = append(addresses, r.From)
	}
	taken := engine.RootResources(addresses...)
	byFrom := map[string][]departing{}
	for _, d := range leaving {
		from := engine.RemovedFrom(d.had.Address)
		byFrom[from] = append(byFrom[from], d)
	}
	var removals []engine.Removal
	var problems []string
	for _, from := range sortedDirs(byFrom) {
		going := byFrom[from]
		roots := map[string]bool{}
		removal := engine.Removal{From: from}
		problem := ""
		for _, d := range going {
			roots[relativePath(out, d.to)] = true
			removal.Resources = append(removal.Resources, engine.Departure{Type: d.had.Type, ID: d.had.ID, Address: d.had.Address, Root: relativePath(out, d.to)})
			if d.had.Unverified {
				problem = fmt.Sprintf("%s goes to %s, but where the root has it is only inferred, as its import blocks are deleted", d.had.Address, relativePath(out, d.to))
			}
		}
		where := strings.Join(slices.Sorted(maps.Keys(roots)), ", ")
		partial := len(going) < members[from]
		for _, d := range going {
			if call := engine.PackagedCall(d.had.Address, calls); problem == "" && partial && call != "" {
				problem = fmt.Sprintf("only part of %s goes to %s: a removed block takes only the whole module call, and a moved block can't move %s out of a call %s makes for one to take it alone", from, where, d.had.Address, call)
			}
		}
		for _, m := range moved {
			if problem == "" && (engine.RemovedFrom(m.From) == from || engine.RemovedFrom(m.To) == from) {
				problem = fmt.Sprintf("%s goes to %s, and %s still moves it: plan and apply the root, and delete %s, before generating it again", from, where, engine.MovesFileName, engine.MovesFileName)
			}
		}
		switch {
		case problem != "":
			problems = append(problems, problem)
		case partial:
			// Only some of it goes: each resource that goes alone.
			for _, d := range removal.Resources {
				removals = append(removals, engine.Removal{From: engine.DepartedAddress(d.Address, taken), Resources: []engine.Departure{d}, Instance: d.Address})
			}
		default:
			removals = append(removals, removal)
		}
	}
	return removals, problems, nil
}

// pendingMoves returns the moved blocks the root in dir may not have
// applied yet: those of its moves.tf or, while a run that stopped
// generates it again, those its checkpoint kept (see claimRoot).
func pendingMoves(out, dir string) ([]engine.Move, error) {
	c, err := readCheckpoint(out, dir)
	if err != nil || c == nil {
		return nil, err
	}
	if c.Result == nil {
		return c.PendingMoves, nil
	}
	return engine.ReadMoves(dir)
}

// checkpointDirs returns the directories of the roots the checkpoints
// under out record, sorted.
func checkpointDirs(out string) ([]string, error) {
	base := filepath.Join(out, CheckpointDir, "checkpoints")
	if _, err := os.Stat(base); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	var dirs []string
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		dirs = append(dirs, filepath.Join(out, strings.TrimSuffix(rel, ".json")))
		return nil
	})
	sort.Strings(dirs)
	return dirs, err
}

// sortedDirs returns the keys of m, sorted.
func sortedDirs[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

// recordMoves records what the result of the root at path, relative to
// the output directory, moves within it, as moved blocks do or, for a
// resource of another name of its type now, removed and import blocks
// (see engine.Retype), hands over to other roots, and imports that other
// roots hand over (see report.Move).
func (r *Run) recordMoves(path string, result *engine.Result) {
	types := map[string]engine.Managed{}
	for _, m := range result.Addresses {
		types[m.Address] = m
	}
	for _, m := range result.Moves {
		had, ok := types[m.To]
		if !ok {
			had = engine.Managed{Type: resourceTypeOf(m.To)}
		}
		r.report.Moved = append(r.report.Moved, report.Move{Type: had.Type, ID: had.ID, FromRoot: path, From: m.From, ToRoot: path, To: m.To})
	}
	for _, rt := range result.Retyped {
		r.report.Moved = append(r.report.Moved, report.Move{Type: types[rt.To].Type, ID: rt.ID, FromRoot: path, From: rt.From, ToRoot: path, To: rt.To})
	}
	for _, removal := range result.Removed {
		for _, d := range removal.Resources {
			r.report.Departed = append(r.report.Departed, report.Move{Type: d.Type, ID: d.ID, FromRoot: path, From: d.Address, ToRoot: d.Root})
		}
	}
	for _, a := range result.Arrived {
		r.report.AddArrival(report.Move{Type: a.HadType(), ID: a.ID, FromRoot: a.Root, From: a.From, ToRoot: path, To: a.To})
	}
}
