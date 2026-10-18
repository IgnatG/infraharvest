// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// The files infraharvest drift writes into the report directory, apart
// from an import's, so that the import's report stays the same.
const (
	DriftJSONFile     = "drift.json"
	DriftMarkdownFile = "drift.md"
)

// What the code makes of a change in the cloud (DriftChange.Outcome).
const (
	DriftReverts       = "reverts"
	DriftAbsorbed      = "absorbed"
	DriftNotAssessable = "not_assessable"
)

// DriftDeleted is the kind of a change of a resource no longer in the
// cloud.
const DriftDeleted = "deleted"

// DriftUndecided is the decision of a change no one decided on yet, as
// for every change detection alone finds.
const DriftUndecided = "undecided"

// Drift is what infraharvest drift found in the roots of an output
// directory.
type Drift struct {
	SchemaVersion int `json:"schema_version"`
	// DriftFound says whether a change reverts on the next apply or is
	// not assessable: changes the code absorbs alone aren't drift to act
	// on.
	DriftFound bool          `json:"drift_found"`
	Totals     DriftTotals   `json:"totals"`
	Roots      []DriftRoot   `json:"roots"`
	Changes    []DriftChange `json:"changes"`
}

// DriftTotals count the roots and changes of a Drift.
type DriftTotals struct {
	Roots         int `json:"roots"`
	Failed        int `json:"failed"`
	Changes       int `json:"changes"`
	Reverts       int `json:"reverts"`
	Absorbed      int `json:"absorbed"`
	NotAssessable int `json:"not_assessable"`
}

// DriftRoot is one root drift checked, or couldn't.
type DriftRoot struct {
	Path string `json:"path"`
	// Backend is the type of the backend the state was read from, local
	// for none.
	Backend string `json:"backend,omitempty"`
	// Resources counts the managed resources in the state.
	Resources int `json:"resources"`
	// NotApplied are the resources the import blocks import that the
	// state doesn't have yet.
	NotApplied []string `json:"not_applied,omitempty"`
	// Placeholders are the variables without a value that got a
	// placeholder: what they set is not assessable.
	Placeholders []string `json:"placeholders,omitempty"`
	// Error says why the root couldn't be checked.
	Error string `json:"error,omitempty"`
}

// DriftChange is one attribute, or one resource, that changed in the
// cloud since the state last recorded it.
type DriftChange struct {
	Root       string `json:"root"`
	Address    string `json:"address"`
	Type       string `json:"type"`
	ModuleCall string `json:"module_call,omitempty"`
	// Attribute is the path of what changed, such as tags.Env; empty for
	// a deleted resource.
	Attribute string `json:"attribute,omitempty"`
	// Before is the state's value and After the cloud's, with secrets
	// shown as "(sensitive value)".
	Before any `json:"before,omitempty"`
	After  any `json:"after,omitempty"`
	// Kind is changed, or deleted for a resource no longer in the cloud.
	Kind string `json:"kind"`
	// Source is where the root's code sets the attribute, as best it can
	// be told.
	Source   *DriftSource `json:"source,omitempty"`
	Decision string       `json:"decision"`
	// Outcome is reverts, absorbed or not_assessable.
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

// DriftSource is where a root's code sets an attribute.
type DriftSource struct {
	File       string `json:"file"`
	Line       int    `json:"line,omitempty"`
	Expression string `json:"expression,omitempty"`
}

// Finish sorts the roots and changes and counts them.
func (d *Drift) Finish() {
	d.SchemaVersion = SchemaVersion
	sort.SliceStable(d.Roots, func(i, j int) bool { return d.Roots[i].Path < d.Roots[j].Path })
	sort.SliceStable(d.Changes, func(i, j int) bool {
		a, b := d.Changes[i], d.Changes[j]
		if a.Root != b.Root {
			return a.Root < b.Root
		}
		if a.Address != b.Address {
			return a.Address < b.Address
		}
		return a.Attribute < b.Attribute
	})
	if d.Roots == nil {
		d.Roots = []DriftRoot{}
	}
	if d.Changes == nil {
		d.Changes = []DriftChange{}
	}
	t := DriftTotals{Roots: len(d.Roots), Changes: len(d.Changes)}
	for _, r := range d.Roots {
		if r.Error != "" {
			t.Failed++
		}
	}
	for _, c := range d.Changes {
		switch c.Outcome {
		case DriftReverts:
			t.Reverts++
		case DriftAbsorbed:
			t.Absorbed++
		case DriftNotAssessable:
			t.NotAssessable++
		}
	}
	d.Totals = t
	d.DriftFound = t.Reverts+t.NotAssessable > 0
}

// WriteFiles writes drift.json and drift.md into the report directory
// under outputDir.
func (d *Drift) WriteFiles(outputDir string) error {
	dir := filepath.Join(outputDir, Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	content, err := marshal(d)
	if err != nil {
		return err
	}
	if err := fsutil.WriteFile(filepath.Join(dir, DriftJSONFile), content, 0o644); err != nil {
		return err
	}
	return fsutil.WriteFile(filepath.Join(dir, DriftMarkdownFile), []byte(d.Markdown()), 0o644)
}

// WriteJSON writes the drift report as one JSON document, as drift.json
// has it.
func (d *Drift) WriteJSON(w io.Writer) error {
	content, err := marshal(d)
	if err != nil {
		return err
	}
	_, err = w.Write(content)
	return err
}

// maxDriftValue is the longest value drift.md quotes; drift.json has
// them whole.
const maxDriftValue = 80

// Markdown renders the drift report for people.
func (d *Drift) Markdown() string {
	var b strings.Builder
	b.WriteString("# Drift report\n\n")
	b.WriteString("What changed in the cloud since each root was last applied, and what the next `terraform apply` of its code would do about it. Nothing was changed: not the code, not the state, not the cloud.\n\n")
	t := d.Totals
	b.WriteString("| Roots | Not checked | Changes | Reverted by the next apply | Not assessable | Absorbed |\n|---|---|---|---|---|---|\n")
	fmt.Fprintf(&b, "| %d | %d | %d | %d | %d | %d |\n", t.Roots, t.Failed, t.Changes, t.Reverts, t.NotAssessable, t.Absorbed)
	if t.Changes == 0 && t.Failed == 0 {
		b.WriteString("\nNo drift: the cloud is as the roots' state records it.\n")
	}

	sections := []struct {
		outcome, title, intro string
	}{
		{DriftReverts, "Reverted by the next apply", "The code still has the value the state recorded: the next apply puts it back in the cloud. To keep the cloud's value, change the code to match it."},
		{DriftNotAssessable, "Not assessable", "Whether the next apply puts these back can't be told here."},
		{DriftAbsorbed, "Absorbed", "The code doesn't set these, such as attributes the provider computes or that `ignore_changes` names: the next apply keeps the cloud's value. Nothing to do."},
	}
	for _, s := range sections {
		var lines []string
		for _, c := range d.Changes {
			if c.Outcome == s.outcome {
				lines = append(lines, driftLine(c))
			}
		}
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\n%s\n\n%s\n", s.title, s.intro, strings.Join(lines, "\n"))
	}

	var failed, notApplied, placeholders []string
	for _, r := range d.Roots {
		if r.Error != "" {
			failed = append(failed, fmt.Sprintf("- `%s`: %s", r.Path, oneLine(r.Error)))
		}
		for _, a := range r.NotApplied {
			notApplied = append(notApplied, fmt.Sprintf("- `%s` in `%s`", a, r.Path))
		}
		if len(r.Placeholders) > 0 {
			placeholders = append(placeholders, fmt.Sprintf("- `%s`: `%s`", r.Path, strings.Join(r.Placeholders, "`, `")))
		}
	}
	if len(failed) > 0 {
		fmt.Fprintf(&b, "\n## Not checked\n\n%s\n", strings.Join(failed, "\n"))
	}
	if len(notApplied) > 0 {
		fmt.Fprintf(&b, "\n## Not applied yet\n\nThe import blocks import these, but the state doesn't have them yet, so they weren't checked.\n\n%s\n", strings.Join(notApplied, "\n"))
	}
	if len(placeholders) > 0 {
		fmt.Fprintf(&b, "\n## Variables without a value\n\nThese got placeholder values, so what they set is not assessable: set them with `TF_VAR_` environment variables or a `.auto.tfvars` file to check it.\n\n%s\n", strings.Join(placeholders, "\n"))
	}
	return b.String()
}

// driftLine is a change as drift.md lists it.
func driftLine(c DriftChange) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- `%s` in `%s`", c.Address, c.Root)
	if c.Kind == DriftDeleted {
		b.WriteString(": deleted in the cloud")
	} else {
		fmt.Fprintf(&b, ", `%s`: %s → %s", c.Attribute, driftValue(c.Before), driftValue(c.After))
	}
	if c.Source != nil && c.Source.File != "" {
		fmt.Fprintf(&b, " (`%s:%d`)", c.Source.File, c.Source.Line)
	}
	if c.Reason != "" {
		fmt.Fprintf(&b, "; %s", c.Reason)
	}
	return b.String()
}

// driftValue quotes a value for drift.md, as JSON, shortened.
func driftValue(v any) string {
	if v == nil {
		return "(none)"
	}
	content, err := json.Marshal(v)
	if err != nil {
		return "(unreadable)"
	}
	s := strings.ReplaceAll(string(content), "`", "'")
	if r := []rune(s); len(r) > maxDriftValue {
		s = string(r[:maxDriftValue-1]) + "…"
	}
	return "`" + s + "`"
}

// oneLine joins the lines of s, such as a Terraform error's.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
