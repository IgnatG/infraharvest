// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
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
	RetainTemplateBytes int                      `json:"retain_template_bytes,omitempty"`
	Resources           []CloudFormationResource `json:"resources"`
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
	// owners should see. resolved are the values the SSM parameter types
	// resolved to, kept out for the same reason.
	parameters, resolved map[string]string
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

// SetParameters sets the values the stack's parameters have, by name, as
// the stack was given them: a NoEcho parameter's is masked, as
// CloudFormation gives it, and an SSM parameter type's is the SSM
// parameter's name, whose value resolved has.
func (s *CloudFormationStack) SetParameters(values, resolved map[string]string) {
	s.parameters, s.resolved = values, resolved
}

// Parameters returns the values SetParameters set, or nil.
func (s *CloudFormationStack) Parameters() map[string]string {
	return s.parameters
}

// ResolvedParameters returns the values SetParameters set for the SSM
// parameter types, as CloudFormation resolved them, or nil.
func (s *CloudFormationStack) ResolvedParameters() map[string]string {
	return s.resolved
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
	// Two stacks of a region can share a name, as a deleted one, named by
	// its stack ID, and the live one created again with its name: their
	// templates' files take the stack ID's last part too, so that neither
	// overwrites the other's.
	named := map[string]int{}
	for _, s := range r.CloudFormationStacks {
		named[s.Region+"/"+s.Name]++
	}
	for i := range r.CloudFormationStacks {
		s := &r.CloudFormationStacks[i]
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
			res.Outcome, res.Reason = stackOutcome(res, func(m *CloudFormationMatch) (string, string) {
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
			if failed := checks[p]; len(failed) > 0 {
				s.Blockers = append(s.Blockers, fmt.Sprintf("the gate of `%s`, which imports some of its resources, didn't pass (%s), so its plan may change them", p, strings.Join(failed, ", ")))
			}
		}
		if len(r.Failures) > 0 {
			s.Blockers = append(s.Blockers, "the import had failures (see Failures), so what it found of the stack may be incomplete")
		}
		s.SafeToDelete = len(s.Blockers) == 0
	}
	// Deleting a stack deletes the stacks nested in it, so what keeps one of
	// those from being deleted keeps each stack it is nested in too.
	own := map[string][]string{}
	byID := map[string]*CloudFormationStack{}
	for i := range r.CloudFormationStacks {
		s := &r.CloudFormationStacks[i]
		own[s.ID], byID[s.ID] = slices.Clone(s.Blockers), s
	}
	for _, s := range r.CloudFormationStacks {
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
func stackOutcome(res *CloudFormationResource, outcome func(*CloudFormationMatch) (string, string)) (string, string) {
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
	b.WriteString("\n## CloudFormation stacks\n\nThe resources of the stacks `--cfn-stack` names, and their nested stacks, and whether each stack can be deleted without deleting what was imported: every resource imported, left out by the selection file or, where Terraform can't import it, acknowledged with `--cfn-ignore`, every root that imports them passing its gate, so that its plan changes nothing, and `DeletionPolicy` and `UpdateReplacePolicy` `Retain` on every resource. infraharvest changes no stack: update it with the copy of its template that has `Retain` added, a nested stack through its root stack, check the change set, apply the roots, then delete the stack.\n")
	for _, s := range r.CloudFormationStacks {
		status := s.Status
		if s.Unreadable != "" {
			status = "unreadable"
		}
		fmt.Fprintf(b, "\n### `%s` (%s)\n\n`%s`, %s", s.Name, s.Region, s.ID, status)
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
			b.WriteString("**Safe to delete**: every resource is imported, left out by the selection file or acknowledged with `--cfn-ignore`, the plans of the roots that import them change nothing, and every resource is retained. Apply the roots first.\n\n")
		default:
			b.WriteString("**Not safe to delete yet**:\n\n")
			for _, blocker := range s.Blockers {
				fmt.Fprintf(b, "- %s\n", blocker)
			}
			b.WriteString("\n")
		}
		if s.Template != "" {
			fmt.Fprintf(b, "Template: `%s`.", s.Template)
			switch {
			case s.RetainTemplate == "" || s.Deleted:
			case r.throughRoot(s):
				fmt.Fprintf(b, " With `Retain` added where it lacks it: `%s`; AWS advises updating a nested stack only through its root stack, so %s, before deleting the stack.", s.RetainTemplate, r.nestedUpdate(s))
			default:
				first := ""
				if slices.ContainsFunc(r.CloudFormationStacks, func(n CloudFormationStack) bool {
					return n.Root == s.ID && n.RetainTemplate != "" && !n.Deleted
				}) {
					first = ", once the copies of the stacks nested in it are uploaded, as their sections say"
				}
				fmt.Fprintf(b, " With `Retain` added where it lacks it: `%s`; update the stack with it (%s, with the capabilities and parameters it was created with)%s, before deleting the stack.", s.RetainTemplate, s.updateCommand(), first)
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

// templateBodyLimit is the largest template, in bytes, UpdateStack takes
// in its request (--template-body); a larger one must be in S3
// (--template-url).
const templateBodyLimit = 51200

// updateCommand returns the AWS CLI commands that update the stack with
// the copy of its template with Retain added: from the file, or, for a
// copy larger than UpdateStack takes in its request, uploaded to a bucket
// of the user's first.
func (s *CloudFormationStack) updateCommand() string {
	if s.RetainTemplateBytes <= templateBodyLimit {
		return fmt.Sprintf("`aws cloudformation update-stack --stack-name %s --template-body %s --region %s`", shellArg(s.Name), shellArg("file://"+s.RetainTemplate), shellArg(s.Region))
	}
	key := path.Base(s.RetainTemplate)
	return fmt.Sprintf("it is larger than the %d bytes `--template-body` takes, so upload it to a bucket of yours first: `aws s3 cp %s s3://<bucket>/%s`, then `aws cloudformation update-stack --stack-name %s --template-url https://<bucket>.s3.%s.amazonaws.com/%s --region %s`", templateBodyLimit, shellArg(s.RetainTemplate), key, shellArg(s.Name), s.Region, key, shellArg(s.Region))
}

// shellSafe matches what a shell takes as one word as it is.
var shellSafe = regexp.MustCompile(`^[A-Za-z0-9._/:=@,+-]+$`)

// shellArg returns value as one word of a shell command: as it is where
// that is safe, else single-quoted, with a quote in it escaped, and with
// any backtick or control character, which would break the Markdown code
// span or the line it is pasted on, dropped.
func shellArg(value string) string {
	if shellSafe.MatchString(value) {
		return value
	}
	value = strings.Map(func(c rune) rune {
		if c == '`' || c < ' ' || c == 0x7f {
			return -1
		}
		return c
	}, value)
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// stack returns the stack of the report whose ID is id, nil if none is.
func (r *Report) stack(id string) *CloudFormationStack {
	i := slices.IndexFunc(r.CloudFormationStacks, func(s CloudFormationStack) bool { return s.ID == id })
	if i < 0 {
		return nil
	}
	return &r.CloudFormationStacks[i]
}

// throughRoot reports whether s is a nested stack to be updated through
// its root stack: one no deleted stack retained (DELETE_SKIPPED), which
// leaves it nested in none that is live, so that it is updated itself.
func (r *Report) throughRoot(s CloudFormationStack) bool {
	if s.Parent == "" {
		return false
	}
	for parent := r.stack(s.Parent); parent != nil; parent = r.stack(parent.Parent) {
		if parent.Deleted {
			return false
		}
	}
	return true
}

// nestedUpdate returns how the nested stack s is updated with its copy
// with Retain added, through its root stack: the copy is uploaded over
// the template its parent's TemplateURL names, and the root updated, with
// its own copy if it has one, else with its template as it is, which
// updates each nested stack whose template changed.
func (r *Report) nestedUpdate(s CloudFormationStack) string {
	resource := "the `AWS::CloudFormation::Stack` resource of its parent that creates it"
	if logicalIDPattern.MatchString(s.NestedAs) {
		resource = fmt.Sprintf("its parent's `%s`", s.NestedAs)
	}
	// The URL is the template's, so it goes into the command only as the
	// S3 URI of a bucket and key that are safe there, never as it is.
	object := "s3://<bucket>/<key>"
	if uri := s3Object(s.TemplateURL); uri != "" {
		object = shellArg(uri)
	}
	upload := fmt.Sprintf("upload it over the template the `TemplateURL` of %s names, `aws s3 cp %s %s`", resource, shellArg(s.RetainTemplate), object)
	root := r.stack(s.Root)
	name := "<root-stack>"
	switch {
	case root != nil:
		name = shellArg(root.Name)
	case s.Root != "":
		name = shellArg(s.Root)
	}
	if root != nil && root.RetainTemplate != "" {
		return fmt.Sprintf("%s, then update the root stack `%s` with its own copy, as its section says, which updates this one from there", upload, name)
	}
	return fmt.Sprintf("%s, then update the root stack `%s`, which updates this one from there: `aws cloudformation update-stack --stack-name %s --use-previous-template --parameters ParameterKey=<name>,UsePreviousValue=true --region %s`, with `ParameterKey=<name>,UsePreviousValue=true` for each of its parameters and the capabilities it was created with", upload, name, name, shellArg(s.Region))
}

// logicalIDPattern matches a CloudFormation logical ID, which is alphanumeric.
var logicalIDPattern = regexp.MustCompile(`^[A-Za-z0-9]+$`)

// s3Bucket and s3Key match an S3 bucket name and the keys whose S3 URI
// is safe in a shell command as it is.
var (
	s3Bucket = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	s3Key    = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// s3Object returns the S3 URI, s3://<bucket>/<key>, of the object an S3
// URL names, virtual-hosted (https://<bucket>.s3.<region>.amazonaws.com/<key>)
// or path-style (https://s3.<region>.amazonaws.com/<bucket>/<key>), ""
// for what isn't one, or has a bucket or key that isn't safe in a shell
// command (see s3Bucket): the URL is a template's, which anyone who can
// update the stack writes.
func s3Object(objectURL string) string {
	u, err := url.Parse(objectURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	host, key := u.Hostname(), strings.TrimPrefix(u.Path, "/")
	if !strings.HasSuffix(host, ".amazonaws.com") {
		return ""
	}
	var bucket string
	if strings.HasPrefix(host, "s3.") || strings.HasPrefix(host, "s3-") {
		bucket, key, _ = strings.Cut(key, "/")
	} else if i := max(strings.LastIndex(host, ".s3."), strings.LastIndex(host, ".s3-")); i > 0 {
		bucket = host[:i]
	}
	if !s3Bucket.MatchString(bucket) || !s3Key.MatchString(key) {
		return ""
	}
	return "s3://" + bucket + "/" + key
}
