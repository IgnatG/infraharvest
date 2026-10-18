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

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// handovers are what a run with --regenerate hands over between the roots
// it generates (see planHandovers), by root directory: the removed blocks
// a root gets for the resources it had that now go to other roots, the
// resources a root imports that other roots hand over, and the roots left
// as they were, with why.
type handovers struct {
	removals map[string][]engine.Removal
	arrivals map[string][]engine.Arrival
	refused  map[string]error
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
// in another root, at to.
type departing struct {
	had engine.Managed
	to  string
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
// A root of the run whose resources a removed block can't hand over,
// as it takes no instance keys, is left as it was and reported as
// failed, and they are left out of the roots they would go to too.
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
				key := engine.External{Type: imp.Type, ID: imp.ID}
				if _, ok := assigned[key]; !ok {
					assigned[key] = filepath.Clean(dir)
				}
			}
		}
	}
	gone, err := departures(out, assigned)
	if err != nil {
		return err
	}
	h := &handovers{removals: map[string][]engine.Removal{}, arrivals: map[string][]engine.Arrival{}, refused: map[string]error{}}
	// What stays in the root it was in, by type and import ID, with that
	// root relative to out.
	stays := map[engine.External]string{}
	problems := map[string][]string{}
	for _, dir := range sortedDirs(gone) {
		leaving := gone[dir]
		if _, inRun := callOf[dir]; !inRun {
			previous, err := previousAddresses(out, dir)
			if err != nil {
				return err
			}
			if len(leaving) < len(previous) {
				for _, d := range leaving {
					stays[engine.External{Type: d.had.Type, ID: d.had.ID}] = relativePath(out, dir)
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
		removals, why, err := handOver(out, dir, leaving)
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
			stays[engine.External{Type: d.had.Type, ID: d.had.ID}] = relativePath(out, dir)
		}
	}
	for _, dir := range sortedDirs(h.removals) {
		root := relativePath(out, dir)
		for _, d := range gone[dir] {
			log.Printf("%s: %s goes to %s, and a removed block hands it over (--regenerate)", root, d.had.Address, relativePath(out, d.to))
			h.arrivals[d.to] = append(h.arrivals[d.to], engine.Arrival{Type: d.had.Type, ID: d.had.ID, Root: root, From: d.had.Address})
		}
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
			if root, ok := stays[engine.External{Type: imp.Type, ID: imp.ID}]; ok {
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
// them (see previousAddresses), that assigned, the root of the run each
// resource goes to by type and import ID, puts in another root now. Only
// roots that still have configuration count, applied ones too, with
// their import blocks deleted; a root whose import didn't finish counts
// only while it is generated again (see checkpoint.Pending). A checkpoint
// that can't be read is logged and passed over.
func departures(out string, assigned map[engine.External]string) (map[string][]departing, error) {
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
		previous, err := previousAddresses(out, dir)
		if err != nil {
			return nil, err
		}
		for _, p := range previous {
			if to, ok := assigned[engine.External{Type: p.Type, ID: p.ID}]; ok && to != dir {
				gone[dir] = append(gone[dir], departing{had: p, to: to})
			}
		}
		sort.Slice(gone[dir], func(i, j int) bool { return gone[dir][i].had.Address < gone[dir][j].had.Address })
	}
	return gone, nil
}

// handOver returns the removed blocks the root in dir, an earlier
// import's, gets for leaving, those of its resources that go to other
// roots now, or what keeps them from it: a removed block takes a whole
// resource, with no instance key, or a whole module call, so all of it
// must go; and a moved block of its moves.tf that a plan may not have
// applied yet mustn't move it, nor an address only inferred name it.
func handOver(out, dir string, leaving []departing) ([]engine.Removal, []string, error) {
	previous, err := previousAddresses(out, dir)
	if err != nil {
		return nil, nil, err
	}
	moved, err := pendingMoves(out, dir)
	if err != nil {
		return nil, nil, err
	}
	members := map[string]int{}
	for _, p := range previous {
		members[engine.RemovedFrom(p.Address)]++
	}
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
		switch {
		case problem != "":
		case len(going) < members[from] && strings.HasPrefix(from, "module."):
			problem = fmt.Sprintf("only part of %s goes to %s, and a removed block takes the whole module call", from, where)
		case len(going) < members[from]:
			problem = fmt.Sprintf("only some instances of %s go to %s, and a removed block takes no instance keys", from, where)
		}
		for _, m := range moved {
			if problem == "" && (engine.RemovedFrom(m.From) == from || engine.RemovedFrom(m.To) == from) {
				problem = fmt.Sprintf("%s goes to %s, and %s still moves it: plan and apply the root, and delete %s, before generating it again", from, where, engine.MovesFileName, engine.MovesFileName)
			}
		}
		if problem != "" {
			problems = append(problems, problem)
			continue
		}
		removals = append(removals, removal)
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
// the output directory, moves within it, hands over to other roots, and
// imports that other roots hand over (see report.Move).
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
	for _, removal := range result.Removed {
		for _, d := range removal.Resources {
			r.report.Departed = append(r.report.Departed, report.Move{Type: d.Type, ID: d.ID, FromRoot: path, From: d.Address, ToRoot: d.Root})
		}
	}
	for _, a := range result.Arrived {
		r.report.AddArrival(report.Move{Type: a.Type, ID: a.ID, FromRoot: a.Root, From: a.From, ToRoot: path, To: a.To})
	}
}
