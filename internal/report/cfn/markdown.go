// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Markdown renders the stacks for report.md, nothing when there are none.
func (all Stacks) Markdown(b *strings.Builder) {
	if len(all) == 0 {
		return
	}
	b.WriteString("\n## CloudFormation stacks\n\nThe resources of the stacks `--cfn-stack` names, and their nested stacks, and whether each stack can be deleted without deleting what was imported: every resource imported, left out by the selection file or, where Terraform can't import it, acknowledged with `--cfn-ignore`, every root that imports them passing its gate, so that its plan changes nothing, and `DeletionPolicy` and `UpdateReplacePolicy` `Retain` on every resource. infraharvest changes no stack: update it with the copy of its template that has `Retain` added, a nested stack through its root stack, check the change set, apply the roots, then delete the stack.\n")
	for _, s := range all {
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
			case all.throughRoot(s):
				fmt.Fprintf(b, " With `Retain` added where it lacks it: `%s`; AWS advises updating a nested stack only through its root stack, so %s, before deleting the stack.", s.RetainTemplate, all.nestedUpdate(s))
			default:
				first := ""
				if slices.ContainsFunc(all, func(n Stack) bool {
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
func (s *Stack) updateCommand() string {
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

// stack returns the stack whose ID is id, nil if none is.
func (all Stacks) stack(id string) *Stack {
	i := slices.IndexFunc(all, func(s Stack) bool { return s.ID == id })
	if i < 0 {
		return nil
	}
	return &all[i]
}

// throughRoot reports whether s is a nested stack to be updated through
// its root stack: one no deleted stack retained (DELETE_SKIPPED), which
// leaves it nested in none that is live, so that it is updated itself.
func (all Stacks) throughRoot(s Stack) bool {
	if s.Parent == "" {
		return false
	}
	for parent := all.stack(s.Parent); parent != nil; parent = all.stack(parent.Parent) {
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
func (all Stacks) nestedUpdate(s Stack) string {
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
	root := all.stack(s.Root)
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
