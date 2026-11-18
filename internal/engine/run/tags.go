// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package run

// EffectiveTags describes a provider whose resources' tags attribute holds
// only the tags their configuration sets, such as the Google provider's
// labels since its version 5.0: an import leaves it empty, and another
// attribute, such as effective_labels, has every tag the resource has.
// Terraform then generates no tags, so Generate writes them from that
// attribute (see fillEffectiveTags).
type EffectiveTags struct {
	// Attribute is the resources' tags attribute, e.g. labels.
	Attribute string
	// Effective is the attribute with every tag a resource has, e.g.
	// effective_labels.
	Effective string
	// ReservedPrefix starts the keys of tags the cloud or the provider
	// sets itself, e.g. goog-, which Generate leaves out: the tags
	// attribute only manages the tags it has, so those stay as they are.
	ReservedPrefix string
}

// DefaultTags describes how a provider applies tags to every resource it
// manages, such as the AWS provider's default_tags.
type DefaultTags struct {
	// Provider is the provider block's name, e.g. aws.
	Provider string
	// Attribute is the resources' tags attribute, e.g. tags.
	Attribute string
	// Block is the provider block that applies tags to every resource,
	// e.g. default_tags, with the tags in its Attribute. Empty if the
	// provider has none, as azurerm: each resource's tags then refer to
	// the local instead (see referTags).
	Block string
	// Argument says Block is an argument of the provider block that holds
	// the tags, as the Google provider's default_labels, rather than a
	// block holding them in Attribute, as the AWS provider's default_tags.
	Argument bool `json:",omitempty"`
	// ReservedPrefix starts the keys of tags the cloud sets itself, which
	// can't be applied through the provider, e.g. aws:.
	ReservedPrefix string
	// Applied, if set, are tags the provider already applies, such as in
	// the root an incremental import adds to: Generate applies them too,
	// and leaves them out of the resources instead of lifting others.
	Applied map[string]string `json:",omitempty"`
}
