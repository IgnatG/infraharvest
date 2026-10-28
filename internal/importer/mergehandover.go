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
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/envmerge"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/report"
)

// mergedLeaving is a resource of a merged root that the run puts in
// another root now: env is the environment it is of, and had says where
// the merged root has it (see departing).
type mergedLeaving struct {
	env string
	departing
}

// planMergedHandovers works out, for planHandovers, which resources of the
// merged roots environments share go to another root of the run now, by
// assigned, the root each resource goes to by type and import ID: those
// of an environment whose root of the stack, set aside (see premergeDir),
// the run generates again, as callOf says, that go to a root other than
// that one, such as with a changed environment tag. Each gets a removed
// block in the merged root's moves.tf once its stack merges again (see
// mergedHandovers), recorded in h, and the root it goes to imports it.
// As the environments share the merged root's code, a removed block
// forgets a resource in every environment's state (see mergedRemovals).
// Where none can take what an environment hands over, that environment's
// roots of the stack the run generates are left as they were, reported as
// failed, and what they would hand over stays in the merged root, left
// out of the roots it would go to, as stays records; so does what an
// environment whose root the run doesn't generate again would hand over.
// But where no root of the run merges into the merged root, such as with
// a changed --layout, and every resource an environment has in it goes
// to roots of the run, the root each goes to imports it, and h records it
// as vacating the merged root: once every environment's have gone, the
// merge phase writes the merged root with removed blocks alone (see
// vacateMergedRoots), as a root of its own whose every resource goes is
// (see engine.Vacate).
func planMergedHandovers(x *outputIndex, h *handovers, callOf map[string]*listedCall, assigned map[engine.External]string, stays map[engine.External]string) error {
	out := x.out
	stacks, err := x.mergedStacks()
	if err != nil {
		return err
	}
	// What the command imports, as the calls of the scope share it.
	var s *shared
	scope := ""
	for _, c := range callOf {
		if c.run != nil {
			s = c.run.shared
		}
		scope = c.scope
		break
	}
	for _, merged := range stacks {
		stack := merged.dir
		c, err := merged.mergeCheckpoint()
		if err != nil {
			log.Printf("%s: %v; what that merged root has isn't handed over to other roots (--regenerate)", relativePath(out, stack), err)
			continue
		}
		if c == nil || !c.Merge.Merged {
			continue
		}
		has, err := merged.configured()
		if err != nil {
			return err
		}
		if !has {
			continue
		}
		path := relativePath(out, stack)
		// Each environment's roots of the stack.
		roots := map[string][]string{}
		for _, m := range c.Members {
			roots[m.Env] = append(roots[m.Env], filepath.Clean(filepath.Join(out, filepath.FromSlash(m.Path))))
		}
		generated := func(env string) bool {
			return slices.ContainsFunc(roots[env], func(dir string) bool { _, ok := callOf[dir]; return ok })
		}
		// An environment whose roots of the stack the run doesn't generate,
		// all of whose resources go to roots of the run, vacates it, where
		// no root of the run merges into it, the command imports every
		// environment's account, subscription or project, and its moves.tf
		// moves nothing that a plan may not have applied yet.
		vacatable := !mergesInto(callOf, stack) && importsEnvironments(s, scope, c.Members)
		moved, err := merged.movesTF()
		if err != nil {
			return err
		}
		var leaving []mergedLeaving
		for _, env := range slices.Sorted(maps.Keys(c.Imports)) {
			ids := c.Imports[env]
			var apart []mergedLeaving
			for _, address := range slices.Sorted(maps.Keys(ids)) {
				had := engine.Managed{Type: engine.AddressType(address), ID: ids[address], Address: address}
				now, to, ok := assignedAs(assigned, had)
				if !ok || slices.Contains(roots[env], to) {
					continue
				}
				l := mergedLeaving{env: env, departing: departing{had: had, to: to, now: now}}
				if !generated(env) {
					apart = append(apart, l)
					continue
				}
				leaving = append(leaving, l)
			}
			goes := vacatable && len(apart) > 0 && len(apart) == len(ids) && !slices.ContainsFunc(apart, func(l mergedLeaving) bool { return l.to == filepath.Clean(stack) })
			if goes && len(moved) > 0 {
				log.Printf("%s: every resource of %s goes to other roots, but %s still moves resources: plan and apply every environment, and delete %s, before generating it again (--regenerate)", path, env, rootfile.MovesFileName, rootfile.MovesFileName)
				goes = false
			}
			if goes {
				h.vacating[filepath.Clean(stack)] = append(h.vacating[filepath.Clean(stack)], apart...)
				for _, l := range apart {
					log.Printf("%s: %s of %s goes to %s, as does every resource of %s there, and a removed block of the merged root hands it over (--regenerate)", path, l.had.Address, env, relativePath(out, l.to), env)
					h.arrivals[l.to] = append(h.arrivals[l.to], l.arrival(path))
				}
				continue
			}
			for _, l := range apart {
				log.Printf("%s: %s of %s goes to %s, but the run doesn't generate %s's root of the stack again, so it stays (--regenerate)", path, l.had.Address, env, relativePath(out, l.to), env)
				stays[l.now] = path
			}
		}
		if len(leaving) == 0 {
			continue
		}
		removals, problems := mergedRemovals(out, moved, c.Imports, leaving)
		if len(problems) > 0 {
			var envs []string
			for _, l := range leaving {
				stays[l.now] = path
				if !slices.Contains(envs, l.env) {
					envs = append(envs, l.env)
				}
			}
			for _, env := range envs {
				for _, dir := range roots[env] {
					if _, ok := callOf[dir]; ok {
						h.refused[dir] = fmt.Errorf("generating %s again would hand resources of %s over to other roots where no removed block of the merged root %s can take them, as its environments share its code, so it is left as it was, and they stay in the merged root: %s", dir, env, path, strings.Join(problems, "; "))
					}
				}
			}
			continue
		}
		h.merged[filepath.Clean(stack)] = removals
		for _, l := range leaving {
			log.Printf("%s: %s of %s goes to %s, and a removed block of the merged root hands it over (--regenerate)", path, l.had.Address, l.env, relativePath(out, l.to))
			h.arrivals[l.to] = append(h.arrivals[l.to], l.arrival(path))
		}
	}
	return nil
}

// arrival is how the root l goes to imports it, from the merged root at
// path, relative to the output directory.
func (l mergedLeaving) arrival(path string) rootfile.Arrival {
	arrival := rootfile.Arrival{Type: l.now.Type, ID: l.had.ID, Root: path, From: l.had.Address}
	if l.now.Type != l.had.Type {
		arrival.FromType = l.had.Type
	}
	return arrival
}

// departure is l as a removed block of the merged root under out hands
// it over.
func (l mergedLeaving) departure(out string) rootfile.Departure {
	return rootfile.Departure{Type: l.had.Type, ID: l.had.ID, Address: l.had.Address, Root: relativePath(out, l.to)}
}

// mergesInto reports whether a root of one of the calls of callOf merges
// into the merged root in stack (see listedCall.places).
func mergesInto(callOf map[string]*listedCall, stack string) bool {
	for _, c := range callOf {
		for _, p := range c.places {
			if filepath.Clean(p.stack) == filepath.Clean(stack) {
				return true
			}
		}
	}
	return false
}

// mergedRemovals returns the removed blocks a merged root, under out,
// gets for leaving, those of its resources that go to other roots now,
// imports being the import IDs of each environment's resources in it by
// address, and moved the moved blocks of its moves.tf; or what keeps
// them from it. As the environments share
// its code, a removed block forgets what it takes in every environment's
// state: it takes a whole resource, or module call, only where every
// environment that has it hands it over. Else, where every environment
// that has the instance that goes at that address hands it over, a moved
// block moves it to a resource of the root alone, named departed_, first,
// for a removed block to take that (see rootfile.Removal.Instance): another
// environment's state has nothing at that address for the moved block to
// move. Never one in a module call, as moved blocks may not take it out
// of a module package, nor one its moves.tf still moves.
func mergedRemovals(out string, moved []rootfile.Move, imports map[string]map[string]string, leaving []mergedLeaving) ([]rootfile.Removal, []string) {
	resources, instances := map[string]int{}, map[string]int{}
	var addresses []string
	for _, ids := range imports {
		for address := range ids {
			resources[rootfile.RemovedFrom(address)]++
			instances[address]++
			addresses = append(addresses, address)
		}
	}
	for _, m := range moved {
		addresses = append(addresses, m.From, m.To)
	}
	taken := engine.RootResources(addresses...)
	byFrom := map[string][]mergedLeaving{}
	for _, l := range leaving {
		from := rootfile.RemovedFrom(l.had.Address)
		byFrom[from] = append(byFrom[from], l)
	}
	var removals []rootfile.Removal
	var problems []string
	for _, from := range sortedDirs(byFrom) {
		going := byFrom[from]
		if slices.ContainsFunc(moved, func(m rootfile.Move) bool {
			return rootfile.RemovedFrom(m.From) == from || rootfile.RemovedFrom(m.To) == from
		}) {
			problems = append(problems, fmt.Sprintf("%s goes to another root, and %s still moves it: plan and apply every environment, and delete %s, before generating it again", from, rootfile.MovesFileName, rootfile.MovesFileName))
			continue
		}
		if len(going) == resources[from] {
			removal := rootfile.Removal{From: from}
			for _, l := range going {
				removal.Resources = append(removal.Resources, l.departure(out))
			}
			removals = append(removals, removal)
			continue
		}
		byAddress := map[string][]mergedLeaving{}
		for _, l := range going {
			byAddress[l.had.Address] = append(byAddress[l.had.Address], l)
		}
		for _, address := range sortedDirs(byAddress) {
			at := byAddress[address]
			switch {
			case strings.HasPrefix(address, "module."):
				problems = append(problems, fmt.Sprintf("only part of %s goes to another root, and a moved block can't take %s out of the module call for a removed block to take it alone", from, address))
			case len(at) < instances[address]:
				problems = append(problems, fmt.Sprintf("%s of %s goes to %s, but another environment keeps a resource at that address, which a removed block would make its state forget too", address, at[0].env, relativePath(out, at[0].to)))
			default:
				removal := rootfile.Removal{From: engine.DepartedAddress(address, taken), Instance: address}
				for _, l := range at {
					removal.Resources = append(removal.Resources, l.departure(out))
				}
				removals = append(removals, removal)
			}
		}
	}
	return removals, problems
}

// plannedRemoval is a removed block planned for a merged root (see
// planMergedHandovers), with the handovers of the scope that planned it,
// which say whether the roots its resources go to imported them.
type plannedRemoval struct {
	rootfile.Removal
	h *handovers
}

// plannedRemovals returns the removed blocks planned for the merged root
// in stack by the scopes of members, the stack's roots.
func plannedRemovals(stack string, members []mergeMember) []plannedRemoval {
	var seen []*handovers
	var planned []plannedRemoval
	for _, m := range members {
		if m.handovers == nil || slices.Contains(seen, m.handovers) {
			continue
		}
		seen = append(seen, m.handovers)
		for _, r := range m.handovers.merged[filepath.Clean(stack)] {
			planned = append(planned, plannedRemoval{Removal: r, h: m.handovers})
		}
	}
	return planned
}

// mergedHandovers returns the removed blocks of the merged root in stack,
// under out, merged again into now, the import IDs of each environment's
// resources in it by address: those planned for it by the scopes of
// members (see planMergedHandovers). A removed block of a resource now
// still has, as the root of its environment wasn't generated again, isn't
// written, and its handover is incomplete: the root it goes to imports
// it, while the merged root keeps it. It returns why the merged root
// can't merge again, where a root a resource goes to didn't import it: a
// plan of the merged root, which no longer has it, would destroy it.
func mergedHandovers(out, stack string, members []mergeMember, now map[string]map[string]string) ([]rootfile.Removal, []report.IncompleteHandover, string) {
	held := map[engine.External]string{}
	for env, ids := range now {
		for address, id := range ids {
			held[engine.External{Type: engine.AddressType(address), ID: id}] = env
		}
	}
	path := relativePath(out, stack)
	var removals []rootfile.Removal
	var incomplete []report.IncompleteHandover
	var missing []string
	for _, p := range plannedRemovals(stack, members) {
		kept := ""
		for _, d := range p.Resources {
			if env, ok := held[engine.ResourceKey(d.Type, d.ID)]; ok {
				kept = env
			}
		}
		if kept != "" {
			for _, d := range p.Resources {
				if arrivalOf(p.h.generated[p.h.dirOf(d.Root)], d) != nil {
					incomplete = append(incomplete, report.IncompleteHandover{Move: report.Move{Type: d.Type, ID: d.ID, FromRoot: path, From: d.Address, ToRoot: d.Root}, FailedRoot: path, Warning: fmt.Sprintf("%s imports it, but the merged root still has it, as the root of %s wasn't generated again: generate it again with --regenerate before applying either root", d.Root, kept)})
				}
			}
			continue
		}
		for _, d := range p.Resources {
			if arrivalOf(p.h.generated[p.h.dirOf(d.Root)], d) == nil {
				missing = append(missing, fmt.Sprintf("%s goes to %s, which didn't import it", d.Address, d.Root))
			}
		}
		removals = append(removals, p.Removal)
	}
	if len(missing) > 0 {
		return nil, nil, "merging it again would hand resources over to roots that didn't import them, and each environment's plan would destroy them: " + strings.Join(missing, "; ")
	}
	return removals, incomplete, ""
}

// warnMergedIncomplete records, as incomplete, each handover planned for
// the merged root in stack by the scopes of members (see
// planMergedHandovers) whose root imported what it goes to, as the
// merged root stays as it was, why says, and still has it.
func (r *Run) warnMergedIncomplete(stack string, members []mergeMember, why string) {
	path := relativePath(r.options.Out, stack)
	for _, p := range plannedRemovals(stack, members) {
		for _, d := range p.Resources {
			if arrivalOf(p.h.generated[p.h.dirOf(d.Root)], d) == nil {
				continue
			}
			i := report.IncompleteHandover{Move: report.Move{Type: d.Type, ID: d.ID, FromRoot: path, From: d.Address, ToRoot: d.Root}, FailedRoot: path, Warning: fmt.Sprintf("%s imports it, but the merged root stays as it was, and each environment's state there still has it (%s): generate both again with --regenerate before applying either root", d.Root, why)}
			r.recordIncomplete(i)
		}
	}
}

// stillRemoved returns, of kept, the removed blocks of a merged root's
// moves.tf from before, not yet applied and deleted, those it keeps once
// merged again into now, the import IDs of each environment's resources
// in it by address: of what it no longer has, and removed, its new
// removed blocks, don't take. A removed block of what it has again would
// fail the moves check.
func stillRemoved(kept []rootfile.Removal, now map[string]map[string]string, removed []rootfile.Removal) []rootfile.Removal {
	taken := map[string]bool{}
	for _, ids := range now {
		for address := range ids {
			taken[rootfile.RemovedFrom(address)] = true
		}
	}
	for _, r := range removed {
		taken[r.From] = true
	}
	var still []rootfile.Removal
	for _, r := range kept {
		if !taken[r.From] {
			still = append(still, r)
		}
	}
	return still
}

// plannedLeaving is what leaves a merged root none of whose environments'
// roots the run generates (see handovers.vacating), with the handovers of
// the scope that planned it, which say whether the root it goes to
// imported it.
type plannedLeaving struct {
	mergedLeaving
	h *handovers
}

// vacateMergedRoots vacates each merged root that the scopes of the run
// planned to (see planMergedHandovers), but those of merging, the stacks
// the run merges, by the merged root's directory (see vacateMerged).
func (r *Run) vacateMergedRoots(merging map[string][]mergeMember) error {
	leaving := map[string][]plannedLeaving{}
	for _, h := range r.vacating {
		for stack, ls := range h.vacating {
			for _, l := range ls {
				leaving[stack] = append(leaving[stack], plannedLeaving{mergedLeaving: l, h: h})
			}
		}
	}
	merged := map[string]bool{}
	for stack := range merging {
		merged[filepath.Clean(stack)] = true
	}
	for _, stack := range sortedDirs(leaving) {
		if err := r.vacateMerged(stack, leaving[stack], merged[stack]); err != nil {
			return err
		}
	}
	return nil
}

// vacateMerged writes the merged root in stack with the removed blocks of
// leaving alone, what its environments have that goes to other roots of
// the run (see envmerge.VacateMerged): once every environment's every
// resource in it goes, and the roots they go to imported them. Else, or
// where merging says the run merges roots into it, it stays as it was,
// the run fails, saying why, and the handovers of what the roots it goes
// to imported are reported as incomplete: both roots have it.
func (r *Run) vacateMerged(stack string, leaving []plannedLeaving, merging bool) error {
	out := r.options.Out
	path := relativePath(out, stack)
	c, err := mergeCheckpoints.read(out, stack)
	if err != nil {
		return err
	}
	if c == nil {
		return nil
	}
	going := map[string]map[string]bool{}
	for _, l := range leaving {
		if going[l.env] == nil {
			going[l.env] = map[string]bool{}
		}
		going[l.env][l.had.Address] = true
	}
	var why string
	var staying, missing []string
	for _, env := range slices.Sorted(maps.Keys(c.Imports)) {
		for _, address := range slices.Sorted(maps.Keys(c.Imports[env])) {
			if !going[env][address] {
				staying = append(staying, fmt.Sprintf("%s of %s", address, env))
			}
		}
	}
	for _, l := range leaving {
		d := l.departure(out)
		if arrivalOf(l.h.generated[l.h.dirOf(d.Root)], d) == nil {
			missing = append(missing, fmt.Sprintf("%s of %s goes to %s, which didn't import it", d.Address, l.env, d.Root))
		}
	}
	var removals []rootfile.Removal
	switch {
	case merging:
		why = "the run merges roots into it again"
	case len(staying) > 0:
		why = "the run doesn't hand over all it has, as it doesn't list " + strings.Join(staying, ", ") + " for other roots"
	case len(missing) > 0:
		why = strings.Join(missing, "; ")
	default:
		var all []mergedLeaving
		for _, l := range leaving {
			all = append(all, l.mergedLeaving)
		}
		moved, err := rootfile.ReadMoves(stack)
		if err != nil {
			return err
		}
		var problems []string
		removals, problems = mergedRemovals(out, moved, c.Imports, all)
		why = strings.Join(problems, "; ")
	}
	if why == "" {
		why, err = r.writeVacated(stack, c, removals)
		if err != nil {
			return err
		}
	}
	if why != "" {
		for _, l := range leaving {
			d := l.departure(out)
			if arrivalOf(l.h.generated[l.h.dirOf(d.Root)], d) == nil {
				continue
			}
			i := report.IncompleteHandover{Move: report.Move{Type: d.Type, ID: d.ID, FromRoot: path, From: d.Address, ToRoot: d.Root}, FailedRoot: path, Warning: fmt.Sprintf("%s imports it, but the merged root stays as it was, and each environment's state there still has it (%s): generate both again with --regenerate before applying either root", d.Root, why)}
			r.recordIncomplete(i)
		}
		err := fmt.Errorf("%s: every resource it has goes to other roots, but it can't hand them over, so it stays as it was: %s", path, why)
		log.Print(err)
		r.recordFailure(r.options, err)
		return nil
	}
	log.Printf("%s: every resource it has goes to other roots, so it keeps only the removed blocks that hand them over: apply it as each environment before the roots they go to (--regenerate)", path)
	r.report.AddMergedMoves(path, nil, nil, removals)
	return nil
}

// writeVacated writes the merged root in stack, of which c is the merge
// checkpoint, with removals alone, the removed blocks of all it has, and
// those of its moves.tf from before, not yet applied and deleted (see
// envmerge.VacateMerged), and records it in c: it has nothing now. It
// returns why it can't, where it can't, and leaves the merged root as it
// was.
func (r *Run) writeVacated(stack string, c *mergeCheckpoint, removals []rootfile.Removal) (string, error) {
	kept, err := rootfile.ReadRemoved(stack)
	if err != nil {
		return "", err
	}
	files := c.Files
	if len(files) == 0 {
		if files, err = envmerge.MergedRootFiles(stack); err != nil {
			return "", err
		}
	}
	backup, err := backupTree(stack)
	if err != nil {
		return "", err
	}
	restore := func() error {
		if _, had := backup.files[rootfile.MovesFileName]; !had {
			if err := os.Remove(filepath.Join(stack, rootfile.MovesFileName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return backup.restore()
	}
	left, check, err := r.tools.vacateMerged(stack, files, append(removals, stillRemoved(kept, nil, removals)...))
	if err != nil {
		return err.Error(), restore()
	}
	if !check.Passed {
		return "the removed blocks fail the moves check: " + strings.Join(check.Details, "; "), restore()
	}
	c.Files, c.Imports = left, nil
	return "", mergeCheckpoints.write(r.options.Out, stack, *c)
}

// importsEnvironments reports whether the command imports the account,
// subscription or project of each of members, the roots of a merged
// root's environments its checkpoint records: one of the scopes it names
// (see shared.scopes), with s, or, where it names none, scope, the one it
// imports. Only then can all the merged root has go to other roots: an
// environment the command doesn't import keeps its resources there.
func importsEnvironments(s *shared, scope string, members []mergeCheckpointMember) bool {
	for _, m := range members {
		switch {
		case m.Account == "":
			return false
		case s != nil && len(s.scopes) > 0:
			if !slices.ContainsFunc(s.scopes, func(named string) bool { return strings.EqualFold(named, m.Account) }) {
				return false
			}
		case !strings.EqualFold(m.Account, scopeAccount(scope)):
			return false
		}
	}
	return len(members) > 0
}
