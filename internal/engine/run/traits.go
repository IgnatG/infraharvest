// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package run

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// Distinctive reports whether value identifies a resource on its own
// (see Traits.Referenceable), or looks like a generated ID: six characters or
// more, letters and digits, no spaces.
func (t Traits) Distinctive(value string) bool {
	if t.Referenceable(value) {
		return true
	}
	return len(value) >= 6 && strings.ContainsFunc(value, unicode.IsLetter) && strings.ContainsFunc(value, unicode.IsDigit) && !strings.ContainsFunc(value, unicode.IsSpace)
}

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
// Terraform tag set to true, as the Terraform AWS modules' examples set; or
// one of o, the tags a provider's cloud gives what tools create, such as
// the one CloudFormation gives what its stacks create. Importing such a
// resource again would give it two owners.
func (o OwnerTags) OwnedBy(tags map[string]string) (Owner, bool) {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := tags[k]
		tag := k + "=" + v
		if owner, ok := o.owner(k, v); ok {
			return owner, true
		}
		switch normalized := normalizeKey(k); {
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

// Traits are what a provider's resources and the values that identify
// them are like, which the provider says (see harvest.Traits): the engine
// knows no provider of its own. The zero value knows none of them, and
// finds what any provider's resources have: IDs, names and their parents.
type Traits struct {
	// IDPrefixes start values that identify a resource on their own, such
	// as ARNs and Azure resource IDs, which are worth naming (see
	// liftLiterals) and referring to wherever they are (see Referenceable).
	IDPrefixes []string
	// IDSuffixes end such values, as a Google service account's email
	// ends with its domain.
	IDSuffixes []string
	// IDSegments are the parts that make a value, wherever they are in it,
	// a path or a link whose own parts are IDs, such as a Google Cloud
	// projects/ and an Azure /subscriptions/ (see HoldsIDs).
	IDSegments []string
	// ARNPrefix, if set, starts the ARNs of resources whose IDs may be
	// other values: a value with it names a resource by its arn attribute,
	// as Options.ARNs say how (see rootValues.byARN).
	ARNPrefix string
	// WellKnownIDs, if set, matches IDs that read better where they are
	// used than as a local, such as cloud-managed policies, whose names
	// say what they grant.
	WellKnownIDs *regexp.Regexp
	// SelfLinkAliases map the start of a self link, as the provider writes
	// it, to another start some APIs name the same resource by.
	SelfLinkAliases map[string]string
	// PrimaryIdentities are the attributes that identify the resource
	// that has them first, ahead of all others, such as an ARN or a self
	// link (see identityAttributes).
	PrimaryIdentities []string
	// IdentityAttributes are the attributes, in the order a reference
	// prefers them, that identify the resource that has them where the
	// provider computes them, such as a load balancer's DNS name, besides
	// those every resource has (see identityIndex).
	IdentityAttributes []string
	// NestedIdentities are, by block, the attributes of nested blocks
	// that identify the resource that has them, such as the principal of
	// a resource's managed identity. The provider computes them, and a
	// resource has one such block at most.
	NestedIdentities map[string]string
	// NameAttributes are the attributes that hold a resource's name
	// besides name, such as a Lambda function's function_name, which
	// resources of other types may share (see identityAttributes).
	NameAttributes []string
	// OwnerTags are tags or labels the cloud or the provider gives what a
	// tool creates, which say that tool manages it (see OwnerTags.OwnedBy).
	OwnerTags OwnerTags
	// TagAttributes are the arguments whose maps are a resource's tags or
	// labels, in the order their keys win, and MergedTagAttributes those
	// that merge them with tags from elsewhere. Neither holds references,
	// even where a value is another resource's ID.
	TagAttributes       []string
	MergedTagAttributes []string
}

// OwnerTag is a tag that says which tool manages what has it.
type OwnerTag struct {
	// Key is the tag's key, and Value, if set, the value it must have,
	// whatever the case.
	Key, Value string
	// Tool is the tool it names (see Owner.Tool).
	Tool string
	// Stack says the tag's value is the stack the tool manages it in.
	Stack bool
	// KeyPrefix says Key starts the tag's key rather than being all of
	// it, as kubernetes.io/cluster/ starts kubernetes.io/cluster/<name>.
	KeyPrefix bool
}

// OwnerTags are the tags a provider's cloud gives what tools create.
type OwnerTags []OwnerTag

// owner returns the owner the tag key=value says manages what has it, if
// it is one of o.
func (o OwnerTags) owner(key, value string) (Owner, bool) {
	for _, t := range o {
		matches := key == t.Key
		if t.KeyPrefix {
			matches = len(key) > len(t.Key) && strings.HasPrefix(key, t.Key)
		}
		if !matches || (t.Value != "" && !strings.EqualFold(t.Value, value)) {
			continue
		}
		owner := Owner{Tool: t.Tool, Tag: key + "=" + value}
		if t.Stack {
			owner.Stack = value
		}
		return owner, true
	}
	return Owner{}, false
}

// Referenceable reports whether a value identifies a resource on its own:
// one with an ID's prefix or suffix of t, a URL (an SQS queue's id) or
// a cloud ID such as vpc-0abc1234, not a name another resource could
// share by chance.
func (t Traits) Referenceable(value string) bool {
	return t.Liftable(value) || strings.HasPrefix(value, "https://") || slices.ContainsFunc(t.IDSuffixes, func(s string) bool { return strings.HasSuffix(value, s) })
}

// cloudID matches the IDs clouds give resources, such as vpc-0abc12de.
var cloudID = regexp.MustCompile(`^[a-z][a-z0-9]*-[0-9a-f]{8,}$`)

// Liftable reports whether value is worth naming: an identifier of the
// clouds (vpc-0abc12de, or one with an ID's prefix of t), not a word or
// a setting such as "Enabled" or "tcp".
func (t Traits) Liftable(value string) bool {
	return slices.ContainsFunc(t.IDPrefixes, func(p string) bool { return strings.HasPrefix(value, p) }) || cloudID.MatchString(value)
}

// WellKnown reports whether value is an ID that reads better where it is
// used than as a local (see WellKnownIDs).
func (t Traits) WellKnown(value string) bool {
	return t.WellKnownIDs != nil && t.WellKnownIDs.MatchString(value)
}

// HoldsIDs reports whether value is an ARN, a URL, or a path that has an
// ID's own parts, the IDs in which are told from the parts of a name (see
// scopeWordIndexFrom): it starts with one of IDPrefixes, has "://", or has
// one of IDSegments.
func (t Traits) HoldsIDs(value string) bool {
	return slices.ContainsFunc(t.IDPrefixes, func(p string) bool { return strings.HasPrefix(value, p) }) ||
		strings.Contains(value, "://") ||
		slices.ContainsFunc(t.IDSegments, func(s string) bool { return strings.Contains(value, s) })
}

// IsARN reports whether value is an ARN (see ARNPrefix).
func (t Traits) IsARN(value string) bool {
	return t.ARNPrefix != "" && strings.HasPrefix(value, t.ARNPrefix)
}

// UnreferencedMap reports whether the map of the argument name holds no
// references: tags and labels describe a resource.
func (t Traits) UnreferencedMap(name string) bool {
	return slices.Contains(t.TagAttributes, name) || slices.Contains(t.MergedTagAttributes, name)
}
