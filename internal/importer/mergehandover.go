// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// mergedStacks returns the directories of the merged roots the merge
// checkpoints under out record, sorted.
func mergedStacks(out string) ([]string, error) {
	base := filepath.Join(out, CheckpointDir, "merge")
	var stacks []string
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		stacks = append(stacks, filepath.Join(out, strings.TrimSuffix(rel, ".json")))
		return nil
	})
	sort.Strings(stacks)
	return stacks, err
}

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
func planMergedHandovers(out string, h *handovers, callOf map[string]*listedCall, assigned map[engine.External]string, stays map[engine.External]string) error {
	stacks, err := mergedStacks(out)
	if err != nil {
		return err
	}
	for _, stack := range stacks {
		c, err := readMergeCheckpoint(out, stack)
		if err != nil {
			log.Printf("%s: %v; what that merged root has isn't handed over to other roots (--regenerate)", relativePath(out, stack), err)
			continue
		}
		if c == nil || !c.Merge.Merged {
			continue
		}
		names, err := tfFiles(stack)
		if err != nil {
			return err
		}
		if len(names) == 0 {
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
		var leaving []mergedLeaving
		for _, env := range slices.Sorted(maps.Keys(c.Imports)) {
			ids := c.Imports[env]
			for _, address := range slices.Sorted(maps.Keys(ids)) {
				had := engine.Managed{Type: engine.AddressType(address), ID: ids[address], Address: address}
				now, to, ok := assignedAs(assigned, had)
				if !ok || slices.Contains(roots[env], to) {
					continue
				}
				if !generated(env) {
					log.Printf("%s: %s of %s goes to %s, but the run doesn't generate %s's root of the stack again, so it stays (--regenerate)", path, address, env, relativePath(out, to), env)
					stays[now] = path
					continue
				}
				leaving = append(leaving, mergedLeaving{env: env, departing: departing{had: had, to: to, now: now}})
			}
		}
		if len(leaving) == 0 {
			continue
		}
		removals, problems, err := mergedRemovals(out, stack, c.Imports, leaving)
		if err != nil {
			return err
		}
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
			arrival := engine.Arrival{Type: l.now.Type, ID: l.had.ID, Root: path, From: l.had.Address}
			if l.now.Type != l.had.Type {
				arrival.FromType = l.had.Type
			}
			h.arrivals[l.to] = append(h.arrivals[l.to], arrival)
		}
	}
	return nil
}

// mergedRemovals returns the removed blocks the merged root in stack, under
// out, gets for leaving, those of its resources that go to other roots
// now, imports being the import IDs of each environment's resources in
// it by address; or what keeps them from it. As the environments share
// its code, a removed block forgets what it takes in every environment's
// state: it takes a whole resource, or module call, only where every
// environment that has it hands it over. Else, where every environment
// that has the instance that goes at that address hands it over, a moved
// block moves it to a resource of the root alone, named departed_, first,
// for a removed block to take that (see engine.Removal.Instance): another
// environment's state has nothing at that address for the moved block to
// move. Never one in a module call, as moved blocks may not take it out
// of a module package, nor one its moves.tf still moves.
func mergedRemovals(out, stack string, imports map[string]map[string]string, leaving []mergedLeaving) ([]engine.Removal, []string, error) {
	moved, err := engine.ReadMoves(stack)
	if err != nil {
		return nil, nil, err
	}
	resources, instances := map[string]int{}, map[string]int{}
	var addresses []string
	for _, ids := range imports {
		for address := range ids {
			resources[engine.RemovedFrom(address)]++
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
		from := engine.RemovedFrom(l.had.Address)
		byFrom[from] = append(byFrom[from], l)
	}
	departure := func(l mergedLeaving) engine.Departure {
		return engine.Departure{Type: l.had.Type, ID: l.had.ID, Address: l.had.Address, Root: relativePath(out, l.to)}
	}
	var removals []engine.Removal
	var problems []string
	for _, from := range sortedDirs(byFrom) {
		going := byFrom[from]
		if slices.ContainsFunc(moved, func(m engine.Move) bool {
			return engine.RemovedFrom(m.From) == from || engine.RemovedFrom(m.To) == from
		}) {
			problems = append(problems, fmt.Sprintf("%s goes to another root, and %s still moves it: plan and apply every environment, and delete %s, before generating it again", from, engine.MovesFileName, engine.MovesFileName))
			continue
		}
		if len(going) == resources[from] {
			removal := engine.Removal{From: from}
			for _, l := range going {
				removal.Resources = append(removal.Resources, departure(l))
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
				removal := engine.Removal{From: engine.DepartedAddress(address, taken), Instance: address}
				for _, l := range at {
					removal.Resources = append(removal.Resources, departure(l))
				}
				removals = append(removals, removal)
			}
		}
	}
	return removals, problems, nil
}

// plannedRemoval is a removed block planned for a merged root (see
// planMergedHandovers), with the handovers of the scope that planned it,
// which say whether the roots its resources go to imported them.
type plannedRemoval struct {
	engine.Removal
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
func mergedHandovers(out, stack string, members []mergeMember, now map[string]map[string]string) ([]engine.Removal, []report.IncompleteHandover, string) {
	held := map[engine.External]string{}
	for env, ids := range now {
		for address, id := range ids {
			held[engine.External{Type: engine.AddressType(address), ID: id}] = env
		}
	}
	path := relativePath(out, stack)
	var removals []engine.Removal
	var incomplete []report.IncompleteHandover
	var missing []string
	for _, p := range plannedRemovals(stack, members) {
		kept := ""
		for _, d := range p.Resources {
			if env, ok := held[engine.External{Type: d.Type, ID: d.ID}]; ok {
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
			log.Printf("warning: the handover of %s (%s) from %s to %s isn't finished: %s", i.From, i.ID, i.FromRoot, i.ToRoot, i.Warning)
			r.report.IncompleteHandovers = append(r.report.IncompleteHandovers, i)
		}
	}
}

// recordMergedMoves records, for the merged root at path, relative to the
// output directory, merged again, what has another address now, where
// before and now are the import IDs of each environment's resources in
// it by address, and what removed, its removed blocks, hand over to
// other roots (see report.Move).
func (r *Run) recordMergedMoves(path string, before, now map[string]map[string]string, removed []engine.Removal) {
	for _, env := range slices.Sorted(maps.Keys(before)) {
		at := map[string]string{}
		for address, id := range now[env] {
			at[id] = address
		}
		for _, address := range slices.Sorted(maps.Keys(before[env])) {
			id := before[env][address]
			if to, ok := at[id]; ok && to != address {
				r.report.Moved = append(r.report.Moved, report.Move{Type: engine.AddressType(to), ID: id, FromRoot: path, From: address, ToRoot: path, To: to})
			}
		}
	}
	for _, removal := range removed {
		for _, d := range removal.Resources {
			r.report.Departed = append(r.report.Departed, report.Move{Type: d.Type, ID: d.ID, FromRoot: path, From: d.Address, ToRoot: d.Root})
		}
	}
}

// stillRemoved returns, of kept, the removed blocks of a merged root's
// moves.tf from before, not yet applied and deleted, those it keeps once
// merged again into now, the import IDs of each environment's resources
// in it by address: of what it no longer has, and removed, its new
// removed blocks, don't take. A removed block of what it has again would
// fail the moves check.
func stillRemoved(kept []engine.Removal, now map[string]map[string]string, removed []engine.Removal) []engine.Removal {
	taken := map[string]bool{}
	for _, ids := range now {
		for address := range ids {
			taken[engine.RemovedFrom(address)] = true
		}
	}
	for _, r := range removed {
		taken[r.From] = true
	}
	var still []engine.Removal
	for _, r := range kept {
		if !taken[r.From] {
			still = append(still, r)
		}
	}
	return still
}
