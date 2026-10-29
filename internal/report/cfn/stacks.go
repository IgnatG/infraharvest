// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package cfn is the report's section on the AWS CloudFormation stacks an
// import is scoped to (AWS --cfn-stack): what became of each stack's
// resources, whether the stack can be deleted without deleting what the
// import brought under Terraform, and the AWS CLI commands that update it
// with the copy of its template that has Retain added.
//
// The aws provider fills the stacks (see Stacks.Add); the report finishes
// them from what the import did with the listed resources (see Results),
// renders them into report.md and writes their templates.
package cfn

import (
	"fmt"
	"log"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// TemplatesDir is where, under the output directory, the report writes the
// templates of the CloudFormation stacks an import is scoped to (see
// Stack): inside .infraharvest, which the output's .gitignore keeps out of
// version control, as a template may hold values that are secret.
const TemplatesDir = ".infraharvest/cloudformation"

// What became of a resource of a CloudFormation stack (see
// Resource.Outcome).
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
	// StackAcknowledged: Terraform can't import it, and --cfn-ignore
	// accepts that the stack is deleted without it.
	StackAcknowledged = "acknowledged"
	// StackOnlyInCloudFormation: it exists only in CloudFormation, as a
	// wait condition or the CDK's metadata does, so there is nothing to
	// import, and deleting it deletes nothing else.
	StackOnlyInCloudFormation = "only_in_cloudformation"
	// StackNested: a nested stack, whose resources are its own stack's.
	StackNested = "nested_stack"
	// StackDeleted: CloudFormation deleted it, with its stack or before.
	StackDeleted = "deleted"
)

// Stack is a CloudFormation stack an import is scoped to (AWS
// --cfn-stack), or a stack nested in one, with each of its resources,
// what became of it, and whether the stack can be deleted without
// deleting what the import brought under Terraform.
//
// It is also the seam for translating the stack's template into Terraform
// (its Parameters into variables, its Outputs into outputs, its
// Conditions): the template, after transforms, is at Template, and each
// resource maps its logical ID to the addresses it was imported at
// (Resource.Matches), so a Ref or Fn::GetAtt of a logical ID resolves to
// a Terraform address. Both are in report.json and the JSON report.
type Stack struct {
	Name   string `json:"name"`
	ID     string `json:"id"`
	Region string `json:"region"`
	Status string `json:"status"`
	// Parent is the ID of the stack this one is nested in, if any.
	Parent string `json:"parent,omitempty"`
	// Root is the ID of the top-level stack a nested stack is nested in,
	// NestedAs the logical ID of its parent's AWS::CloudFormation::Stack
	// resource that creates it, and TemplateURL that resource's, where it
	// is a string rather than a function's value: AWS advises updating a
	// nested stack only through its root, which reads its template from
	// there (see nestedUpdate).
	Root        string `json:"root,omitempty"`
	NestedAs    string `json:"nested_as,omitempty"`
	TemplateURL string `json:"template_url,omitempty"`
	// Deleted says CloudFormation deleted the stack already: what it
	// retained is imported, and nothing is left to delete.
	Deleted bool `json:"deleted,omitempty"`
	// Unreadable says why a nested stack couldn't be read, as when its
	// deleted parent deleted it longer ago than CloudFormation keeps it,
	// or the role may not describe it: what it holds is unknown, so
	// neither it nor a stack it is nested in is safe to delete.
	Unreadable string `json:"unreadable,omitempty"`
	// Warnings are about the stack, such as a service that manages it.
	Warnings []string `json:"warnings,omitempty"`
	// Template and RetainTemplate are the paths, relative to the output
	// directory, of the stack's template and of a copy with Retain added
	// where a resource lacks it (see TemplatesDir), which the stack can be
	// updated with before it is deleted. RetainTemplate is empty when no
	// resource lacks it.
	Template       string `json:"template,omitempty"`
	RetainTemplate string `json:"retain_template,omitempty"`
	// RetainTemplateBytes is the size of the copy at RetainTemplate, which
	// says how the stack is updated with it (see updateCommand), also for
	// a report read back, which has no body.
	RetainTemplateBytes int        `json:"retain_template_bytes,omitempty"`
	Resources           []Resource `json:"resources"`
	// SafeToDelete says deleting the stack deletes nothing the import
	// brought under Terraform: every resource was imported or left out by
	// people's decision, every root's gate passed, so their plans change
	// nothing, and every resource is retained. Blockers say what keeps it
	// from being so, for a stack that isn't deleted yet.
	SafeToDelete bool     `json:"safe_to_delete"`
	Blockers     []string `json:"blockers,omitempty"`

	// templateBody and retainBody are the templates WriteTemplates writes
	// to Template and RetainTemplate: a report read back has none.
	templateBody, retainBody string
	templateExt              string
	// parameters are the values the stack's parameters have, by name, as
	// DescribeStacks gives them (see SetParameters): not in the report,
	// which a person may share, as a value may be one only the stack's
	// owners should see. resolved are the values the SSM parameter types
	// resolved to, kept out for the same reason.
	parameters, resolved map[string]string
}

// SetTemplates sets the stack's template body, and that of the copy with
// Retain added ("" when none is needed), which WriteTemplates writes, with
// their extension (json or yaml).
func (s *Stack) SetTemplates(body, retain, ext string) {
	s.templateBody, s.retainBody, s.templateExt = body, retain, ext
}

// TemplateBody returns the stack's template, as SetTemplates set it, or
// "" for a report read back.
func (s *Stack) TemplateBody() string {
	return s.templateBody
}

// SetParameters sets the values the stack's parameters have, by name, as
// the stack was given them: a NoEcho parameter's is masked, as
// CloudFormation gives it, and an SSM parameter type's is the SSM
// parameter's name, whose value resolved has.
func (s *Stack) SetParameters(values, resolved map[string]string) {
	s.parameters, s.resolved = values, resolved
}

// Parameters returns the values SetParameters set, or nil.
func (s *Stack) Parameters() map[string]string {
	return s.parameters
}

// ResolvedParameters returns the values SetParameters set for the SSM
// parameter types, as CloudFormation resolved them, or nil.
func (s *Stack) ResolvedParameters() map[string]string {
	return s.resolved
}

// Resource is a resource of a CloudFormation stack.
type Resource struct {
	LogicalID  string `json:"logical_id"`
	Type       string `json:"type"`
	PhysicalID string `json:"physical_id,omitempty"`
	// Status is the resource's status in the stack, such as
	// CREATE_COMPLETE, or DELETE_SKIPPED for one a deleted stack retained.
	Status string `json:"status"`
	// NotImportable says why Terraform can't import it, for an outcome of
	// StackNotImportable or StackOnlyInCloudFormation.
	NotImportable string `json:"not_importable,omitempty"`
	// MustRemove says deleting it destroys what other resources hold, such
	// as the CDK's Custom::S3AutoDeleteObjects a bucket's objects: it must
	// be taken out of the stack before the stack is deleted, as
	// NotImportable says how. Acknowledged says --cfn-ignore accepts that
	// Terraform can't import it.
	MustRemove   bool `json:"must_remove,omitempty"`
	Acknowledged bool `json:"acknowledged,omitempty"`
	// Retain says what keeps it from outliving the stack: a DeletionPolicy
	// or an UpdateReplacePolicy that isn't Retain. None means it is
	// retained.
	Retain []string `json:"retain,omitempty"`
	// Matches are the listed resources it is, with where each was imported
	// (see Match).
	Matches []Match `json:"matches,omitempty"`
	// Outcome is what became of it, one of the Stack constants, and Reason
	// says why, where it wasn't imported.
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

// Match is a listed resource that is a resource of a stack: where it was
// listed, its Terraform type and import ID and, once the report is
// finished, the root it was imported into and its address there.
type Match struct {
	Scope   string `json:"scope,omitempty"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Path    string `json:"path,omitempty"`
	Address string `json:"address,omitempty"`
}

// Stacks are the CloudFormation stacks an import is scoped to, and those
// nested in them, as the report has them.
type Stacks []Stack

// Add records the stacks an Import call is scoped to, with the listed
// resources it found of each: a stack another call of the import recorded
// already gets the call's matches added, as the global call finds a
// stack's IAM roles and the regional one its buckets.
func (all *Stacks) Add(stacks []Stack) {
	for _, s := range stacks {
		i := slices.IndexFunc(*all, func(have Stack) bool { return have.ID == s.ID })
		if i < 0 {
			s.Resources = slices.Clone(s.Resources)
			for j := range s.Resources {
				s.Resources[j].Matches = slices.Clone(s.Resources[j].Matches)
			}
			*all = append(*all, s)
			continue
		}
		have := &(*all)[i]
		for _, res := range s.Resources {
			j := slices.IndexFunc(have.Resources, func(h Resource) bool { return h.LogicalID == res.LogicalID })
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

// Results is what an import did with the resources it listed, by where
// each was listed, its Terraform type and its import ID, which the report
// knows once its directories and exclusions are in: Finish works out from
// it what became of each resource of the stacks.
type Results interface {
	// Imported returns the root the resource was imported into, and its
	// address there.
	Imported(scope, typ, id string) (path, address string, ok bool)
	// LeftOut returns why the resource was left out of the root it was
	// generated in, such as the errors that rejected it.
	LeftOut(scope, typ, id string) (why string, ok bool)
	// Excluded returns why the selection left the resource out, and
	// whether that was people's decision or Terraform already manages it,
	// rather than the default selection's.
	Excluded(scope, typ, id string) (reason string, decided, ok bool)
	// FailedChecks returns what failed in the root at path: its error, or
	// each check of its gate that didn't pass.
	FailedChecks(path string) []string
	// Failed reports whether the import had failures.
	Failed() bool
}

// Finish works out, once the directories and exclusions are in, what
// became of each resource of the stacks, where each was imported, and
// whether each stack is safe to delete (see Stack).
func (all Stacks) Finish(results Results) {
	if len(all) == 0 {
		return
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.Region != b.Region {
			return a.Region < b.Region
		}
		return a.Name < b.Name
	})
	// Two stacks of a region can share a name, as a deleted one, named by
	// its stack ID, and the live one created again with its name: their
	// templates' files take the stack ID's last part too, so that neither
	// overwrites the other's.
	named := map[string]int{}
	for _, s := range all {
		named[s.Region+"/"+s.Name]++
	}
	for i := range all {
		s := &all[i]
		s.Blockers = nil
		dir := path.Join(TemplatesDir, s.Region)
		base := s.Name
		if named[s.Region+"/"+s.Name] > 1 {
			base += "-" + path.Base(s.ID)
		}
		if s.templateBody != "" {
			s.Template = path.Join(dir, base+".template."+s.templateExt)
		}
		if s.retainBody != "" {
			s.RetainTemplate = path.Join(dir, base+".retain.template."+s.templateExt)
			s.RetainTemplateBytes = len(s.retainBody)
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
			res.Outcome, res.Reason = stackOutcome(res, func(m *Match) (string, string) {
				if p, address, ok := results.Imported(m.Scope, m.Type, m.ID); ok {
					m.Path, m.Address = p, address
					roots[p] = true
					return StackImported, ""
				}
				if errs, ok := results.LeftOut(m.Scope, m.Type, m.ID); ok {
					return StackNotImported, "left out: " + errs
				}
				if reason, decided, ok := results.Excluded(m.Scope, m.Type, m.ID); ok {
					if decided {
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
			case StackNotImportable:
				if res.MustRemove {
					s.Blockers = append(s.Blockers, fmt.Sprintf("%s (%s) must be taken out of the stack before the stack is deleted, and --cfn-ignore can't acknowledge it: it is %s", res.LogicalID, res.Type, res.NotImportable))
					break
				}
				s.Blockers = append(s.Blockers, fmt.Sprintf("%s (%s) %s", res.LogicalID, res.Type, res.Reason))
			case StackNotFound:
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
		if s.Unreadable != "" {
			s.Blockers = append(s.Blockers, "it couldn't be read, so what it holds, which deleting it may delete, is unknown: "+s.Unreadable)
		}
		paths := make([]string, 0, len(roots))
		for p := range roots {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			if failed := results.FailedChecks(p); len(failed) > 0 {
				s.Blockers = append(s.Blockers, fmt.Sprintf("the gate of `%s`, which imports some of its resources, didn't pass (%s), so its plan may change them", p, strings.Join(failed, ", ")))
			}
		}
		if results.Failed() {
			s.Blockers = append(s.Blockers, "the import had failures (see Failures), so what it found of the stack may be incomplete")
		}
		s.SafeToDelete = len(s.Blockers) == 0
	}
	// Deleting a stack deletes the stacks nested in it, so what keeps one of
	// those from being deleted keeps each stack it is nested in too.
	own := map[string][]string{}
	byID := map[string]*Stack{}
	for i := range all {
		s := &all[i]
		own[s.ID], byID[s.ID] = slices.Clone(s.Blockers), s
	}
	for _, s := range all {
		for parent := byID[s.Parent]; parent != nil && !parent.Deleted; parent = byID[parent.Parent] {
			for _, blocker := range own[s.ID] {
				parent.Blockers = append(parent.Blockers, "in nested stack "+s.Name+": "+blocker)
			}
			parent.SafeToDelete = parent.SafeToDelete && len(own[s.ID]) == 0
		}
	}
}

// stackOutcome returns what became of res, a resource of a stack, and why:
// outcome gives that of each listed resource it is, and the worst of
// them counts.
func stackOutcome(res *Resource, outcome func(*Match) (string, string)) (string, string) {
	switch {
	case res.Outcome == StackNested || res.Outcome == StackOnlyInCloudFormation:
		return res.Outcome, res.NotImportable
	case strings.HasPrefix(res.Status, "DELETE_COMPLETE"):
		return StackDeleted, "CloudFormation deleted it"
	case res.NotImportable != "" && res.Acknowledged:
		return StackAcknowledged, "by --cfn-ignore, as " + res.NotImportable
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

// WriteTemplates writes the templates of the stacks (see Stack.Template)
// under outputDir, in a directory only the user can read.
func (all Stacks) WriteTemplates(outputDir string) error {
	for _, s := range all {
		for _, f := range []struct{ path, body string }{{s.Template, s.templateBody}, {s.RetainTemplate, s.retainBody}} {
			if f.path == "" || f.body == "" {
				continue
			}
			file := filepath.Join(outputDir, filepath.FromSlash(f.path))
			// Private as it is created: on Windows, where a mode restricts
			// nothing, its access list does it.
			if err := fsutil.MkdirPrivate(filepath.Dir(file)); err != nil {
				return err
			}
			if err := fsutil.WriteFile(file, []byte(f.body), 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

// Log logs, for each stack, whether it is safe to delete, and what keeps
// it from being so.
func (all Stacks) Log() {
	for _, s := range all {
		switch {
		case s.Deleted:
			log.Printf("CloudFormation stack %s: deleted already; what it retained is imported", s.Name)
		case s.SafeToDelete:
			log.Printf("CloudFormation stack %s: safe to delete once the roots are applied: every resource is imported, left out by the selection file or acknowledged, the plans change nothing, and every resource is retained", s.Name)
		default:
			log.Printf("CloudFormation stack %s: not safe to delete yet:\n- %s", s.Name, strings.Join(s.Blockers, "\n- "))
			if s.RetainTemplate != "" {
				log.Printf("CloudFormation stack %s: a copy of its template with Retain added where it lacks it is in %s: update the stack with it before deleting it", s.Name, s.RetainTemplate)
			}
		}
	}
}
