// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package report describes what an import did: what it found, what it
// imported, what it left out and why, and the versions it used. It writes
// the report directory of the output (coverage.json, manifest.json and
// report.md) and the JSON of --format json.
//
// Reports hold no timestamps or absolute paths, so an import of an
// unchanged estate produces the same report.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// SchemaVersion is the version of the JSON documents. It changes only
// when a field is removed or changes meaning.
const SchemaVersion = 1

// Exit codes of an import.
const (
	// ExitOK: everything listed was imported.
	ExitOK = 0
	// ExitIncomplete: something couldn't be imported and --allow-partial
	// isn't set.
	ExitIncomplete = 1
	// ExitCouldNotRun: the import couldn't run, for example without
	// credentials or a Terraform binary.
	ExitCouldNotRun = 2
	// ExitPartial: something couldn't be imported and --allow-partial is
	// set; the output has the rest.
	ExitPartial = 3
	// ExitDrift: infraharvest drift --detailed-exitcode found drift (see
	// Drift.DriftFound).
	ExitDrift = 4
	// ExitDriftRejected: infraharvest drift --detailed-exitcode accepted a
	// change whose edit the plan of the edited code rejected (see
	// DriftTotals.RejectedByPlan).
	ExitDriftRejected = 1
	// ExitInterrupted: the import was interrupted (SIGINT, as by Ctrl-C).
	// Like the codes a shell gives a process a signal ends, it is 128
	// plus the signal's number.
	ExitInterrupted = 130
	// ExitTerminated: the import was terminated (SIGTERM).
	ExitTerminated = 143
)

// Dir is the directory of the output the report files go into.
const Dir = "report"

// Report is what one import did.
type Report struct {
	SchemaVersion int `json:"schema_version"`
	Manifest
	Coverage
	ExitCode int `json:"exit_code"`
	// TagKeys are the tag keys Finish counts by (see Coverage.Tags).
	TagKeys []string `json:"-"`
	// listed are the resources the listers found, with their tags, for
	// the counts by tag (see AddListed).
	listed []Listed
	// arrived are the resources roots import that other roots hand over,
	// which Finish matches with Departed (see AddArrival).
	arrived []Move
}

// Manifest records the versions an import used.
type Manifest struct {
	Tool     Component `json:"tool"`
	Engine   Component `json:"engine"`
	Provider Provider  `json:"provider"`
	// Modules are the curated modules with a newer release than the one
	// their calls use: the one their adapters are tested with.
	Modules []ModuleVersion `json:"modules,omitempty"`
	// Unused are the curated modules the import doesn't call: they accept
	// only an older major release of the provider than its newest, so
	// their resources stay in the root, on the newest provider.
	Unused []UnusedModule `json:"unused_modules,omitempty"`
}

// Add adds the manifest of another Import call of the same run, which
// may have selected other modules, to m: the first manifest added is
// taken whole; after it, the newer and unused modules of either count,
// once each, and the provider of a call that held it back wins, so that
// the reason is shown.
func (m *Manifest) Add(other Manifest) {
	if m.Tool.Name == "" {
		*m = other
		return
	}
	if m.Provider.HeldBack == "" && other.Provider.HeldBack != "" {
		version := m.Provider.Version
		m.Provider = other.Provider
		if m.Provider.Version == "" {
			m.Provider.Version = version
		}
	}
	if m.Provider.Version == "" {
		m.Provider.Version = other.Provider.Version
	}
	for _, mv := range other.Modules {
		if !slices.ContainsFunc(m.Modules, func(have ModuleVersion) bool { return have.Source == mv.Source && have.Version == mv.Version }) {
			m.Modules = append(m.Modules, mv)
		}
	}
	for _, u := range other.Unused {
		if !slices.Contains(m.Unused, u) {
			m.Unused = append(m.Unused, u)
		}
	}
}

// UnusedModule is a module version the import doesn't call: Accepts is
// its constraint on the provider, which admits only older major releases.
type UnusedModule struct {
	Source  string `json:"source"`
	Version string `json:"version"`
	Accepts string `json:"accepts"`
}

// ModuleVersion is a module the import calls at Version while the registry
// has Latest.
type ModuleVersion struct {
	Source  string `json:"source"`
	Version string `json:"version"`
	Latest  string `json:"latest"`
}

// Component is a program and its version.
type Component struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Provider is the provider the generated configuration requires.
type Provider struct {
	Source     string `json:"source"`
	Constraint string `json:"constraint"`
	// Version is the version the lock file records, if any directory got
	// as far as terraform init.
	Version string `json:"version,omitempty"`
	// HeldBack says which module versions keep the constraint below the
	// provider's newest release, if any do.
	HeldBack string `json:"held_back,omitempty"`
}

// Coverage records what an import found and what became of it.
type Coverage struct {
	Types       []TypeCount `json:"types"`
	Directories []Directory `json:"directories"`
	Skipped     []Skipped   `json:"skipped,omitempty"`
	Excluded    []Excluded  `json:"excluded,omitempty"`
	Failures    []string    `json:"failures,omitempty"`
	// Warnings are about how the import ran that people should look at,
	// such as environments it couldn't apply; a directory's are its own.
	Warnings []string      `json:"warnings,omitempty"`
	Totals   CoverageTotal `json:"totals"`
	// NotInCloud are the resources of roots an incremental import added to
	// that its listing didn't find, though it listed everything of their
	// type where they were: they have been deleted since.
	NotInCloud []NotInCloud `json:"not_in_cloud,omitempty"`
	// Moved are the resources that roots generated again with --regenerate
	// move to other addresses with moved blocks, and Departed those they
	// hand over to other roots with removed blocks, which import them.
	Moved    []Move `json:"moved,omitempty"`
	Departed []Move `json:"departed,omitempty"`
	// IncompleteHandovers are the resources handed over between roots
	// whose handover one of the roots didn't finish, as it failed.
	IncompleteHandovers []IncompleteHandover `json:"incomplete_handovers,omitempty"`
	// KeptRoots are the roots earlier imports generated that keep their
	// directories, and the resources in them, though the configuration
	// file's environments would put those under envs/<env>/ now.
	KeptRoots []KeptRoot `json:"kept_roots,omitempty"`
	// Merges are the stacks whose environments' roots were merged into a
	// root they share, or kept apart, with why.
	Merges []Merge `json:"merges,omitempty"`
	// Scopes count by provider, account and region (see AddDiscovered),
	// such as aws/123456789012/eu-west-2.
	Scopes []ScopeCount `json:"scopes,omitempty"`
	// Tags count by the value of each of the tag keys asked for (see
	// Report.TagKeys), "" for resources without the tag.
	Tags []TagCount `json:"tags,omitempty"`
	// CloudFormationStacks are the CloudFormation stacks the import is
	// scoped to, and those nested in them, with what became of their
	// resources and whether each is safe to delete (AWS --cfn-stack).
	CloudFormationStacks []CloudFormationStack `json:"cloudformation_stacks,omitempty"`
}

// TagCount counts the resources with one value of a tag key.
type TagCount struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	CoverageTotal
}

// Listed is a resource the listers found that Terraform can import: where,
// its type and import ID, and its tags.
type Listed struct {
	Scope, Type, ID string
	Tags            map[string]string
}

// ScopeCount counts the resources of one scope.
type ScopeCount struct {
	Scope string `json:"scope"`
	CoverageTotal
}

// TypeCount counts the resources of one type.
type TypeCount struct {
	Type string `json:"type"`
	CoverageTotal
}

// CoverageTotal counts resources by outcome. Discovered is everything the
// listers found; each one was imported, left out, skipped, or lost to a
// failure.
type CoverageTotal struct {
	Discovered int `json:"discovered"`
	Imported   int `json:"imported"`
	LeftOut    int `json:"left_out"`
	Skipped    int `json:"skipped"`
	Excluded   int `json:"excluded"`
	Failed     int `json:"failed"`
	// Managed counts the excluded resources Terraform already manages.
	Managed int `json:"managed,omitempty"`
	// OtherTool counts the excluded resources another tool manages, such as
	// CloudFormation (see OtherToolPrefix).
	OtherTool int `json:"other_tool,omitempty"`
	// Cloud counts the excluded resources the default selection leaves out
	// for another reason, mostly as the cloud creates and manages them
	// itself, such as a default VPC (see OwnerCloud).
	Cloud int `json:"cloud,omitempty"`
}

// exclude counts an excluded resource, and what manages it by reason (see
// OwnerOf).
func (c *CoverageTotal) exclude(reason string) {
	c.Excluded++
	switch OwnerOf(reason) {
	case OwnerTerraformState, OwnerTerraformTag:
		c.Managed++
	case OwnerCloudFormation, OwnerOtherTool:
		c.OtherTool++
	case OwnerCloud:
		c.Cloud++
	}
}

// NotManaged counts what was discovered that neither Terraform, another
// tool nor the cloud manages.
func (c CoverageTotal) NotManaged() int {
	return c.Discovered - c.Managed - c.OtherTool - c.Cloud
}

// Directory is one output directory.
type Directory struct {
	// Path is relative to the output directory, with forward slashes.
	Path string `json:"path"`
	// Scope is where its resources were listed (see Coverage.Scopes).
	Scope    string     `json:"scope,omitempty"`
	Imported []Resource `json:"imported"`
	LeftOut  []LeftOut  `json:"left_out,omitempty"`
	Secrets  []Secret   `json:"secrets,omitempty"`
	// Warnings are about what the configuration holds that people should
	// look at, such as a tag named after an AWS access key ID.
	Warnings []string `json:"warnings,omitempty"`
	// Modules are the directory's module calls, and the clusters of
	// resources that stayed in the root, with why.
	Modules []Module `json:"modules,omitempty"`
	// Checks are the verification gate's results.
	Checks []Check `json:"checks,omitempty"`
	// Error is why nothing in the directory was imported.
	Error string `json:"error,omitempty"`
	// MergedInto is the root, relative to the output directory, that the
	// directory's environment shares with the other environments of its
	// stack, which has its resources now (see Coverage.Merges).
	MergedInto string `json:"merged_into,omitempty"`
}

// Resource is an imported resource.
type Resource struct {
	Address string `json:"address"`
	ID      string `json:"id"`
}

// LeftOut is a resource Terraform couldn't import or generate valid
// configuration for.
type LeftOut struct {
	Address string   `json:"address"`
	ID      string   `json:"id"`
	Errors  []string `json:"errors"`
}

// Secret is a variable to set before planning.
type Secret struct {
	Variable  string `json:"variable"`
	Address   string `json:"address"`
	Attribute string `json:"attribute"`
}

// Module is a module call, by its name, the module's source and version
// and the addresses the resources had in the root. A declined cluster has
// no name, and why the resources stayed in the root.
type Module struct {
	Name      string   `json:"name,omitempty"`
	Source    string   `json:"source"`
	Version   string   `json:"version,omitempty"`
	Resources []string `json:"resources"`
	Declined  string   `json:"declined,omitempty"`
}

// Check is one check of the verification gate on a directory: formatted,
// valid, planning only imports, free of secrets, deterministic.
type Check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	// Skipped checks couldn't run, so they checked nothing; they count
	// as passed.
	Skipped bool     `json:"skipped,omitempty"`
	Details []string `json:"details,omitempty"`
}

// Excluded is a listed resource the selection left out.
type Excluded struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Scope  string `json:"scope,omitempty"`
}

// NotInCloud is a resource a root has, by its address in the root, that
// is no longer in the cloud: Path is the root's, relative to the output
// directory, with forward slashes, and Scope where it was looked for.
// In a root environments share, Environment is the environment whose
// resource it is.
type NotInCloud struct {
	Path        string `json:"path"`
	Address     string `json:"address"`
	ID          string `json:"id"`
	Scope       string `json:"scope,omitempty"`
	Environment string `json:"environment,omitempty"`
}

// Move is a resource a moved block moves within a root, or that a
// removed block of one root hands over to another, which imports it: its
// type and import ID, the root it was in and its address there, and the
// root it is in now and its address there. Roots are relative to the
// output directory, with forward slashes. To is empty for a resource
// handed over to a root that didn't import it.
type Move struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	FromRoot string `json:"from_root"`
	From     string `json:"from"`
	ToRoot   string `json:"to_root"`
	To       string `json:"to,omitempty"`
}

// IncompleteHandover is a resource handed over from one root to another
// whose handover one of them didn't finish, as it failed: FailedRoot is
// FromRoot, whose state still has the resource, which ToRoot imports, so
// both roots have it; or ToRoot, which didn't import it, so that applying
// FromRoot's removed block would leave it in neither. Warning says what
// to do.
type IncompleteHandover struct {
	Move
	FailedRoot string `json:"failed_root"`
	Warning    string `json:"warning"`
}

// KeptRoot is a root an earlier import generated that keeps its
// directory, as its state key comes from its path, though Environments,
// those of its resources, would lay them out elsewhere now. Path is
// relative to the output directory, with forward slashes, and Scope is
// where its resources were listed.
type KeptRoot struct {
	Path         string   `json:"path"`
	Environments []string `json:"environments"`
	Scope        string   `json:"scope,omitempty"`
}

// Merge is a stack: the roots of its environments, one each, which an
// import merged into one root they share (Path), with what differs
// between them in var files per environment, or kept apart, with why
// (Reason). Paths are relative to the output directory, with forward
// slashes.
type Merge struct {
	Path         string       `json:"path"`
	Environments []string     `json:"environments"`
	Roots        []MergedRoot `json:"roots"`
	// Merged says the environments share the root at Path; else Reason
	// says why each keeps its own.
	Merged bool   `json:"merged"`
	Reason string `json:"reason,omitempty"`
	// Shared counts the resources and module calls of the merged root
	// more than one environment has, and Residue are those it keeps as
	// one environment's root had them, with why.
	Shared  int            `json:"shared,omitempty"`
	Residue []MergeResidue `json:"residue,omitempty"`
	// Variables are the merged root's variables.
	Variables []string `json:"variables,omitempty"`
	// Checks are the merged root's gate, for each environment, whose
	// name each check's details start with.
	Checks []Check `json:"checks,omitempty"`
	// Unchanged says a resumed import kept the merged root as an earlier
	// one wrote it (--resume).
	Unchanged bool `json:"unchanged,omitempty"`
	// Unplanned are the environments the import didn't have, which merged
	// again as their roots were set aside, but weren't planned, without
	// their credentials: verify --environment plans them.
	Unplanned []string `json:"unplanned,omitempty"`
	// Apart are the roots of environments the merged root didn't have,
	// which the import brought, that keep roots of their own, as
	// ApartReason says: merging them in failed, or would have given what
	// the merged root has other addresses.
	Apart       []MergedRoot `json:"apart,omitempty"`
	ApartReason string       `json:"apart_reason,omitempty"`
}

// MergedRoot is the root of one environment of a stack.
type MergedRoot struct {
	Env  string `json:"env"`
	Path string `json:"path"`
}

// MergeResidue is a resource of one environment that a merged root keeps
// as the environment's root had it: Address is its address there, and
// MergedAddress in the merged root.
type MergeResidue struct {
	Env           string `json:"env"`
	Address       string `json:"address"`
	MergedAddress string `json:"merged_address"`
	Reason        string `json:"reason"`
}

// mergeKeptRoots returns kept sorted by path, with the environments of
// each root, of every call that kept it, together.
func mergeKeptRoots(kept []KeptRoot) []KeptRoot {
	if len(kept) == 0 {
		return kept
	}
	byPath := map[string]int{}
	var merged []KeptRoot
	for _, k := range kept {
		if i, ok := byPath[k.Path]; ok {
			for _, env := range k.Environments {
				if !slices.Contains(merged[i].Environments, env) {
					merged[i].Environments = append(merged[i].Environments, env)
				}
			}
			continue
		}
		k.Environments = slices.Clone(k.Environments)
		byPath[k.Path] = len(merged)
		merged = append(merged, k)
	}
	for i := range merged {
		slices.Sort(merged[i].Environments)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Path < merged[j].Path })
	return merged
}

// AddArrival records a resource a root imports that another root hands
// over: Finish sets the address it has there on the departure that
// matches it.
func (r *Report) AddArrival(m Move) {
	r.arrived = append(r.arrived, m)
}

// Skipped counts resources of a type infraharvest doesn't import.
type Skipped struct {
	Type   string `json:"type"`
	Count  int    `json:"count"`
	Reason string `json:"reason"`
}

// OtherToolPrefix starts the reason of an exclusion for a resource another
// infrastructure-as-code tool manages, such as "managed by CloudFormation
// stack app".
const OtherToolPrefix = "managed by "

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
// directories, exclusions, failures and discovered counts by scope, and its
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
	r.AddCloudFormationStacks(other.CloudFormationStacks)
	r.arrived = append(r.arrived, other.arrived...)
	r.Failures = append(r.Failures, other.Failures...)
	r.Warnings = append(r.Warnings, other.Warnings...)
	r.listed = append(r.listed, other.listed...)
	if len(r.TagKeys) == 0 {
		r.TagKeys = other.TagKeys
	}
	for _, s := range other.Scopes {
		r.AddDiscovered(s.Scope, s.Discovered)
	}
}

// Finish sorts the report, counts it, and sets the exit code. discovered
// counts what the listers found, by type; failed counts resources in
// directories that failed, by type.
func (r *Report) Finish(discovered, failed map[string]int, allowPartial bool) {
	r.SchemaVersion = SchemaVersion
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
	sort.Slice(r.Merges, func(i, j int) bool { return r.Merges[i].Path < r.Merges[j].Path })

	byType := map[string]*CoverageTotal{}
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
	byScope := map[string]*CoverageTotal{}
	for _, s := range r.Scopes {
		byScope[s.Scope] = &CoverageTotal{Discovered: s.Discovered}
	}
	inScope := func(scope string, add func(*CoverageTotal)) {
		if c, ok := byScope[scope]; ok {
			add(c)
		}
	}
	for _, e := range r.Excluded {
		count(e.Type).exclude(e.Reason)
		inScope(e.Scope, func(c *CoverageTotal) { c.exclude(e.Reason) })
	}
	for i := range r.Directories {
		d := &r.Directories[i]
		sort.Slice(d.Imported, func(a, b int) bool { return d.Imported[a].Address < d.Imported[b].Address })
		sort.Slice(d.LeftOut, func(a, b int) bool { return d.LeftOut[a].Address < d.LeftOut[b].Address })
		for _, res := range d.Imported {
			count(ResourceType(res.Address)).Imported++
		}
		for _, res := range d.LeftOut {
			count(ResourceType(res.Address)).LeftOut++
		}
		inScope(d.Scope, func(c *CoverageTotal) {
			c.Imported += len(d.Imported)
			c.LeftOut += len(d.LeftOut)
		})
	}
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
	r.Tags = r.countByTag()
	r.finishStacks()

	switch {
	case !r.Incomplete():
		r.ExitCode = ExitOK
	case allowPartial:
		r.ExitCode = ExitPartial
	default:
		r.ExitCode = ExitIncomplete
	}
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

// countByTag counts the listed resources by the value of each of TagKeys:
// imported or left out, as the directories say; excluded, and why, as the
// exclusions say; failed otherwise, as in a directory that failed.
func (r *Report) countByTag() []TagCount {
	if len(r.TagKeys) == 0 {
		return nil
	}
	key := func(scope, typ, id string) string { return scope + "\n" + typ + " " + id }
	imported, leftOut := map[string]bool{}, map[string]bool{}
	for _, d := range r.Directories {
		for _, res := range d.Imported {
			imported[key(d.Scope, ResourceType(res.Address), res.ID)] = true
		}
		for _, res := range d.LeftOut {
			leftOut[key(d.Scope, ResourceType(res.Address), res.ID)] = true
		}
	}
	excluded := map[string]string{}
	for _, e := range r.Excluded {
		excluded[key(e.Scope, e.Type, e.ID)] = e.Reason
	}
	var counts []TagCount
	for _, tagKey := range r.TagKeys {
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
			reason, isExcluded := excluded[k]
			switch {
			case imported[k]:
				c.Imported++
			case leftOut[k]:
				c.LeftOut++
			case isExcluded:
				c.exclude(reason)
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

// Incomplete reports whether something listed wasn't imported, other than
// types infraharvest doesn't import, or a directory failed a check.
func (r *Report) Incomplete() bool {
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

// ResourceType returns the type of the resource at address, in a module
// call or not: aws_iam_role for both aws_iam_role.app and
// module.app["a.b"].aws_iam_role.this.
func ResourceType(address string) string {
	for {
		rest, ok := strings.CutPrefix(address, "module.")
		if !ok {
			break
		}
		// Past the call's name and its key, if any, which may hold dots.
		i := strings.IndexAny(rest, ".[")
		if i < 0 {
			return address
		}
		if rest[i] == '[' {
			end := keyEnd(rest[i:])
			if end < 0 || i+end+1 >= len(rest) || rest[i+end+1] != '.' {
				return address
			}
			i += end + 1
		}
		address = rest[i+1:]
	}
	t, _, _ := strings.Cut(address, ".")
	return t
}

// keyEnd returns the index of the bracket that closes the key key starts
// with, such as ["a.b"] or [0], or -1 if none does.
func keyEnd(key string) int {
	quoted := false
	for i := 1; i < len(key); i++ {
		switch {
		case quoted && key[i] == '\\':
			i++
		case key[i] == '"':
			quoted = !quoted
		case !quoted && key[i] == ']':
			return i
		}
	}
	return -1
}

// WriteFiles writes coverage.json, manifest.json and report.md into the
// report directory under outputDir, and the templates of the CloudFormation
// stacks the import is scoped to under TemplatesDir.
func (r *Report) WriteFiles(outputDir string) error {
	dir := filepath.Join(outputDir, Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	coverage, err := marshal(struct {
		SchemaVersion int `json:"schema_version"`
		Coverage
		ExitCode int `json:"exit_code"`
	}{r.SchemaVersion, r.Coverage, r.ExitCode})
	if err != nil {
		return err
	}
	manifest, err := marshal(struct {
		SchemaVersion int `json:"schema_version"`
		Manifest
	}{r.SchemaVersion, r.Manifest})
	if err != nil {
		return err
	}
	for name, content := range map[string][]byte{
		"coverage.json": coverage,
		"manifest.json": manifest,
		"report.md":     []byte(r.Markdown()),
	} {
		if err := fsutil.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			return err
		}
	}
	return r.writeTemplates(outputDir)
}

// WriteJSON writes the whole report as one JSON document.
func (r *Report) WriteJSON(w io.Writer) error {
	content, err := marshal(r)
	if err != nil {
		return err
	}
	_, err = w.Write(content)
	return err
}

func marshal(v any) ([]byte, error) {
	content, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

// Markdown renders the report for people.
func (r *Report) Markdown() string {
	var b strings.Builder
	b.WriteString("# Import report\n\n")
	fmt.Fprintf(&b, "%s %s, %s %s, provider `%s` %s", r.Tool.Name, r.Tool.Version, r.Engine.Name, r.Engine.Version, r.Provider.Source, r.Provider.Constraint)
	if r.Provider.Version != "" {
		fmt.Fprintf(&b, " (%s)", r.Provider.Version)
	}
	b.WriteString(".\n\n")
	if r.Provider.HeldBack != "" {
		fmt.Fprintf(&b, "The provider is held back: %s.\n\n", r.Provider.HeldBack)
	}
	for _, m := range r.Modules {
		fmt.Fprintf(&b, "`%s` %s is available; the module calls use %s, the release its adapter is tested with (`--modules latest-untested` calls the newest).\n\n", m.Source, m.Latest, m.Version)
	}
	for _, m := range r.Unused {
		fmt.Fprintf(&b, "`%s` %s isn't called: it accepts the provider at `%s` only, not its newest major release, so its resources stay in the root.\n\n", m.Source, m.Version, m.Accepts)
	}

	t := r.Totals
	b.WriteString("| Discovered | Imported | Excluded | Left out | Not importable | Failed |\n|---|---|---|---|---|---|\n")
	fmt.Fprintf(&b, "| %d | %d | %d | %d | %d | %d |\n", t.Discovered, t.Imported, t.Excluded, t.LeftOut, t.Skipped, t.Failed)

	if len(r.Types) > 0 {
		b.WriteString("\n## By type\n\n| Type | Discovered | Imported | Excluded | Left out | Not importable | Failed |\n|---|---|---|---|---|---|---|\n")
		for _, c := range r.Types {
			fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %d | %d | %d |\n", c.Type, c.Discovered, c.Imported, c.Excluded, c.LeftOut, c.Skipped, c.Failed)
		}
	}
	if len(r.Types) > 0 {
		b.WriteString("\n## Managed and unmanaged\n\nWhat Terraform already manages, according to the state read with --managed-state or to the resources' tags, what other tools such as CloudFormation or Pulumi manage, and what the cloud manages itself, such as a default VPC, or the default selection leaves out for another reason: the import left those out (--include-owned imports those their tags say are managed). The rest isn't under infrastructure as code yet.\n\n")
		const header = "| Discovered | Managed by Terraform | Managed by another tool | Managed by the cloud | Not managed |"
		if len(r.Scopes) > 1 {
			b.WriteString("| Account and region " + header + "\n|---|---|---|---|---|---|\n")
			for _, s := range r.Scopes {
				fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %d | %d |\n", s.Scope, s.Discovered, s.Managed, s.OtherTool, s.Cloud, s.NotManaged())
			}
			b.WriteString("\n")
		}
		b.WriteString("| Type " + header + "\n|---|---|---|---|---|---|\n")
		for _, c := range r.Types {
			fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %d | %d |\n", c.Type, c.Discovered, c.Managed, c.OtherTool, c.Cloud, c.NotManaged())
		}
	}
	if len(r.Tags) > 0 {
		b.WriteString("\n## By tag\n\nWhat was discovered by the value of each tag asked for with --report-tags, and how much of it is under infrastructure as code.\n")
		key := ""
		for _, c := range r.Tags {
			if c.Key != key {
				key = c.Key
				fmt.Fprintf(&b, "\n| `%s` | Discovered | Imported | Managed by Terraform | Managed by another tool | Managed by the cloud | Not managed |\n|---|---|---|---|---|---|---|\n", key)
			}
			value := "(not tagged)"
			if c.Value != "" {
				value = "`" + c.Value + "`"
			}
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d |\n", value, c.Discovered, c.Imported, c.Managed, c.OtherTool, c.Cloud, c.NotManaged())
		}
	}

	var leftOut, secrets, warnings, modules, declined, dirErrors []string
	for _, w := range r.Warnings {
		warnings = append(warnings, "- "+w+".")
	}
	for _, d := range r.Directories {
		for _, m := range d.Modules {
			resources := "`" + strings.Join(m.Resources, "`, `") + "`"
			source := strings.TrimSpace("`" + m.Source + "` " + m.Version)
			if m.Declined != "" {
				declined = append(declined, fmt.Sprintf("- %s in `%s` (%s): %s", resources, d.Path, source, m.Declined))
			} else {
				modules = append(modules, fmt.Sprintf("- `module.%s` in `%s`: %s, for %s", m.Name, d.Path, source, resources))
			}
		}
		for _, l := range d.LeftOut {
			leftOut = append(leftOut, fmt.Sprintf("- `%s` in `%s`: %s", l.Address, d.Path, strings.Join(l.Errors, "; ")))
		}
		for _, s := range d.Secrets {
			secrets = append(secrets, fmt.Sprintf("- `%s` in `%s`: %s of `%s`", s.Variable, d.Path, s.Attribute, s.Address))
		}
		for _, w := range d.Warnings {
			warnings = append(warnings, fmt.Sprintf("- `%s`: %s.", d.Path, w))
		}
		if d.Error != "" {
			dirErrors = append(dirErrors, fmt.Sprintf("- `%s`: %s", d.Path, d.Error))
		}
	}
	section := func(title, intro string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n## %s\n\n%s\n\n%s\n", title, intro, strings.Join(lines, "\n"))
	}
	section("Left out", "Terraform couldn't import these resources or generate valid configuration for them. Each directory's `rejected.hcl` has their blocks and errors.", leftOut)
	section("Secrets to set", "Set these variables before planning, from your secret store, such as AWS Secrets Manager, Azure Key Vault or Google Secret Manager, or with `TF_VAR_<name>`: infraharvest never writes secret values into its output.", secrets)
	section("Warnings", "Look at these before using the configuration.", warnings)
	var gone []string
	for _, g := range r.NotInCloud {
		line := fmt.Sprintf("- `%s` `%s` in `%s`", g.Address, g.ID, g.Path)
		if g.Environment != "" {
			line += ", of " + g.Environment
		}
		gone = append(gone, line)
	}
	section("No longer in the cloud", "The roots have these resources, but the import didn't find them, though it listed every resource of their type where they were: they have been deleted since, and the next plan of the root would create them again. Delete each one's import block, if the root's `imports.tf` still has it, then either delete its resource block, and anything that refers to it, and plan; or, if it should exist, plan to create it again. The import changed nothing the roots have.", gone)
	var movedLines, departedLines []string
	for _, m := range r.Moved {
		movedLines = append(movedLines, fmt.Sprintf("- `%s` to `%s` in `%s`", m.From, m.To, m.FromRoot))
	}
	for _, d := range r.Departed {
		to := "`" + d.ToRoot + "`, which didn't import it"
		if d.To != "" {
			to = fmt.Sprintf("`%s` in `%s`", d.To, d.ToRoot)
		}
		departedLines = append(departedLines, fmt.Sprintf("- `%s` in `%s` to %s", d.From, d.FromRoot, to))
	}
	section("Moved", "Generated again, these roots have these resources at other addresses: their moved blocks, in `moves.tf`, move them in state, so their next plans destroy nothing.", movedLines)
	section("Handed over", "These resources go to other roots now. The removed blocks in `moves.tf` of the roots they were in make those roots' states forget them, without destroying them, and the roots they go to import them. Apply the root each was in first, then the root it goes to.", departedLines)
	var incompleteLines []string
	for _, h := range r.IncompleteHandovers {
		incompleteLines = append(incompleteLines, fmt.Sprintf("- `%s` from `%s` to `%s`: %s", h.From, h.FromRoot, h.ToRoot, h.Warning))
	}
	var keptLines []string
	for _, k := range r.KeptRoots {
		keptLines = append(keptLines, fmt.Sprintf("- `%s`, of %s", k.Path, strings.Join(k.Environments, ", ")))
	}
	section("Kept where they were", "An earlier import generated these roots, so they keep their directories, and the resources in them, though the configuration file's environments would put those under `envs/<env>/` now: a root's state key comes from its path. To lay them out by environment, import into a new `--out`, or generate them again with `--regenerate`, which hands their resources over to the roots of their environments where removed blocks can take them all, the log saying why not.", keptLines)
	var mergedLines, apartLines []string
	for _, m := range r.Merges {
		roots := make([]string, len(m.Roots))
		for i, root := range m.Roots {
			roots[i] = fmt.Sprintf("`%s` (%s)", root.Path, root.Env)
		}
		if !m.Merged {
			apartLines = append(apartLines, fmt.Sprintf("- %s: %s", strings.Join(roots, ", "), m.Reason))
			continue
		}
		line := fmt.Sprintf("- `%s`, of %s: %d resources shared", m.Path, strings.Join(roots, ", "), m.Shared)
		if len(m.Residue) > 0 {
			line += fmt.Sprintf(", %d kept as their environment's root had them", len(m.Residue))
		}
		if len(m.Unplanned) > 0 {
			line += fmt.Sprintf("; not planned as %s, which the import didn't have: plan it with each one's credentials, with `infraharvest verify --environment`, before applying", strings.Join(m.Unplanned, ", "))
		}
		if len(m.Apart) > 0 {
			apart := make([]string, len(m.Apart))
			for i, root := range m.Apart {
				apart[i] = fmt.Sprintf("`%s` (%s)", root.Path, root.Env)
			}
			line += fmt.Sprintf("; %s, new to it, kept a root of its own: %s", strings.Join(apart, ", "), m.ApartReason)
		}
		mergedLines = append(mergedLines, line)
		for _, res := range m.Residue {
			mergedLines = append(mergedLines, fmt.Sprintf("  - `%s` of %s, as `%s`: %s", res.Address, res.Env, res.MergedAddress, res.Reason))
		}
	}
	section("Environments merged", "The environments of each of these stacks share one root, with their own values in `vars/<env>/`: each environment's plan of it, with its own credentials, imports what its own root did and changes nothing else. The roots they had are kept under `.infraharvest/premerge/`. Each merged root's README says how to plan and apply an environment.", mergedLines)
	section("Environments kept apart", "The environments of each of these stacks keep a root each, as they were generated: merging them into one root wouldn't have planned the same.", apartLines)
	section("Handovers not finished", "One of the two roots of each of these handovers failed: do what each line says before applying either root.", incompleteLines)
	r.stacksMarkdown(&b)
	section("Modules", "These resources are managed through module calls, each kept because it planned the same as the resources it replaced.", modules)
	section("Not moved into a module", "A module couldn't manage these resources the same way, so they stay in the root.", declined)
	var skipped []string
	for _, s := range r.Skipped {
		skipped = append(skipped, fmt.Sprintf("- `%s` (%d): %s", s.Type, s.Count, s.Reason))
	}
	section("Not importable", "infraharvest doesn't import these resource types.", skipped)
	var excluded []string
	for _, e := range r.Excluded {
		excluded = append(excluded, fmt.Sprintf("- `%s` `%s`: %s", e.Type, e.ID, e.Reason))
	}
	section("Excluded", "The selection left these resources out.", excluded)
	var failures []string
	for _, f := range r.Failures {
		failures = append(failures, "- "+f)
	}
	var checks []string
	for _, f := range r.FailedChecks() {
		checks = append(checks, "- "+f)
	}
	section("Failed checks", "These directories were generated, but failed a check of the verification gate. Each directory's README lists its checks.", checks)
	var skippedChecks []string
	for _, s := range r.SkippedChecks() {
		skippedChecks = append(skippedChecks, "- "+s)
	}
	section("Skipped checks", "These checks couldn't run on these directories, so they checked nothing. They don't fail the import.", skippedChecks)
	section("Failed directories", "Nothing in these directories was imported.", dirErrors)
	section("Failures", "These parts of the import couldn't run, or ran only in part: services that couldn't be listed, accounts or projects that couldn't be reached, tags or default selections that couldn't be read, and what stopped the run.", failures)
	return b.String()
}

// Read reads the report an import wrote into outputDir (see WriteFiles).
func Read(outputDir string) (*Report, error) {
	r := &Report{}
	for _, name := range []string{"coverage.json", "manifest.json"} {
		content, err := os.ReadFile(filepath.Join(outputDir, Dir, name))
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(content, r); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	if r.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("report schema version %d, want %d", r.SchemaVersion, SchemaVersion)
	}
	return r, nil
}
