// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"fmt"
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/internal/report/mdtext"
)

// writeSection writes a section of the report: its title, the intro that
// says what its lines are, and the lines, or nothing without lines.
func writeSection(b *strings.Builder, title, intro string, lines []string) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(b, "\n## %s\n\n%s\n\n%s\n", title, intro, strings.Join(lines, "\n"))
}

// proseLines returns a list item for each message.
func proseLines(messages []string) []string {
	var lines []string
	for _, m := range messages {
		lines = append(lines, "- "+mdtext.Prose(m))
	}
	return lines
}

// summaryMarkdown writes the report's heading, the tools, provider and
// modules the import used, and its totals.
func (r *Report) summaryMarkdown(b *strings.Builder) {
	b.WriteString("# Import report\n\n")
	fmt.Fprintf(b, "%s %s, %s %s, provider `%s` %s", r.Tool.Name, r.Tool.Version, r.Engine.Name, r.Engine.Version, r.Provider.Source, r.Provider.Constraint)
	if r.Provider.Version != "" {
		fmt.Fprintf(b, " (%s)", r.Provider.Version)
	}
	b.WriteString(".\n\n")
	if r.Provider.HeldBack != "" {
		fmt.Fprintf(b, "The provider is held back: %s.\n\n", r.Provider.HeldBack)
	}
	for _, m := range r.Modules {
		fmt.Fprintf(b, "`%s` %s is available; the module calls use %s, the release its adapter is tested with (`--modules latest-untested` calls the newest).\n\n", m.Source, m.Latest, m.Version)
	}
	for _, m := range r.Unused {
		fmt.Fprintf(b, "`%s` %s isn't called: it accepts the provider at `%s` only, not its newest major release, so its resources stay in the root.\n\n", m.Source, m.Version, m.Accepts)
	}

	t := r.Totals
	b.WriteString("| Discovered | Imported | Excluded | Left out | Not importable | Failed |\n|---|---|---|---|---|---|\n")
	fmt.Fprintf(b, "| %d | %d | %d | %d | %d | %d |\n", t.Discovered, t.Imported, t.Excluded, t.LeftOut, t.Skipped, t.Failed)
}

// typesMarkdown writes the counts by type, then what is managed and what
// isn't, by account and region where there are several, and by type.
func (r *Report) typesMarkdown(b *strings.Builder) {
	if len(r.Types) == 0 {
		return
	}
	b.WriteString("\n## By type\n\n| Type | Discovered | Imported | Excluded | Left out | Not importable | Failed |\n|---|---|---|---|---|---|---|\n")
	for _, c := range r.Types {
		fmt.Fprintf(b, "| `%s` | %d | %d | %d | %d | %d | %d |\n", c.Type, c.Discovered, c.Imported, c.Excluded, c.LeftOut, c.Skipped, c.Failed)
	}
	b.WriteString("\n## Managed and unmanaged\n\nWhat Terraform already manages, according to the state read with --managed-state or to the resources' tags, what other tools such as CloudFormation or Pulumi manage, and what the cloud manages itself, such as a default VPC, or the default selection leaves out for another reason: the import left those out (--include-owned imports those their tags say are managed). The rest isn't under infrastructure as code yet.\n\n")
	const header = "| Discovered | Managed by Terraform | Managed by another tool | Managed by the cloud | Not managed |"
	if len(r.Scopes) > 1 {
		b.WriteString("| Account and region " + header + "\n|---|---|---|---|---|---|\n")
		for _, s := range r.Scopes {
			fmt.Fprintf(b, "| %s | %d | %d | %d | %d | %d |\n", mdtext.Cell(mdtext.Code(s.Scope)), s.Discovered, s.Managed, s.OtherTool, s.Cloud, s.notManaged())
		}
		b.WriteString("\n")
	}
	b.WriteString("| Type " + header + "\n|---|---|---|---|---|---|\n")
	for _, c := range r.Types {
		fmt.Fprintf(b, "| `%s` | %d | %d | %d | %d | %d |\n", c.Type, c.Discovered, c.Managed, c.OtherTool, c.Cloud, c.notManaged())
	}
}

// tagsMarkdown writes the counts by the value of each tag asked for, a
// table for each tag.
func (r *Report) tagsMarkdown(b *strings.Builder) {
	if len(r.Tags) == 0 {
		return
	}
	b.WriteString("\n## By tag\n\nWhat was discovered by the value of each tag asked for with --report-tags, and how much of it is under infrastructure as code.\n")
	key := ""
	for _, c := range r.Tags {
		if c.Key != key {
			key = c.Key
			fmt.Fprintf(b, "\n| %s | Discovered | Imported | Managed by Terraform | Managed by another tool | Managed by the cloud | Not managed |\n|---|---|---|---|---|---|---|\n", mdtext.Cell(mdtext.Code(key)))
		}
		value := "(not tagged)"
		if c.Value != "" {
			value = mdtext.Cell(mdtext.Code(c.Value))
		}
		fmt.Fprintf(b, "| %s | %d | %d | %d | %d | %d | %d |\n", value, c.Discovered, c.Imported, c.Managed, c.OtherTool, c.Cloud, c.notManaged())
	}
}

// directoryMarkdown has the list items of the report's sections that
// gather what each directory has.
type directoryMarkdown struct {
	leftOut, secrets, warnings, modules, declined, plans, errors []string
}

// directoryLines returns the list items of each directory's resources
// left out, secrets, warnings, module calls, declined modules and error,
// the report's own warnings first.
func (r *Report) directoryLines() directoryMarkdown {
	var d directoryMarkdown
	for _, w := range r.Warnings {
		d.warnings = append(d.warnings, "- "+mdtext.Prose(w)+".")
	}
	for _, dir := range r.Directories {
		for _, m := range dir.Modules {
			resources := "`" + strings.Join(m.Resources, "`, `") + "`"
			source := strings.TrimSpace("`" + m.Source + "` " + m.Version)
			if m.Declined != "" {
				d.declined = append(d.declined, fmt.Sprintf("- %s in `%s` (%s): %s", resources, dir.Path, source, mdtext.Prose(m.Declined)))
			} else {
				d.modules = append(d.modules, fmt.Sprintf("- `module.%s` in `%s`: %s, for %s", m.Name, dir.Path, source, resources))
			}
		}
		for _, l := range dir.LeftOut {
			d.leftOut = append(d.leftOut, fmt.Sprintf("- `%s` in `%s`: %s", l.Address, dir.Path, mdtext.Prose(strings.Join(l.Errors, "; "))))
		}
		for _, s := range dir.Secrets {
			d.secrets = append(d.secrets, fmt.Sprintf("- `%s` in `%s`: %s of `%s`", s.Variable, dir.Path, s.Attribute, s.Address))
		}
		for _, w := range dir.Warnings {
			d.warnings = append(d.warnings, fmt.Sprintf("- `%s`: %s.", dir.Path, mdtext.Prose(w)))
		}
		if len(dir.Plans) > 0 {
			d.plans = append(d.plans, plansLine(dir))
		}
		if dir.Error != "" {
			d.errors = append(d.errors, fmt.Sprintf("- `%s`: %s", dir.Path, mdtext.Prose(dir.Error)))
		}
	}
	return d
}

// plansLine returns the list item of the plans generating dir ran: their
// total, then each step's.
func plansLine(dir Directory) string {
	total := 0
	steps := make([]string, 0, len(dir.Plans))
	for _, p := range dir.Plans {
		total += p.Plans
		steps = append(steps, fmt.Sprintf("%s %d", p.Step, p.Plans))
	}
	return fmt.Sprintf("- `%s`: %d (%s)", dir.Path, total, strings.Join(steps, ", "))
}

// notInCloudLines returns a list item for each resource the roots have
// that the import didn't find.
func (r *Report) notInCloudLines() []string {
	var lines []string
	for _, g := range r.NotInCloud {
		line := fmt.Sprintf("- `%s` %s in `%s`", g.Address, mdtext.Code(g.ID), g.Path)
		if g.Environment != "" {
			line += ", of " + g.Environment
		}
		lines = append(lines, line)
	}
	return lines
}

// movedLines returns a list item for each resource a root generated again
// moves to another address.
func (r *Report) movedLines() []string {
	var lines []string
	for _, m := range r.Moved {
		lines = append(lines, fmt.Sprintf("- `%s` to `%s` in `%s`", m.From, m.To, m.FromRoot))
	}
	return lines
}

// departedLines returns a list item for each resource handed over to
// another root.
func (r *Report) departedLines() []string {
	var lines []string
	for _, d := range r.Departed {
		to := "`" + d.ToRoot + "`, which didn't import it"
		if d.To != "" {
			to = fmt.Sprintf("`%s` in `%s`", d.To, d.ToRoot)
		}
		lines = append(lines, fmt.Sprintf("- `%s` in `%s` to %s", d.From, d.FromRoot, to))
	}
	return lines
}

// keptRootLines returns a list item for each root an earlier import
// generated that keeps its directory.
func (r *Report) keptRootLines() []string {
	var lines []string
	for _, k := range r.KeptRoots {
		lines = append(lines, fmt.Sprintf("- `%s`, of %s", k.Path, strings.Join(k.Environments, ", ")))
	}
	return lines
}

// mergeLines returns the list items of the stacks whose environments were
// merged into one root, each followed by the resources kept as their
// environment's root had them, and of those kept apart.
func (r *Report) mergeLines() (merged, apart []string) {
	for _, m := range r.Merges {
		roots := make([]string, len(m.Roots))
		for i, root := range m.Roots {
			roots[i] = fmt.Sprintf("`%s` (%s)", root.Path, root.Env)
		}
		if !m.Merged {
			apart = append(apart, fmt.Sprintf("- %s: %s", strings.Join(roots, ", "), mdtext.Prose(m.Reason)))
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
			kept := make([]string, len(m.Apart))
			for i, root := range m.Apart {
				kept[i] = fmt.Sprintf("`%s` (%s)", root.Path, root.Env)
			}
			line += fmt.Sprintf("; %s, new to it, kept a root of its own: %s", strings.Join(kept, ", "), mdtext.Prose(m.ApartReason))
		}
		merged = append(merged, line)
		for _, res := range m.Residue {
			merged = append(merged, fmt.Sprintf("  - `%s` of %s, as `%s`: %s", res.Address, res.Env, res.MergedAddress, mdtext.Prose(res.Reason)))
		}
	}
	return merged, apart
}

// incompleteHandoverLines returns a list item for each handover one of
// whose roots failed.
func (r *Report) incompleteHandoverLines() []string {
	var lines []string
	for _, h := range r.IncompleteHandovers {
		lines = append(lines, fmt.Sprintf("- `%s` from `%s` to `%s`: %s", h.From, h.FromRoot, h.ToRoot, mdtext.Prose(h.Warning)))
	}
	return lines
}

// skippedLines returns a list item for each resource type the import
// doesn't import.
func (r *Report) skippedLines() []string {
	var lines []string
	for _, s := range r.Skipped {
		lines = append(lines, fmt.Sprintf("- `%s` (%d): %s", s.Type, s.Count, s.Reason))
	}
	return lines
}

// excludedLines returns a list item for each resource the selection left
// out.
func (r *Report) excludedLines() []string {
	var lines []string
	for _, e := range r.Excluded {
		lines = append(lines, fmt.Sprintf("- `%s` %s: %s", e.Type, mdtext.Code(e.ID), mdtext.Prose(e.Reason)))
	}
	return lines
}

// maxMarkdownFindings bounds the findings report.md lists; report.json
// has them all.
const maxMarkdownFindings = 200

// findingsMarkdown writes the section of the findings, by root and
// resource, then scanner, up to maxMarkdownFindings, saying how many it
// leaves out.
func (r *Report) findingsMarkdown(b *strings.Builder) {
	if len(r.Findings) == 0 {
		return
	}
	b.WriteString("\n## Findings by resource\n\nThe scanners of the verification gate (G5), with your policies (`--policy`), found these in the roots, by the resource each is about, where they name one. trivy's, checkov's and your policies' describe the infrastructure's own settings, which the configuration mirrors: they don't fail the import, and are for you to review before applying, to change in the cloud, or in the code once it is applied. `report.json` lists them all, with each resource's type and import ID.\n\n")
	if left := writeFindings(b, r.Findings, maxMarkdownFindings); left > 0 {
		fmt.Fprintf(b, "\n%d more findings aren't listed here: `report.json` has them all.\n", left)
	}
}

// ListFindings returns findings as report.md lists them (see
// writeFindings), all of them, sorted, as infraharvest verify prints
// them.
func ListFindings(findings []Finding) string {
	findings = slices.Clone(findings)
	sortFindings(findings)
	var b strings.Builder
	writeFindings(&b, slices.Compact(findings), len(findings))
	return b.String()
}

// writeFindings writes findings, which are sorted, up to limit of them,
// under a line for each root and resource they are about, and returns
// how many it leaves out.
func writeFindings(b *strings.Builder, findings []Finding, limit int) int {
	group := ""
	for i, f := range findings {
		if i == limit {
			return len(findings) - i
		}
		root := "`" + f.Root + "`"
		if f.Environment != "" {
			root += " as " + f.Environment
		}
		heading := fmt.Sprintf("- %s, not about one resource:", root)
		switch {
		case f.ID != "":
			about := "`" + f.ID + "`"
			if f.Service != "" {
				about += ", " + f.Service
			}
			heading = fmt.Sprintf("- `%s` in %s (%s):", f.Address, root, about)
		case f.Address != "":
			heading = fmt.Sprintf("- `%s` in %s:", f.Address, root)
		}
		if heading != group {
			group = heading
			b.WriteString(heading + "\n")
		}
		fmt.Fprintf(b, "  - %s: %s\n", f.Scanner, f.String())
	}
	return 0
}
