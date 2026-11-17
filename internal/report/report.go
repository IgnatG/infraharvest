// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package report describes what an import did: what it found, what it
// imported, what it left out and why, and the versions it used. It writes
// the report directory of the output (report.json and report.md) and the
// JSON of --format json, which report.json has.
//
// Reports hold no timestamps or absolute paths, so an import of an
// unchanged estate produces the same report.
package report

import (
	"slices"
	"sort"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/report/cfn"
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
	// change whose edit the plan of the edited code, or a check of it,
	// rejected (see DriftTotals.RejectedByPlan and RejectedByCheck).
	ExitDriftRejected = 1
	// ExitInterrupted: the import was interrupted (SIGINT, as by Ctrl-C).
	// Like the codes a shell gives a process a signal ends, it is 128
	// plus the signal's number.
	ExitInterrupted = 130
	// ExitTerminated: the import was terminated (SIGTERM).
	ExitTerminated = 143
)

// DefaultOut is the output directory an import writes its roots and
// report into, and the commands that read them back read, without --out.
const DefaultOut = "infrastructure"

// Dir is the directory of the output the report files go into.
const Dir = "_reports"

// Report is what one import did.
type Report struct {
	SchemaVersion int `json:"schema_version"`
	Manifest
	Coverage
	ExitCode int `json:"exit_code"`
	// tagKeys are the tag keys Finish counts by (see CountByTags).
	tagKeys []string
	// listed are the resources the listers found, with their tags, for
	// the counts by tag (see AddListed).
	listed []Listed
	// arrived are the resources roots import that other roots hand over,
	// which Finish matches with Departed (see AddDirectory).
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
	// Report.CountByTags), "" for resources without the tag.
	Tags []TagCount `json:"tags,omitempty"`
	// CloudFormationStacks are the CloudFormation stacks the import is
	// scoped to, and those nested in them, with what became of their
	// resources and whether each is safe to delete (AWS --cfn-stack).
	CloudFormationStacks cfn.Stacks `json:"cloudformation_stacks,omitempty"`
	// Findings are what the verification gate's scanners found in the
	// roots, with the policies of --policy, each with the resource it is
	// about where it names one.
	Findings []Finding `json:"findings,omitempty"`
}

// Finding is what a scanner of the verification gate found in a root
// (Root, relative to the output directory, with forward slashes), as
// Environment plans it for a root environments share: by Scanner, its
// rule, severity, place and message. Address is the
// resource or module call it is about, where the scanner names one; Type
// and ID, the resource's type and import ID, where the root imports a
// resource there, and Scope and Service where the import listed it, as
// the selection file's entry for it has them.
type Finding struct {
	Root        string `json:"root"`
	Environment string `json:"environment,omitempty"`
	Address     string `json:"address,omitempty"`
	Type        string `json:"type,omitempty"`
	ID          string `json:"id,omitempty"`
	Scope       string `json:"scope,omitempty"`
	Service     string `json:"service,omitempty"`
	Scanner     string `json:"scanner"`
	Rule        string `json:"rule"`
	Severity    string `json:"severity,omitempty"`
	Location    string `json:"location,omitempty"`
	Message     string `json:"message,omitempty"`
}

// String describes f as the check's details do: its rule, severity,
// place and message.
func (f Finding) String() string {
	return gate.Finding{Rule: f.Rule, Severity: f.Severity, Location: f.Location, Message: f.Message}.String()
}

// sortFindings sorts findings by root and environment; in each, those
// about a resource first, by address, then the others; then by scanner
// and what each says.
func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Root != b.Root {
			return a.Root < b.Root
		}
		if a.Environment != b.Environment {
			return a.Environment < b.Environment
		}
		if (a.Address == "") != (b.Address == "") {
			return a.Address != ""
		}
		if a.Address != b.Address {
			return a.Address < b.Address
		}
		if a.Scanner != b.Scanner {
			return a.Scanner < b.Scanner
		}
		return a.String() < b.String()
	})
}

// TagCount counts the resources with one value of a tag key.
type TagCount struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	CoverageTotal
}

// Listed is a resource the listers found that Terraform can import: where,
// by which service, its type and import ID, and its tags.
type Listed struct {
	Scope, Service, Type, ID string
	Tags                     map[string]string
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
	// Managed counts the excluded resources Terraform already manages: by a
	// state, by its tags, or in a root of the output.
	Managed int `json:"managed,omitempty"`
	// OtherTool counts the excluded resources another tool manages, such as
	// CloudFormation (see OtherToolPrefix).
	OtherTool int `json:"other_tool,omitempty"`
	// Cloud counts the excluded resources the default selection leaves out
	// for another reason, mostly as the cloud creates and manages them
	// itself, such as a default VPC (see OwnerCloud).
	Cloud int `json:"cloud,omitempty"`
}

// exclude counts an excluded resource, and what manages it, owner, one of
// the Owner constants.
func (c *CoverageTotal) exclude(owner string) {
	c.Excluded++
	switch owner {
	case OwnerTerraformState, OwnerTerraformTag, OwnerTerraformRoot:
		c.Managed++
	case OwnerCloudFormation, OwnerOtherTool:
		c.OtherTool++
	case OwnerCloud:
		c.Cloud++
	}
}

// notManaged counts what was discovered that neither Terraform, another
// tool nor the cloud manages.
func (c CoverageTotal) notManaged() int {
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
	// Plans count the Terraform plans generating the directory ran, by
	// step, each of which imports every resource of it again.
	Plans []Plans `json:"plans,omitempty"`
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

// Plans counts the plans a step of generating a directory ran, failed
// ones included, and the plan files it read back (shows): generate,
// prepare, repair, secrets, tidy, finish, refine or gate, or merge for
// the plans of the merged root of its stack as its environment (see
// AddPlans).
type Plans struct {
	Step  string `json:"step"`
	Plans int    `json:"plans"`
	Shows int    `json:"shows"`
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
	Type string `json:"type"`
	ID   string `json:"id"`
	// Reason says why, for people.
	Reason string `json:"reason"`
	Scope  string `json:"scope,omitempty"`
	// Owner is what manages it, one of the Owner constants, as the code
	// that left it out knows it. Reports older than it have none (see
	// ManagedBy).
	Owner string `json:"owner,omitempty"`
}

// ManagedBy returns what manages e, one of the Owner constants: its Owner,
// or, in a report older than it, what its reason says (see OwnerOf).
func (e Excluded) ManagedBy() string {
	if e.Owner != "" {
		return e.Owner
	}
	return OwnerOf(e.Reason)
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
	// Findings are what the scanners of the merged root's gate found, for
	// each environment, which AddMerge records with the report's, in
	// place of those of the roots merged (see Report.Findings).
	Findings []Finding `json:"findings,omitempty"`
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

// Skipped counts resources of a type infraharvest doesn't import.
type Skipped struct {
	Type   string `json:"type"`
	Count  int    `json:"count"`
	Reason string `json:"reason"`
}

// TerraformPrefix starts the reason of an exclusion for a resource
// Terraform already manages, by a state --managed-state reads or by its
// tags, such as "already managed by Terraform (prod.tfstate)".
const TerraformPrefix = "already managed by Terraform"

// OtherToolPrefix starts the reason of an exclusion for a resource another
// infrastructure-as-code tool manages, such as "managed by CloudFormation
// stack app".
const OtherToolPrefix = "managed by "
