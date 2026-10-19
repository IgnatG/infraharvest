// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

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

// The files infraharvest drift writes into the report directory, apart
// from an import's, so that the import's report stays the same.
const (
	DriftJSONFile     = "drift.json"
	DriftMarkdownFile = "drift.md"
)

// What became of a change in the cloud (DriftChange.Outcome).
const (
	// DriftWritten: accepted, and written into the code.
	DriftWritten = "written"
	// DriftKept: the code keeps its value: the next apply puts it back in
	// the cloud.
	DriftKept = "kept"
	// DriftRejectedByPlan: accepted, but the plan of the edited code
	// didn't take it, so nothing was written for it.
	DriftRejectedByPlan = "rejected_by_plan"
	// DriftManual: accepted, but taking it into the code is a hand edit.
	DriftManual = "manual"
	// DriftSecret: accepted, but a secret, which is never written.
	DriftSecret = "secret"
	// DriftNotAssessable: what the next apply does about it can't be
	// told.
	DriftNotAssessable = "not_assessable"
	// DriftAbsorbed: the code doesn't set it: the next apply keeps the
	// cloud's value.
	DriftAbsorbed = "absorbed"
)

// DriftDeleted is the kind of a change of a resource no longer in the
// cloud.
const DriftDeleted = "deleted"

// What was decided for a change (DriftChange.Decision): keep is the
// default, accept what an --accept rule chose.
const (
	DriftAccept = "accept"
	DriftKeep   = "keep"
)

// Drift is what infraharvest drift found in the roots of an output
// directory.
type Drift struct {
	SchemaVersion int `json:"schema_version"`
	// DriftFound says whether a change is left that the next apply
	// reverts, or that is not assessable: changes written into the code
	// and those the code absorbs aren't drift to act on.
	DriftFound bool          `json:"drift_found"`
	Totals     DriftTotals   `json:"totals"`
	Roots      []DriftRoot   `json:"roots"`
	Changes    []DriftChange `json:"changes"`
}

// DriftTotals count the roots and changes of a Drift.
type DriftTotals struct {
	Roots          int `json:"roots"`
	Failed         int `json:"failed"`
	Changes        int `json:"changes"`
	Written        int `json:"written"`
	Kept           int `json:"kept"`
	RejectedByPlan int `json:"rejected_by_plan"`
	Manual         int `json:"manual"`
	Secret         int `json:"secret"`
	NotAssessable  int `json:"not_assessable"`
	Absorbed       int `json:"absorbed"`
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
	Source *DriftSource `json:"source,omitempty"`
	// Covers are, for a change of tags several resources get from one
	// place, such as local.tags or provider.aws.default_tags, which
	// Address then names, the resources it is of: each changed the key to
	// the same value, or removed it, so that it is one change, accepted by
	// editing that place once. Type is then empty.
	Covers []string `json:"covers,omitempty"`
	// Decision is accept or keep.
	Decision string `json:"decision"`
	// Outcome is written, kept, rejected_by_plan, manual, secret,
	// not_assessable or absorbed.
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
	// ForcesReplacement says the next apply would replace the resource to
	// put the code's value back.
	ForcesReplacement bool `json:"forces_replacement,omitempty"`
}

// DriftSource is where a root's code sets an attribute.
type DriftSource struct {
	File       string `json:"file"`
	Line       int    `json:"line,omitempty"`
	Expression string `json:"expression,omitempty"`
	// ModulePath says, for a resource in a module call, how the module
	// gets the value from the call's argument at File and Line: the
	// module's file, line and expression, such as
	// ../../modules/bucket_0a1b2c3d/main.tf:4 var.tags, or, for a curated
	// module, its release and the input its adapter maps the attribute
	// onto. Calls of modules inside modules are separated by "; ", from
	// the root's call inwards.
	ModulePath string `json:"module_path,omitempty"`
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
		case DriftWritten:
			t.Written++
		case DriftKept:
			t.Kept++
		case DriftRejectedByPlan:
			t.RejectedByPlan++
		case DriftManual:
			t.Manual++
		case DriftSecret:
			t.Secret++
		case DriftNotAssessable:
			t.NotAssessable++
		case DriftAbsorbed:
			t.Absorbed++
		}
	}
	d.Totals = t
	d.DriftFound = t.Kept+t.RejectedByPlan+t.Manual+t.Secret+t.NotAssessable > 0
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
	b.WriteString("What changed in the cloud since each root was last applied, and what the next `terraform apply` of its code would do about it. ")
	if d.Totals.Written > 0 {
		b.WriteString("The changes accepted into the code were written into the roots' files; nothing else was changed: not the state, not the cloud.\n\n")
	} else {
		b.WriteString("Nothing was changed: not the code, not the state, not the cloud.\n\n")
	}
	t := d.Totals
	b.WriteString("| Roots | Not checked | Changes | Accepted into the code | Kept | Needs a hand edit | Secret or not assessable | Absorbed |\n|---|---|---|---|---|---|---|---|\n")
	fmt.Fprintf(&b, "| %d | %d | %d | %d | %d | %d | %d | %d |\n", t.Roots, t.Failed, t.Changes, t.Written, t.Kept, t.RejectedByPlan+t.Manual, t.Secret+t.NotAssessable, t.Absorbed)
	if t.Changes == 0 && t.Failed == 0 {
		b.WriteString("\nNo drift: the cloud is as the roots' state records it.\n")
	}

	sections := []struct {
		outcomes     []string
		title, intro string
	}{
		{[]string{DriftWritten}, "Accepted into the code", "The code now has the cloud's value, at the line each change gives: the next apply leaves it as it is. Look the edits over and commit them."},
		{[]string{DriftKept}, "Kept: the next apply reverts the cloud", "The code still has the value the state recorded: the next apply puts it back in the cloud. To keep the cloud's value instead, accept the change with `--accept`."},
		{[]string{DriftRejectedByPlan, DriftManual}, "Needs a hand edit", "Accepted, but not written: where the code sets the value takes a hand edit, or the plan of the edited code didn't take the edit, as each says. Until then, the next apply puts the code's value back."},
		{[]string{DriftSecret, DriftNotAssessable}, "Secrets and not assessable", "Secrets are never written into the code: update them where the root's secret variables come from. Whether the next apply puts back the others can't be told here."},
		{[]string{DriftAbsorbed}, "Absorbed", "The code doesn't set these, such as attributes the provider computes or that `ignore_changes` names: the next apply keeps the cloud's value. Nothing to do."},
	}
	for _, s := range sections {
		var lines []string
		for _, c := range d.Changes {
			if slices.Contains(s.outcomes, c.Outcome) {
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
	if len(c.Covers) > 0 {
		fmt.Fprintf(&b, ", on every resource that gets it from there: `%s`", strings.Join(c.Covers, "`, `"))
	}
	switch {
	case c.Source != nil && c.Source.File != "" && c.Source.ModulePath != "":
		fmt.Fprintf(&b, " (`%s:%d`, which the module reads at `%s`)", c.Source.File, c.Source.Line, strings.ReplaceAll(c.Source.ModulePath, "`", "'"))
	case c.Source != nil && c.Source.File != "":
		fmt.Fprintf(&b, " (`%s:%d`)", c.Source.File, c.Source.Line)
	}
	if c.Reason != "" {
		fmt.Fprintf(&b, "; %s", c.Reason)
	}
	if c.ForcesReplacement && c.Decision == DriftAccept {
		switch c.Outcome {
		case DriftWritten:
			b.WriteString("; keeping the code instead would have replaced the resource on the next apply")
		default:
			b.WriteString("; keeping the code replaces the resource on the next apply")
		}
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
