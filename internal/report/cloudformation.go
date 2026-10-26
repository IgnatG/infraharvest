// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// OutsideScopePrefix starts the reason of an exclusion for a resource the
// import's scope leaves out, such as the resources of no CloudFormation
// stack --cfn-stack names: the user's choice, like the selection file's
// (see OwnerSelection).
const OutsideScopePrefix = "outside the import's scope: "

// TemplatesDir is where, under the output directory, the report writes the
// templates of the CloudFormation stacks an import is scoped to (see
// CloudFormationStack): inside .infraharvest, which the output's
// .gitignore keeps out of version control, as a template may hold values
// that are secret.
const TemplatesDir = ".infraharvest/cloudformation"

// What became of a resource of a CloudFormation stack (see
// CloudFormationResource.Outcome).
const (
	// StackImported: every Terraform resource it is was imported.
	StackImported = "imported"
	// StackExcluded: the selection file left it out, by people's decision,
	// or Terraform already manages it.
	StackExcluded = "excluded"
	// StackNotImported: it was found, but not imported: left out by the
	// default selection, rejected, or in a root that failed.
	StackNotImported = "not_imported"
	// StackNotFound: Terraform has a type for it, but nothing listed has
	// its physical ID.
	StackNotFound = "not_found"
	// StackNotImportable: Terraform has no type for it, as for a custom
	// resource.
	StackNotImportable = "not_importable"
	// StackOnlyInCloudFormation: it exists only in CloudFormation, as a
	// wait condition or the CDK's metadata does, so there is nothing to
	// import, and deleting it deletes nothing else.
	StackOnlyInCloudFormation = "only_in_cloudformation"
	// StackNested: a nested stack, whose resources are its own stack's.
	StackNested = "nested_stack"
	// StackDeleted: CloudFormation deleted it, with its stack or before.
	StackDeleted = "deleted"
)

// CloudFormationStack is a CloudFormation stack an import is scoped to
// (AWS --cfn-stack), or a stack nested in one, with each of its
// resources, what became of it, and whether the stack can be deleted
// without deleting what the import brought under Terraform.
//
// It is also the seam for translating the stack's template into Terraform
// (its Parameters into variables, its Outputs into outputs, its
// Conditions): the template, after transforms, is at Template, and each
// resource maps its logical ID to the addresses it was imported at
// (CloudFormationResource.Matches), so a Ref or Fn::GetAtt of a logical
// ID resolves to a Terraform address. Both are in coverage.json and the
// JSON report.
type CloudFormationStack struct {
	Name   string `json:"name"`
	ID     string `json:"id"`
	Region string `json:"region"`
	Status string `json:"status"`
	// Parent is the ID of the stack this one is nested in, if any.
	Parent string `json:"parent,omitempty"`
	// Deleted says CloudFormation deleted the stack already: what it
	// retained is imported, and nothing is left to delete.
	Deleted bool `json:"deleted,omitempty"`
	// Warnings are about the stack, such as a service that manages it.
	Warnings []string `json:"warnings,omitempty"`
	// Template and RetainTemplate are the paths, relative to the output
	// directory, of the stack's template and of a copy with Retain added
	// where a resource lacks it (see TemplatesDir), which the stack can be
	// updated with before it is deleted. RetainTemplate is empty when no
	// resource lacks it.
	Template       string                   `json:"template,omitempty"`
	RetainTemplate string                   `json:"retain_template,omitempty"`
	Resources      []CloudFormationResource `json:"resources"`
	// SafeToDelete says deleting the stack deletes nothing the import
	// brought under Terraform: every resource was imported or left out by
	// people's decision, every root's gate passed, so their plans change
	// nothing, and every resource is retained. Blockers say what keeps it
	// from being so, for a stack that isn't deleted yet.
	SafeToDelete bool     `json:"safe_to_delete"`
	Blockers     []string `json:"blockers,omitempty"`

	// templateBody and retainBody are the templates WriteFiles writes to
	// Template and RetainTemplate: a report read back has none.
	templateBody, retainBody string
	templateExt              string
	// parameters are the values the stack's parameters have, by name, as
	// DescribeStacks gives them (see SetParameters): not in the report,
	// which a person may share, as a value may be one only the stack's
	// owners should see.
	parameters map[string]string
}

// SetTemplates sets the stack's template body, and that of the copy with
// Retain added ("" when none is needed), which WriteFiles writes, with
// their extension (json or yaml).
func (s *CloudFormationStack) SetTemplates(body, retain, ext string) {
	s.templateBody, s.retainBody, s.templateExt = body, retain, ext
}

// TemplateBody returns the stack's template, as SetTemplates set it, or
// "" for a report read back.
func (s *CloudFormationStack) TemplateBody() string {
	return s.templateBody
}

// SetParameters sets the values the stack's parameters have, by name: a
// NoEcho parameter's is masked, as CloudFormation gives it, and an SSM
// parameter type's is the value it resolved to.
func (s *CloudFormationStack) SetParameters(values map[string]string) {
	s.parameters = values
}

// Parameters returns the values SetParameters set, or nil.
func (s *CloudFormationStack) Parameters() map[string]string {
	return s.parameters
}

// CloudFormationResource is a resource of a CloudFormation stack.
type CloudFormationResource struct {
	LogicalID  string `json:"logical_id"`
	Type       string `json:"type"`
	PhysicalID string `json:"physical_id,omitempty"`
	// Status is the resource's status in the stack, such as
	// CREATE_COMPLETE, or DELETE_SKIPPED for one a deleted stack retained.
	Status string `json:"status"`
	// NotImportable says why Terraform can't import it, for an outcome of
	// StackNotImportable or StackOnlyInCloudFormation.
	NotImportable string `json:"not_importable,omitempty"`
	// Retain says what keeps it from outliving the stack: a DeletionPolicy
	// or an UpdateReplacePolicy that isn't Retain. None means it is
	// retained.
	Retain []string `json:"retain,omitempty"`
	// Matches are the listed resources it is, with where each was imported
	// (see CloudFormationMatch).
	Matches []CloudFormationMatch `json:"matches,omitempty"`
	// Outcome is what became of it, one of the Stack constants, and Reason
	// says why, where it wasn't imported.
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

// CloudFormationMatch is a listed resource that is a resource of a stack:
// where it was listed, its Terraform type and import ID and, once the
// report is finished, the root it was imported into and its address
// there.
type CloudFormationMatch struct {
	Scope   string `json:"scope,omitempty"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Path    string `json:"path,omitempty"`
	Address string `json:"address,omitempty"`
}

// AddCloudFormationStacks records the stacks an Import call is scoped to,
// with the listed resources it found of each: a stack another call of the
// import recorded already gets the call's matches added, as the global
// call finds a stack's IAM roles and the regional one its buckets.
func (r *Report) AddCloudFormationStacks(stacks []CloudFormationStack) {
	for _, s := range stacks {
		i := slices.IndexFunc(r.CloudFormationStacks, func(have CloudFormationStack) bool { return have.ID == s.ID })
		if i < 0 {
			s.Resources = slices.Clone(s.Resources)
			for j := range s.Resources {
				s.Resources[j].Matches = slices.Clone(s.Resources[j].Matches)
			}
			r.CloudFormationStacks = append(r.CloudFormationStacks, s)
			continue
		}
		have := &r.CloudFormationStacks[i]
		for _, res := range s.Resources {
			j := slices.IndexFunc(have.Resources, func(h CloudFormationResource) bool { return h.LogicalID == res.LogicalID })
			if j < 0 {
				have.Resources = append(have.Resources, res)
				continue
			}
			for _, m := range res.Matches {
				if !slices.Contains(have.Resources[j].Matches, m) {
					have.Resources[j].Matches = append(have.Resources[j].Matches, m)
				}
			}
		}
	}
}

// finishStacks works out, once the directories and exclusions are in,
// what became of each resource of the stacks, where each was imported,
// and whether each stack is safe to delete (see CloudFormationStack).
func (r *Report) finishStacks() {
	if len(r.CloudFormationStacks) == 0 {
		return
	}
	type at struct{ path, address string }
	key := func(scope, typ, id string) string { return scope + "\n" + typ + " " + id }
	imported, leftOut := map[string]at{}, map[string]string{}
	checks := map[string][]string{} // failed checks or the error, by path
	scopeOf := map[string]string{}
	for _, d := range r.Directories {
		for _, res := range d.Imported {
			imported[key(d.Scope, ResourceType(res.Address), res.ID)] = at{d.Path, res.Address}
			scopeOf[d.Path] = d.Scope
		}
		for _, res := range d.LeftOut {
			leftOut[key(d.Scope, ResourceType(res.Address), res.ID)] = strings.Join(res.Errors, "; ")
		}
		if d.Error != "" {
			checks[d.Path] = append(checks[d.Path], "failed: "+d.Error)
		}
		for _, c := range d.Checks {
			if !c.Passed {
				checks[d.Path] = append(checks[d.Path], c.Name+" failed")
			}
		}
	}
	excluded := map[string]string{}
	for _, e := range r.Excluded {
		excluded[key(e.Scope, e.Type, e.ID)] = e.Reason
	}
	sort.Slice(r.CloudFormationStacks, func(i, j int) bool {
		a, b := r.CloudFormationStacks[i], r.CloudFormationStacks[j]
		if a.Region != b.Region {
			return a.Region < b.Region
		}
		return a.Name < b.Name
	})
	for i := range r.CloudFormationStacks {
		s := &r.CloudFormationStacks[i]
		s.Blockers = nil
		dir := path.Join(TemplatesDir, s.Region)
		if s.templateBody != "" {
			s.Template = path.Join(dir, s.Name+".template."+s.templateExt)
		}
		if s.retainBody != "" {
			s.RetainTemplate = path.Join(dir, s.Name+".retain.template."+s.templateExt)
		}
		roots := map[string]bool{}
		for j := range s.Resources {
			res := &s.Resources[j]
			sort.Slice(res.Matches, func(a, b int) bool {
				if res.Matches[a].Type != res.Matches[b].Type {
					return res.Matches[a].Type < res.Matches[b].Type
				}
				return res.Matches[a].ID < res.Matches[b].ID
			})
			res.Outcome, res.Reason = r.stackOutcome(s, res, func(m *CloudFormationMatch) (string, string) {
				k := key(m.Scope, m.Type, m.ID)
				if where, ok := imported[k]; ok {
					m.Path, m.Address = where.path, where.address
					roots[where.path] = true
					return StackImported, ""
				}
				if errs, ok := leftOut[k]; ok {
					return StackNotImported, "left out: " + errs
				}
				if reason, ok := excluded[k]; ok {
					switch OwnerOf(reason) {
					case OwnerSelection, OwnerTerraformState:
						return StackExcluded, reason
					}
					return StackNotImported, "excluded: " + reason
				}
				return StackNotImported, "not imported, as its root or its service failed"
			})
			if s.Deleted {
				continue
			}
			switch res.Outcome {
			case StackNotImported:
				s.Blockers = append(s.Blockers, fmt.Sprintf("%s (%s) isn't imported, %s", res.LogicalID, res.Type, res.Reason))
			case StackNotFound, StackNotImportable:
				s.Blockers = append(s.Blockers, fmt.Sprintf("%s (%s) %s", res.LogicalID, res.Type, res.Reason))
			}
			if len(res.Retain) > 0 {
				s.Blockers = append(s.Blockers, fmt.Sprintf("%s (%s) isn't retained: %s", res.LogicalID, res.Type, strings.Join(res.Retain, "; ")))
			}
		}
		if s.Deleted {
			s.SafeToDelete = false
			continue
		}
		paths := make([]string, 0, len(roots))
		for p := range roots {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			if failed := checks[p]; len(failed) > 0 {
				s.Blockers = append(s.Blockers, fmt.Sprintf("the gate of `%s`, which imports some of its resources, didn't pass (%s), so its plan may change them", p, strings.Join(failed, ", ")))
			}
		}
		if len(r.Failures) > 0 {
			s.Blockers = append(s.Blockers, "the import had failures (see Failures), so what it found of the stack may be incomplete")
		}
		s.SafeToDelete = len(s.Blockers) == 0
	}
}

// stackOutcome returns what became of res, a resource of s, and why:
// outcome gives that of each listed resource it is, and the worst of
// them counts.
func (r *Report) stackOutcome(s *CloudFormationStack, res *CloudFormationResource, outcome func(*CloudFormationMatch) (string, string)) (string, string) {
	switch {
	case res.Outcome == StackNested || res.Outcome == StackOnlyInCloudFormation:
		return res.Outcome, res.NotImportable
	case strings.HasPrefix(res.Status, "DELETE_COMPLETE"):
		return StackDeleted, "CloudFormation deleted it"
	case res.NotImportable != "":
		return StackNotImportable, "can't be imported: " + res.NotImportable
	case len(res.Matches) == 0:
		return StackNotFound, "wasn't found: nothing listed has its physical ID " + res.PhysicalID + "; import it by hand, or check that --services lists its type"
	}
	worst, why := StackImported, ""
	rank := map[string]int{StackImported: 0, StackExcluded: 1, StackNotImported: 2}
	for i := range res.Matches {
		o, reason := outcome(&res.Matches[i])
		if rank[o] > rank[worst] {
			worst, why = o, fmt.Sprintf("as %s %s: %s", res.Matches[i].Type, res.Matches[i].ID, reason)
		}
	}
	return worst, why
}

// writeTemplates writes the templates of the stacks (see
// CloudFormationStack.Template) under outputDir, in a directory only the
// user can read.
func (r *Report) writeTemplates(outputDir string) error {
	for _, s := range r.CloudFormationStacks {
		for _, f := range []struct{ path, body string }{{s.Template, s.templateBody}, {s.RetainTemplate, s.retainBody}} {
			if f.path == "" || f.body == "" {
				continue
			}
			file := filepath.Join(outputDir, filepath.FromSlash(f.path))
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				return err
			}
			if err := fsutil.WriteFile(file, []byte(f.body), 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

// stacksMarkdown renders the stacks for the report.
func (r *Report) stacksMarkdown(b *strings.Builder) {
	if len(r.CloudFormationStacks) == 0 {
		return
	}
	b.WriteString("\n## CloudFormation stacks\n\nThe resources of the stacks `--cfn-stack` names, and their nested stacks, and whether each stack can be deleted without deleting what was imported: every resource imported, or left out by the selection file, every root that imports them passing its gate, so that its plan changes nothing, and `DeletionPolicy` and `UpdateReplacePolicy` `Retain` on every resource. infraharvest changes no stack: update it with the copy of its template that has `Retain` added, check the change set, apply the roots, then delete the stack.\n")
	for _, s := range r.CloudFormationStacks {
		fmt.Fprintf(b, "\n### `%s` (%s)\n\n`%s`, %s", s.Name, s.Region, s.ID, s.Status)
		if s.Parent != "" {
			fmt.Fprintf(b, ", nested in `%s`", s.Parent)
		}
		b.WriteString(".\n\n")
		for _, w := range s.Warnings {
			fmt.Fprintf(b, "Warning: %s.\n\n", w)
		}
		switch {
		case s.Deleted:
			b.WriteString("Deleted already: the resources it retained are imported, and there is nothing left to delete.\n\n")
		case s.SafeToDelete:
			b.WriteString("**Safe to delete**: every resource is imported or left out by the selection file, the plans of the roots that import them change nothing, and every resource is retained. Apply the roots first.\n\n")
		default:
			b.WriteString("**Not safe to delete yet**:\n\n")
			for _, blocker := range s.Blockers {
				fmt.Fprintf(b, "- %s\n", blocker)
			}
			b.WriteString("\n")
		}
		if s.Template != "" {
			fmt.Fprintf(b, "Template: `%s`.", s.Template)
			if s.RetainTemplate != "" && !s.Deleted {
				fmt.Fprintf(b, " With `Retain` added where it lacks it: `%s`; update the stack with it (`aws cloudformation update-stack --stack-name %s --template-body file://%s --region %s`, with the capabilities and parameters it was created with), before deleting the stack.", s.RetainTemplate, s.Name, s.RetainTemplate, s.Region)
			}
			b.WriteString("\n\n")
		}
		b.WriteString("| Logical ID | Type | Physical ID | Outcome | Terraform | Retained |\n|---|---|---|---|---|---|\n")
		for _, res := range s.Resources {
			var addresses []string
			for _, m := range res.Matches {
				if m.Address != "" {
					addresses = append(addresses, fmt.Sprintf("`%s` in `%s`", m.Address, m.Path))
				}
			}
			outcome := strings.ReplaceAll(res.Outcome, "_", " ")
			if res.Reason != "" && res.Outcome != StackImported {
				outcome += ": " + res.Reason
			}
			retained := "yes"
			if len(res.Retain) > 0 {
				retained = "no: " + strings.Join(res.Retain, "; ")
			}
			fmt.Fprintf(b, "| `%s` | `%s` | `%s` | %s | %s | %s |\n", res.LogicalID, res.Type, res.PhysicalID, cell(outcome), strings.Join(addresses, ", "), cell(retained))
		}
	}
}

// cell escapes text for a Markdown table cell.
func cell(text string) string {
	return strings.ReplaceAll(text, "|", `\|`)
}
