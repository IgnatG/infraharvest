// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package report describes what an import did: what it found, what it
// imported, what it left out and why, and the versions it used. It writes
// the report directory of the output (coverage.json, manifest.json and
// report.md) and the JSON of --output json.
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
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/managed"
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
)

// Dir is the directory of the output the report files go into.
const Dir = "report"

// Report is what one import did.
type Report struct {
	SchemaVersion int `json:"schema_version"`
	Manifest
	Coverage
	ExitCode int `json:"exit_code"`
}

// Manifest records the versions an import used.
type Manifest struct {
	Tool     Component `json:"tool"`
	Engine   Component `json:"engine"`
	Provider Provider  `json:"provider"`
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
}

// Coverage records what an import found and what became of it.
type Coverage struct {
	Types       []TypeCount   `json:"types"`
	Directories []Directory   `json:"directories"`
	Skipped     []Skipped     `json:"skipped,omitempty"`
	Excluded    []Excluded    `json:"excluded,omitempty"`
	Failures    []string      `json:"failures,omitempty"`
	Totals      CoverageTotal `json:"totals"`
	// Scopes count by provider, account and region (see AddDiscovered),
	// such as aws/123456789012/eu-west-2.
	Scopes []ScopeCount `json:"scopes,omitempty"`
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
	// Modules are the directory's module calls, and the clusters of
	// resources that stayed in the root, with why.
	Modules []Module `json:"modules,omitempty"`
	// Checks are the verification gate's results.
	Checks []Check `json:"checks,omitempty"`
	// Error is why nothing in the directory was imported.
	Error string `json:"error,omitempty"`
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
	Name    string   `json:"name"`
	Passed  bool     `json:"passed"`
	Details []string `json:"details,omitempty"`
}

// Excluded is a listed resource the selection left out.
type Excluded struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Scope  string `json:"scope,omitempty"`
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
	r.Scopes = append(r.Scopes, ScopeCount{Scope: scope, CoverageTotal: CoverageTotal{Discovered: n}})
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
	sort.Strings(r.Failures)

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
		count(e.Type).Excluded++
		if strings.HasPrefix(e.Reason, managed.Reason) {
			count(e.Type).Managed++
		} else if strings.HasPrefix(e.Reason, OtherToolPrefix) {
			count(e.Type).OtherTool++
		}
		inScope(e.Scope, func(c *CoverageTotal) {
			c.Excluded++
			switch {
			case strings.HasPrefix(e.Reason, managed.Reason):
				c.Managed++
			case strings.HasPrefix(e.Reason, OtherToolPrefix):
				c.OtherTool++
			}
		})
	}
	for i := range r.Directories {
		d := &r.Directories[i]
		sort.Slice(d.Imported, func(a, b int) bool { return d.Imported[a].Address < d.Imported[b].Address })
		sort.Slice(d.LeftOut, func(a, b int) bool { return d.LeftOut[a].Address < d.LeftOut[b].Address })
		for _, res := range d.Imported {
			count(resourceType(res.Address)).Imported++
		}
		for _, res := range d.LeftOut {
			count(resourceType(res.Address)).LeftOut++
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
	}
	for i := range r.Scopes {
		r.Scopes[i].CoverageTotal = *byScope[r.Scopes[i].Scope]
	}
	sort.Slice(r.Scopes, func(i, j int) bool { return r.Scopes[i].Scope < r.Scopes[j].Scope })

	switch {
	case !r.Incomplete():
		r.ExitCode = ExitOK
	case allowPartial:
		r.ExitCode = ExitPartial
	default:
		r.ExitCode = ExitIncomplete
	}
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

func resourceType(address string) string {
	t, _, _ := strings.Cut(address, ".")
	return t
}

// WriteFiles writes coverage.json, manifest.json and report.md into the
// report directory under outputDir.
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
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			return err
		}
	}
	return nil
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

	t := r.Totals
	b.WriteString("| Discovered | Imported | Excluded | Left out | Not importable | Failed |\n|---|---|---|---|---|---|\n")
	fmt.Fprintf(&b, "| %d | %d | %d | %d | %d | %d |\n", t.Discovered, t.Imported, t.Excluded, t.LeftOut, t.Skipped, t.Failed)

	if len(r.Types) > 0 {
		b.WriteString("\n## By type\n\n| Type | Discovered | Imported | Excluded | Left out | Not importable | Failed |\n|---|---|---|---|---|---|---|\n")
		for _, c := range r.Types {
			fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %d | %d | %d |\n", c.Type, c.Discovered, c.Imported, c.Excluded, c.LeftOut, c.Skipped, c.Failed)
		}
	}
	if r.Totals.Managed > 0 || r.Totals.OtherTool > 0 {
		b.WriteString("\n## Managed and unmanaged\n\nTerraform already manages some of what was discovered, according to the state read with --managed-state, and other tools, such as CloudFormation, manage some more: the import left those out. The rest isn't under infrastructure as code yet.\n\n")
		if len(r.Scopes) > 1 {
			b.WriteString("| Account and region | Discovered | Managed by Terraform | Managed by another tool | Not managed |\n|---|---|---|---|---|\n")
			for _, s := range r.Scopes {
				fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %d |\n", s.Scope, s.Discovered, s.Managed, s.OtherTool, s.Discovered-s.Managed-s.OtherTool)
			}
			b.WriteString("\n")
		}
		b.WriteString("| Type | Discovered | Managed by Terraform | Managed by another tool | Not managed |\n|---|---|---|---|---|\n")
		for _, c := range r.Types {
			fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %d |\n", c.Type, c.Discovered, c.Managed, c.OtherTool, c.Discovered-c.Managed-c.OtherTool)
		}
	}

	var leftOut, secrets, modules, declined, dirErrors []string
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
	section("Secrets to set", "Set these variables before planning: Terraform doesn't write secret values into the configuration it generates.", secrets)
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
	section("Failed directories", "Nothing in these directories was imported.", dirErrors)
	section("Failed services", "These services couldn't be listed.", failures)
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
