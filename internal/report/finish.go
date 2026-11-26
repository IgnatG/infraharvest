// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// AddDiscovered counts n resources listed in scope (see Coverage.Scopes).
// Finish counts what became of them from the directories and exclusions
// that name the scope.
func (r *Report) AddDiscovered(scope string, n int) {
	for i := range r.Scopes {
		if r.Scopes[i].Scope == scope {
			r.Scopes[i].Discovered += n
			return
		}
	}
	r.Scopes = append(r.Scopes, ScopeCount{Scope: scope, Discovered: n})
}

// AddListed records a resource the listers found, for the counts by tag:
// Finish counts what became of it from the directories and exclusions.
func (r *Report) AddListed(l Listed) {
	r.listed = append(r.listed, l)
}

// Merge adds what other, the report of another part of the same import
// (such as one account of several), recorded before Finish: its
// directories, exclusions, failures, discovered counts and listings by scope, and its
// manifest if r has none yet. Finish then counts the whole.
func (r *Report) Merge(other *Report) {
	r.Add(other.Manifest)
	r.Directories = append(r.Directories, other.Directories...)
	r.Excluded = append(r.Excluded, other.Excluded...)
	r.NotInCloud = append(r.NotInCloud, other.NotInCloud...)
	r.Moved = append(r.Moved, other.Moved...)
	r.Departed = append(r.Departed, other.Departed...)
	r.IncompleteHandovers = append(r.IncompleteHandovers, other.IncompleteHandovers...)
	r.KeptRoots = append(r.KeptRoots, other.KeptRoots...)
	r.Merges = append(r.Merges, other.Merges...)
	r.CloudFormationStacks.Add(other.CloudFormationStacks)
	r.Findings = append(r.Findings, other.Findings...)
	r.arrived = append(r.arrived, other.arrived...)
	r.Failures = append(r.Failures, other.Failures...)
	r.Warnings = append(r.Warnings, other.Warnings...)
	r.listed = append(r.listed, other.listed...)
	if len(r.tagKeys) == 0 {
		r.tagKeys = other.tagKeys
	}
	for _, s := range other.Scopes {
		r.AddDiscovered(s.Scope, s.Discovered)
	}
	for _, l := range other.Listings {
		r.AddListing(l)
	}
}

// Finish sorts the report, counts it, and sets the exit code. discovered
// counts what the listers found, by type; failed counts resources in
// directories that failed, by type.
func (r *Report) Finish(discovered, failed map[string]int, allowPartial bool) {
	r.SchemaVersion = SchemaVersion
	r.sortEntries()
	byType, byScope := r.countCoverage(discovered, failed)
	r.setTotals(byType, byScope)
	r.Tags = r.countByTag()
	if len(r.CloudFormationStacks) > 0 {
		r.CloudFormationStacks.Finish(r.results())
	}

	switch {
	case !r.incomplete():
		r.ExitCode = ExitOK
	case allowPartial:
		r.ExitCode = ExitPartial
	default:
		r.ExitCode = ExitIncomplete
	}
}

// sortEntries sorts the report's lists and drops their duplicates; it also
// sets where each departure arrives and the service of each finding.
func (r *Report) sortEntries() {
	sort.Slice(r.Directories, func(i, j int) bool { return r.Directories[i].Path < r.Directories[j].Path })
	sort.Slice(r.Skipped, func(i, j int) bool { return r.Skipped[i].Type < r.Skipped[j].Type })
	sort.Slice(r.Excluded, func(i, j int) bool {
		if r.Excluded[i].Type != r.Excluded[j].Type {
			return r.Excluded[i].Type < r.Excluded[j].Type
		}
		return r.Excluded[i].ID < r.Excluded[j].ID
	})
	// Calls of one account and region that list the same service look
	// in the same roots.
	sort.Slice(r.NotInCloud, func(i, j int) bool {
		a, b := r.NotInCloud[i], r.NotInCloud[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Address != b.Address {
			return a.Address < b.Address
		}
		return a.ID < b.ID
	})
	r.NotInCloud = slices.Compact(r.NotInCloud)
	r.matchArrivals()
	sortMoves(r.Moved)
	sortMoves(r.Departed)
	r.Moved = slices.Compact(r.Moved)
	r.Departed = slices.Compact(r.Departed)
	sort.Slice(r.IncompleteHandovers, func(i, j int) bool {
		a, b := r.IncompleteHandovers[i], r.IncompleteHandovers[j]
		if a.FromRoot != b.FromRoot {
			return a.FromRoot < b.FromRoot
		}
		return a.From < b.From
	})
	sort.Strings(r.Failures)
	sort.Strings(r.Warnings)
	r.Warnings = slices.Compact(r.Warnings)
	r.KeptRoots = mergeKeptRoots(r.KeptRoots)
	r.listFindings()
	sort.Slice(r.Listings, func(i, j int) bool { return r.Listings[i].Scope < r.Listings[j].Scope })
	sort.Slice(r.Merges, func(i, j int) bool { return r.Merges[i].Path < r.Merges[j].Path })
}

// countCoverage counts the report, by type and by account and region:
// discovered counts what the listers found, by type, and failed resources
// in directories that failed, by type. It sorts each directory's imported
// resources and those left out.
func (r *Report) countCoverage(discovered, failed map[string]int) (byType, byScope map[string]*CoverageTotal) {
	byType = map[string]*CoverageTotal{}
	count := func(t string) *CoverageTotal {
		if byType[t] == nil {
			byType[t] = &CoverageTotal{}
		}
		return byType[t]
	}
	for t, n := range discovered {
		count(t).Discovered += n
	}
	for t, n := range failed {
		count(t).Failed += n
	}
	for _, s := range r.Skipped {
		count(s.Type).Skipped += s.Count
	}
	byScope = map[string]*CoverageTotal{}
	for _, s := range r.Scopes {
		byScope[s.Scope] = &CoverageTotal{Discovered: s.Discovered}
	}
	inScope := func(scope string, add func(*CoverageTotal)) {
		if c, ok := byScope[scope]; ok {
			add(c)
		}
	}
	for _, e := range r.Excluded {
		count(e.Type).exclude(e.ManagedBy())
		inScope(e.Scope, func(c *CoverageTotal) { c.exclude(e.ManagedBy()) })
	}
	for i := range r.Directories {
		d := &r.Directories[i]
		sort.Slice(d.Imported, func(a, b int) bool { return d.Imported[a].Address < d.Imported[b].Address })
		sort.Slice(d.LeftOut, func(a, b int) bool { return d.LeftOut[a].Address < d.LeftOut[b].Address })
		for _, res := range d.Imported {
			count(engine.AddressType(res.Address)).Imported++
		}
		for _, res := range d.LeftOut {
			count(engine.AddressType(res.Address)).LeftOut++
		}
		inScope(d.Scope, func(c *CoverageTotal) {
			c.Imported += len(d.Imported)
			c.LeftOut += len(d.LeftOut)
		})
	}
	return byType, byScope
}

// setTotals sets the counts by type, sorted, the totals, and the counts by
// account and region, sorted.
func (r *Report) setTotals(byType, byScope map[string]*CoverageTotal) {
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Strings(types)
	r.Types = make([]TypeCount, 0, len(types))
	r.Totals = CoverageTotal{}
	for _, t := range types {
		c := *byType[t]
		r.Types = append(r.Types, TypeCount{Type: t, CoverageTotal: c})
		r.Totals.Discovered += c.Discovered
		r.Totals.Imported += c.Imported
		r.Totals.LeftOut += c.LeftOut
		r.Totals.Skipped += c.Skipped
		r.Totals.Excluded += c.Excluded
		r.Totals.Failed += c.Failed
		r.Totals.Managed += c.Managed
		r.Totals.OtherTool += c.OtherTool
		r.Totals.Cloud += c.Cloud
	}
	for i := range r.Scopes {
		r.Scopes[i].CoverageTotal = *byScope[r.Scopes[i].Scope]
	}
	sort.Slice(r.Scopes, func(i, j int) bool { return r.Scopes[i].Scope < r.Scopes[j].Scope })
}

// listFindings sets on each finding about a resource the service that
// listed it, as the listing recorded (see AddListed), and sorts them.
func (r *Report) listFindings() {
	type key struct{ scope, typ, id string }
	services := map[key]string{}
	for _, l := range r.listed {
		if l.Service != "" {
			services[key{l.Scope, l.Type, l.ID}] = l.Service
		}
	}
	for i, f := range r.Findings {
		if f.ID != "" && f.Service == "" {
			r.Findings[i].Service = services[key{f.Scope, f.Type, f.ID}]
		}
	}
	sortFindings(r.Findings)
	r.Findings = slices.Compact(r.Findings)
}

// matchArrivals sets on each departure the address the root it goes to
// imports it at.
func (r *Report) matchArrivals() {
	key := func(m Move) Move { return Move{Type: m.Type, ID: m.ID, FromRoot: m.FromRoot, ToRoot: m.ToRoot} }
	departed := map[Move]int{}
	for i, d := range r.Departed {
		departed[key(d)] = i
	}
	for _, a := range r.arrived {
		if i, ok := departed[key(a)]; ok {
			r.Departed[i].To = a.To
		}
	}
	r.arrived = nil
}

// sortMoves sorts moves by the root and address they come from.
func sortMoves(moves []Move) {
	sort.Slice(moves, func(i, j int) bool {
		a, b := moves[i], moves[j]
		if a.FromRoot != b.FromRoot {
			return a.FromRoot < b.FromRoot
		}
		if a.From != b.From {
			return a.From < b.From
		}
		return a.ID < b.ID
	})
}

// countByTag counts the listed resources by the value of each of tagKeys:
// imported or left out, as the directories say; excluded, and why, as the
// exclusions say; failed otherwise, as in a directory that failed.
func (r *Report) countByTag() []TagCount {
	if len(r.tagKeys) == 0 {
		return nil
	}
	key := func(scope, typ, id string) string { return scope + "\n" + typ + " " + id }
	imported, leftOut := map[string]bool{}, map[string]bool{}
	for _, d := range r.Directories {
		for _, res := range d.Imported {
			imported[key(d.Scope, engine.AddressType(res.Address), res.ID)] = true
		}
		for _, res := range d.LeftOut {
			leftOut[key(d.Scope, engine.AddressType(res.Address), res.ID)] = true
		}
	}
	excluded := map[string]string{}
	for _, e := range r.Excluded {
		excluded[key(e.Scope, e.Type, e.ID)] = e.ManagedBy()
	}
	var counts []TagCount
	for _, tagKey := range r.tagKeys {
		byValue := map[string]*CoverageTotal{}
		for _, l := range r.listed {
			value := l.Tags[tagKey]
			c := byValue[value]
			if c == nil {
				c = &CoverageTotal{}
				byValue[value] = c
			}
			c.Discovered++
			k := key(l.Scope, l.Type, l.ID)
			owner, isExcluded := excluded[k]
			switch {
			case imported[k]:
				c.Imported++
			case leftOut[k]:
				c.LeftOut++
			case isExcluded:
				c.exclude(owner)
			default:
				c.Failed++
			}
		}
		values := make([]string, 0, len(byValue))
		for v := range byValue {
			values = append(values, v)
		}
		sort.Strings(values)
		for _, v := range values {
			counts = append(counts, TagCount{Key: tagKey, Value: v, CoverageTotal: *byValue[v]})
		}
	}
	return counts
}

// incomplete reports whether something listed wasn't imported, other than
// types infraharvest doesn't import, or a directory failed a check.
func (r *Report) incomplete() bool {
	return len(r.Failures) > 0 || r.Totals.LeftOut > 0 || r.Totals.Failed > 0 || len(r.FailedChecks()) > 0
}

// FailedChecks lists the failed checks as "directory: check: details".
func (r *Report) FailedChecks() []string {
	var failed []string
	for _, d := range r.Directories {
		for _, c := range d.Checks {
			if !c.Passed {
				failed = append(failed, fmt.Sprintf("%s: %s: %s", d.Path, c.Name, strings.Join(c.Details, "; ")))
			}
		}
	}
	return failed
}

// SkippedChecks lists the checks that couldn't run, such as the scanners
// check with no scanner installed, as "directory: check: details".
func (r *Report) SkippedChecks() []string {
	var skipped []string
	for _, d := range r.Directories {
		for _, c := range d.Checks {
			if c.Passed && c.Skipped {
				skipped = append(skipped, fmt.Sprintf("%s: %s: %s", d.Path, c.Name, strings.Join(c.Details, "; ")))
			}
		}
	}
	return skipped
}
