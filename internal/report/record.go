// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// An import records what it does as it goes, root by root and resource by
// resource, with the methods in this file, and Finish then sorts and
// counts the whole. They own the text people read, such as why a resource
// was left out, so that every caller says it the same way.

// The reasons an incremental import gives for what it leaves out, as a
// root of the output has it (see InRoot and InOtherRoot).
const (
	reasonInRoot      = "already in the root"
	reasonInOtherRoot = "already in another root of the output"
)

// skippedReason is why resources of a type are skipped (see AddSkipped).
const skippedReason = "Terraform can't import this resource type"

// Why is why an import left a resource out (see Exclude): what it says,
// for people, and what manages the resource, one of the Owner constants.
type Why struct {
	reason, owner string
}

// InRoot is why an incremental import leaves out a resource the root it
// adds to has already.
func InRoot() Why {
	return Why{reason: reasonInRoot, owner: OwnerTerraformRoot}
}

// InOtherRoot is why an import leaves out a resource that root, another
// root of the output, has already, as an earlier import put it there.
func InOtherRoot(root string) Why {
	return Why{reason: reasonInOtherRoot + ", " + root, owner: OwnerTerraformRoot}
}

// Decided is why the selection left out a resource, as its decision says:
// what manages the resource is what reason says (see OwnerOf).
func Decided(reason string) Why {
	return Why{reason: reason, owner: OwnerOf(reason)}
}

// Owned is why an import leaves out a resource whose tags say owner, one
// of the Owner constants, manages it, with reason saying which tag.
func Owned(reason, owner string) Why {
	return Why{reason: reason, owner: owner}
}

// Exclude records that the import left out the resource of typ with import
// ID id, listed in scope, and why.
func (r *Report) Exclude(scope, typ, id string, why Why) {
	r.Excluded = append(r.Excluded, Excluded{Type: typ, ID: id, Reason: why.reason, Scope: scope, Owner: why.owner})
}

// AddFailure records what couldn't be imported, or couldn't run.
func (r *Report) AddFailure(err error) {
	r.Failures = append(r.Failures, err.Error())
}

// AddWarning records w, about how the import ran, unless it has already.
// It reports whether w is new.
func (r *Report) AddWarning(w string) bool {
	if slices.Contains(r.Warnings, w) {
		return false
	}
	r.Warnings = append(r.Warnings, w)
	return true
}

// AddSkipped records n resources of typ that Terraform can't import.
func (r *Report) AddSkipped(typ string, n int) {
	r.Skipped = append(r.Skipped, Skipped{Type: typ, Count: n, Reason: skippedReason})
}

// CountByTags makes Finish count by the value of each of keys (see
// Coverage.Tags).
func (r *Report) CountByTags(keys []string) {
	r.tagKeys = keys
}

// AddKeptRoots records roots earlier imports generated that keep their
// directories (see KeptRoot).
func (r *Report) AddKeptRoots(kept []KeptRoot) {
	r.KeptRoots = append(r.KeptRoots, kept...)
}

// LockProvider records the version of the provider the dependency lock
// file lock records. The lock is the source of truth: where it doesn't
// record the provider, as where it is missing, doesn't parse or holds
// other providers only, no version is recorded, and one recorded before
// goes, rather than show a release no root's lock file pins, which
// Terraform wouldn't install. That loses nothing a call found: a run's
// lock is the first lock file it wrote, kept for the rest of it, so a
// later call of the run records the same version again, and the report
// of a call with a run of its own, merged into the run's, keeps the
// first version either has (see Manifest.Add).
func (r *Report) LockProvider(lock []byte) {
	r.Provider.Version = engine.LockedVersion(lock, r.Provider.Source)
}

// SetExitCode records the exit code of an import that ended with an error,
// which Finish can't tell from what the report records.
func (r *Report) SetExitCode(code int) {
	r.ExitCode = code
}

// AddFailedDirectory records the root at path, relative to the output
// directory, with forward slashes, whose resources, listed in scope,
// weren't imported, as err says.
func (r *Report) AddFailedDirectory(path, scope string, err error) {
	r.Directories = append(r.Directories, Directory{Path: path, Scope: scope, Error: err.Error()})
}

// AddDirectory records the root at path, relative to the output directory,
// with forward slashes, whose resources, listed in scope, Generate left as
// result: what it imports, at its address in the root, in the module call
// Generate moved it into if any; what it left out; its module calls,
// secrets, warnings and checks; and what it moves, within it and between
// roots (see Move).
func (r *Report) AddDirectory(path, scope string, result *engine.Result) {
	d := Directory{Path: path, Scope: scope, Warnings: result.Warnings}
	// A result without addresses, as in a checkpoint an older release
	// wrote, has only the labels Generate gave them.
	at := map[engine.External]string{}
	for _, m := range result.Addresses {
		at[engine.ResourceKey(m.Type, m.ID)] = m.Address
	}
	d.Imported = make([]Resource, 0, len(result.Imported))
	for _, imp := range result.Imported {
		address, ok := at[engine.ResourceKey(imp.Type, imp.ID)]
		if !ok {
			address = imp.Type + "." + imp.Name
		}
		d.Imported = append(d.Imported, Resource{Address: address, ID: imp.ID})
	}
	for _, rej := range result.Rejected {
		d.LeftOut = append(d.LeftOut, LeftOut{Address: rej.Address, ID: rej.ID, Errors: rej.Errors})
	}
	for _, m := range result.Modules {
		d.Modules = append(d.Modules, Module{Name: m.Name, Source: m.Source, Version: m.Version, Resources: m.Resources, Declined: m.Declined})
	}
	for _, s := range result.Secrets {
		d.Secrets = append(d.Secrets, Secret{Variable: s.Variable, Address: s.Address, Attribute: s.Attribute})
	}
	for _, c := range result.Gate {
		d.Checks = append(d.Checks, Check{Name: c.Name, Passed: c.Passed, Skipped: c.Skipped, Details: c.Details})
	}
	r.addFindings(path, scope, result)
	r.addMoves(path, result)
	r.Directories = append(r.Directories, d)
}

// addFindings records what the scanners of result's gate, of the root at
// path, found, each about the resource the root imports at the address
// it names, if any (see engine.ResourceAt), with its type and import ID;
// the root's resources were listed in scope.
func (r *Report) addFindings(path, scope string, result *engine.Result) {
	managed := result.Addresses
	if len(managed) == 0 {
		managed = engine.InferredAddresses(result)
	}
	for _, f := range GateFindings(path, "", managed, result.Gate) {
		f.Scope = scope
		r.Findings = append(r.Findings, f)
	}
}

// GateFindings returns what the scanners of gate found in the root at
// root, as env plans it for a root environments share, each about the
// resource of managed, what the root imports, at the address it names, if
// any (see engine.ResourceAt), with its type and import ID.
func GateFindings(root, env string, managed []engine.Managed, gate engine.Gate) []Finding {
	var findings []Finding
	for _, c := range gate {
		for _, f := range c.Findings {
			finding := Finding{Root: root, Environment: env, Address: f.Resource, Scanner: f.Scanner, Rule: f.Rule, Severity: f.Severity, Location: f.Location, Message: f.Message}
			if m, ok := engine.ResourceAt(managed, f.Resource); ok {
				finding.Address, finding.Type, finding.ID = m.Address, m.Type, m.ID
			}
			findings = append(findings, finding)
		}
	}
	return findings
}

// addMoves records what result, of the root at path, moves within it, as
// moved blocks do or, for a resource of another name of its type now,
// removed and import blocks (see engine.Retype), hands over to other
// roots, and imports that other roots hand over, which Finish matches
// with what they record handed over.
func (r *Report) addMoves(path string, result *engine.Result) {
	types := map[string]engine.Managed{}
	for _, m := range result.Addresses {
		types[m.Address] = m
	}
	for _, m := range result.Moves {
		had, ok := types[m.To]
		if !ok {
			had = engine.Managed{Type: engine.AddressType(m.To)}
		}
		r.Moved = append(r.Moved, Move{Type: had.Type, ID: had.ID, FromRoot: path, From: m.From, ToRoot: path, To: m.To})
	}
	for _, rt := range result.Retyped {
		r.Moved = append(r.Moved, Move{Type: types[rt.To].Type, ID: rt.ID, FromRoot: path, From: rt.From, ToRoot: path, To: rt.To})
	}
	r.depart(path, result.Removed)
	for _, a := range result.Arrived {
		r.arrived = append(r.arrived, Move{Type: a.HadType(), ID: a.ID, FromRoot: a.Root, From: a.From, ToRoot: path, To: a.To})
	}
}

// depart records what removed, the removed blocks of the root at path,
// hand over to other roots.
func (r *Report) depart(path string, removed []engine.Removal) {
	for _, removal := range removed {
		for _, d := range removal.Resources {
			r.Departed = append(r.Departed, Move{Type: d.Type, ID: d.ID, FromRoot: path, From: d.Address, ToRoot: d.Root})
		}
	}
}

// AddMergedMoves records, for the merged root at path, relative to the
// output directory, merged again, what has another address now, where
// before and now are the import IDs of each environment's resources in it
// by address, and what removed, its removed blocks, hand over to other
// roots.
func (r *Report) AddMergedMoves(path string, before, now map[string]map[string]string, removed []engine.Removal) {
	for _, env := range slices.Sorted(maps.Keys(before)) {
		at := map[string]string{}
		for address, id := range now[env] {
			at[id] = address
		}
		for _, address := range slices.Sorted(maps.Keys(before[env])) {
			id := before[env][address]
			if to, ok := at[id]; ok && to != address {
				r.Moved = append(r.Moved, Move{Type: engine.AddressType(to), ID: id, FromRoot: path, From: address, ToRoot: path, To: to})
			}
		}
	}
	r.depart(path, removed)
}

// AddIncompleteHandover records a handover one of its roots didn't finish.
func (r *Report) AddIncompleteHandover(i IncompleteHandover) {
	r.IncompleteHandovers = append(r.IncompleteHandovers, i)
}

// AddNotInCloud records gone, the resources of the root at path, relative
// to the output directory, that the listing of scope no longer finds in
// the cloud.
func (r *Report) AddNotInCloud(path, scope string, gone []engine.Import) {
	for _, g := range gone {
		r.NotInCloud = append(r.NotInCloud, NotInCloud{Path: path, Address: g.Type + "." + g.Name, ID: g.ID, Scope: scope})
	}
}

// NotInCloudAt returns the addresses of what the root at path, relative to
// the output directory, has that is no longer in the cloud (see
// AddNotInCloud), sorted, but for what ReaddressNotInCloud gave a merged
// root.
func (r *Report) NotInCloudAt(path string) []string {
	var gone []string
	for _, g := range r.NotInCloud {
		if g.Path == path && g.Environment == "" {
			gone = append(gone, g.Address)
		}
	}
	sort.Strings(gone)
	return gone
}

// ReaddressNotInCloud makes what the roots of envOf, their paths relative
// to the output directory with the environment of each, have that is no
// longer in the cloud the merged root's at path, of each root's
// environment: at its address there, as imports, the import IDs of each
// environment's resources in it by address, has it. It returns them, by
// environment, with their addresses in the merged root.
func (r *Report) ReaddressNotInCloud(path string, envOf map[string]string, imports map[string]map[string]string) map[string][]engine.Import {
	gone := map[string][]engine.Import{}
	for i, g := range r.NotInCloud {
		env, ok := envOf[g.Path]
		if !ok {
			continue
		}
		g.Path, g.Environment = path, env
		for _, address := range slices.Sorted(maps.Keys(imports[env])) {
			if strings.EqualFold(imports[env][address], g.ID) {
				g.Address = address
				break
			}
		}
		r.NotInCloud[i] = g
		typ := engine.AddressType(g.Address)
		gone[env] = append(gone[env], engine.Import{Type: typ, Name: strings.TrimPrefix(g.Address, typ+"."), ID: g.ID})
	}
	return gone
}

// AddMerge records m, the outcome of a stack whose environments' roots are
// at roots, relative to the output directory: once merged, each root's
// directory names the merged root, and the merged root's findings
// (Merge.Findings) are the report's, which coverage.json lists once,
// with the others.
func (r *Report) AddMerge(m Merge, roots []string) {
	findings := m.Findings
	m.Findings = nil
	r.Merges = append(r.Merges, m)
	if !m.Merged {
		return
	}
	scopes := map[string]string{}
	for i, d := range r.Directories {
		if slices.Contains(roots, d.Path) {
			r.Directories[i].MergedInto = m.Path
			scopes[d.Path] = d.Scope
		}
	}
	// The merged root's findings replace those of the roots it merges,
	// which are set aside; each environment's resources were listed where
	// its root's were.
	r.Findings = slices.DeleteFunc(r.Findings, func(f Finding) bool { return slices.Contains(roots, f.Root) })
	for _, f := range findings {
		for _, root := range m.Roots {
			if root.Env == f.Environment {
				f.Scope = scopes[root.Path]
			}
		}
		r.Findings = append(r.Findings, f)
	}
}

// SetApart records, on the stack whose merged root is at path, recorded
// last, the roots of environments that keep roots of their own, and why
// (see Merge.Apart).
func (r *Report) SetApart(path string, apart []MergedRoot, reason string) {
	for i := len(r.Merges) - 1; i >= 0; i-- {
		if m := &r.Merges[i]; m.Path == path {
			m.Apart, m.ApartReason = apart, reason
			return
		}
	}
}
