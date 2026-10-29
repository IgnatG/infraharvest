// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"regexp"
	"slices"
	"strings"
)

// Traits are what a provider's resources and the values that identify
// them are like, which the provider says (see harvest.Traits): the engine
// knows no provider of its own. The zero value knows none of them, and
// finds what any provider's resources have: IDs, names and their parents.
type Traits struct {
	// IDPrefixes start values that identify a resource on their own, such
	// as ARNs and Azure resource IDs, which are worth naming (see
	// liftLiterals) and referring to wherever they are (see referenceable).
	IDPrefixes []string
	// IDSuffixes end such values, as a Google service account's email
	// ends with its domain.
	IDSuffixes []string
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
}

// OwnerTags are the tags a provider's cloud gives what tools create.
type OwnerTags []OwnerTag

// owner returns the owner the tag key=value says manages what has it, if
// it is one of o.
func (o OwnerTags) owner(key, value string) (Owner, bool) {
	for _, t := range o {
		if t.Key != key || (t.Value != "" && !strings.EqualFold(t.Value, value)) {
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

// referenceable reports whether a value identifies a resource on its own:
// one with an ID's prefix or suffix of t, a URL (an SQS queue's id) or
// a cloud ID such as vpc-0abc1234, not a name another resource could
// share by chance.
func (t Traits) referenceable(value string) bool {
	return t.liftable(value) || strings.HasPrefix(value, "https://") || slices.ContainsFunc(t.IDSuffixes, func(s string) bool { return strings.HasSuffix(value, s) })
}

// cloudID matches the IDs clouds give resources, such as vpc-0abc12de.
var cloudID = regexp.MustCompile(`^[a-z][a-z0-9]*-[0-9a-f]{8,}$`)

// liftable reports whether value is worth naming: an identifier of the
// clouds (vpc-0abc12de, or one with an ID's prefix of t), not a word or
// a setting such as "Enabled" or "tcp".
func (t Traits) liftable(value string) bool {
	return slices.ContainsFunc(t.IDPrefixes, func(p string) bool { return strings.HasPrefix(value, p) }) || cloudID.MatchString(value)
}

// wellKnown reports whether value is an ID that reads better where it is
// used than as a local (see WellKnownIDs).
func (t Traits) wellKnown(value string) bool {
	return t.WellKnownIDs != nil && t.WellKnownIDs.MatchString(value)
}

// isARN reports whether value is an ARN (see ARNPrefix).
func (t Traits) isARN(value string) bool {
	return t.ARNPrefix != "" && strings.HasPrefix(value, t.ARNPrefix)
}

// unreferencedMap reports whether the map of the argument name holds no
// references: tags and labels describe a resource.
func (t Traits) unreferencedMap(name string) bool {
	return slices.Contains(t.TagAttributes, name) || slices.Contains(t.MergedTagAttributes, name)
}
