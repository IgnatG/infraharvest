// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	tfjson "github.com/hashicorp/terraform-json"
)

// Owner is the tool a resource's tags or labels say manages it, and the
// tag that says so.
type Owner struct {
	// Tool is Terraform, OpenTofu, Pulumi, CloudFormation, ... Terraform
	// and OpenTofu mean another state manages the resource.
	Tool string
	// Stack is the CloudFormation stack, when a tag names it.
	Stack string `json:",omitempty"`
	// Tag is the tag or label that says so, as key=value.
	Tag string
}

// TerraformState reports whether the owner is a Terraform or OpenTofu
// state.
func (o Owner) TerraformState() bool {
	return o.Tool == "Terraform" || o.Tool == "OpenTofu"
}

// ownerTools are the values of a managed-by tag that name a tool, by the
// tool's name in lower case.
var ownerTools = map[string]string{
	"terraform":      "Terraform",
	"opentofu":       "OpenTofu",
	"tofu":           "OpenTofu",
	"pulumi":         "Pulumi",
	"cloudformation": "CloudFormation",
	"cdk":            "the AWS CDK",
	"cdktf":          "CDK for Terraform",
	"crossplane":     "Crossplane",
}

// ownerKeys are tag keys whose value names the tool that manages a
// resource, once lower-cased and stripped of separators: ManagedBy,
// managed-by, managed_by, provisioned-by.
var ownerKeys = map[string]bool{"managedby": true, "provisionedby": true}

// OwnedBy returns the tool tags (or labels) say manages a resource, if they
// name one: a ManagedBy, managed-by or managed_by tag (any case) naming
// Terraform, OpenTofu, Pulumi, CloudFormation, the CDK or Crossplane; a
// Terraform tag set to true, as the Terraform AWS modules' examples set; the
// aws:cloudformation:stack-name tag CloudFormation gives what its stacks
// create; or the goog-terraform-provisioned label the Google provider gives
// what Terraform creates. Importing such a resource again would give it two
// owners.
func OwnedBy(tags map[string]string) (Owner, bool) {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := tags[k]
		tag := k + "=" + v
		switch normalized := normalizeKey(k); {
		case k == "aws:cloudformation:stack-name":
			return Owner{Tool: "CloudFormation", Stack: v, Tag: tag}, true
		case k == "goog-terraform-provisioned" && strings.EqualFold(v, "true"):
			return Owner{Tool: "Terraform", Tag: tag}, true
		case normalized == "terraform" && (strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")):
			return Owner{Tool: "Terraform", Tag: tag}, true
		case ownerKeys[normalized]:
			if tool := toolNamed(v); tool != "" {
				return Owner{Tool: tool, Tag: tag}, true
			}
		}
	}
	return Owner{}, false
}

// normalizeKey lower-cases key and leaves out separators.
func normalizeKey(key string) string {
	var b strings.Builder
	for _, r := range key {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// toolNamed returns the tool value names, as "terraform", "Terraform
// Cloud" or "pulumi/stack-a" do, or "".
func toolNamed(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	end := strings.IndexFunc(v, func(r rune) bool { return !unicode.IsLetter(r) })
	if end >= 0 {
		v = v[:end]
	}
	return ownerTools[v]
}

// OwnedResource is a resource Generate left out because its tags say
// another tool manages it (see OwnedBy).
type OwnedResource struct {
	Address string
	ID      string // the import ID
	Owner   Owner
}

// leaveOutOwned moves out of imports.tf and generated.tf the resources whose
// tags or labels, as Terraform generated them, say another tool manages
// them, but those keep names by "<type> <import ID>": the listing doesn't
// read the tags of every type, such as IAM roles'.
func leaveOutOwned(dir string, keep map[string]bool) ([]OwnedResource, error) {
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	imports, err := loadHCL(filepath.Join(dir, ImportsFileName))
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, imp := range imports.imports() {
		ids[imp.to] = imp.id
	}
	owners := map[string]Owner{}
	errs := map[string][]tfjson.Diagnostic{}
	for _, r := range generated.resources() {
		tags := map[string]string{}
		for _, name := range []string{"tags", "labels", "terraform_labels", "effective_labels"} {
			obj, ok := attributeExpr(r.syntax.Body, name).(*hclsyntax.ObjectConsExpr)
			if !ok {
				continue
			}
			for _, item := range obj.Items {
				if k := objectKey(item.KeyExpr); k != "" {
					tags[k] = stringLiteral(item.ValueExpr)
				}
			}
		}
		typ, _, _ := strings.Cut(r.address, ".")
		if owner, ok := OwnedBy(tags); ok && !keep[typ+" "+ids[r.address]] {
			owners[r.address] = owner
			errs[r.address] = []tfjson.Diagnostic{{Severity: tfjson.DiagnosticSeverityError, Summary: "managed by " + owner.Tool}}
		}
	}
	if len(owners) == 0 {
		return nil, nil
	}
	// Moved like rejected resources, but not kept: another tool owns them.
	var discard bytes.Buffer
	rejections, err := reject(dir, errs, &discard)
	if err != nil {
		return nil, err
	}
	owned := make([]OwnedResource, 0, len(rejections))
	for _, r := range rejections {
		owned = append(owned, OwnedResource{Address: r.Address, ID: r.ID, Owner: owners[r.Address]})
	}
	return owned, nil
}
