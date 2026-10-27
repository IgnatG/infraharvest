// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

// Profile is what sets a provider apart from the others, whatever it
// lists: the facts the importer, and through it the engine, read of it
// rather than tell providers apart by their names. Each provider declares
// its own (see Provider.Profile); a field left zero is a fact the
// provider lacks, and the importer does without it.
type Profile struct {
	// Command is the name of the provider's command, such as azure for
	// azurerm and google for google-beta: the {provider} of --layout and
	// the first part of the selection file's scope, so that the output is
	// laid out by what the user typed. Without it, Name.
	Command string
	// Source is the Terraform provider's registry source address, such as
	// DataDog/datadog, for the required_providers block; hashicorp/<Name>
	// without it.
	Source string
	// NameTag is the tag whose value a resource's block label is made
	// from, which people read it by, such as AWS's Name; without it, or
	// the tag, the name the lister gave it.
	NameTag string
	// ConcurrentListing says the services may list at the same time,
	// because their listers share only state that is safe for concurrent
	// use.
	ConcurrentListing bool
	// DefaultTags, if set, says how the provider applies tags to every
	// resource, so that the engine can move the tags all resources share
	// into the provider configuration.
	DefaultTags *DefaultTags
	// EffectiveTags, if set, says the resources' tag attribute holds only
	// the tags their configuration sets, such as Google's labels.
	EffectiveTags *EffectiveTags
	// InheritedArguments are arguments of the provider block that its
	// resources take as theirs when they don't set them, such as Google's
	// project and region, so the engine leaves them out of the resources
	// that repeat the provider's value.
	InheritedArguments []string
	// Layout says which file of a root each resource goes in.
	Layout Layout
	// Merge is what the root the environments of a stack share reads of
	// each environment's account and region.
	Merge Merge
}

// DefaultTags is how a provider applies tags to every resource.
type DefaultTags struct {
	// Attribute is the resources' tag attribute, such as tags.
	Attribute string
	// Block is the provider-level block that applies tags, such as AWS's
	// default_tags; empty when the provider has none, as azurerm, in
	// which case the shared tags go to a local instead.
	Block string
	// Argument says Block is in fact a map argument of the provider block,
	// like Google's default_labels.
	Argument bool
	// ReservedPrefix starts the keys of tags the cloud reserves, which
	// cannot be applied that way, such as aws:.
	ReservedPrefix string
}

// EffectiveTags describes a provider whose resources' tag attribute holds
// only the tags their configuration sets: an import leaves it empty, and
// the effective attribute has every tag.
type EffectiveTags struct {
	// Attribute is the tag attribute, such as labels, and Effective the
	// one with every tag, such as effective_labels.
	Attribute, Effective string
	// ReservedPrefix starts the keys of tags the cloud or the provider
	// sets itself, which the configuration leaves to it, such as goog-.
	ReservedPrefix string
}

// Layout says which file of a root a resource goes in, beyond the file of
// the service that lists it: the network's resources share network.tf,
// and those an inventory API lists go by their type.
type Layout struct {
	// NetworkServices are the services whose resources make up the
	// network: a VPC is read with its subnets, route tables and gateways.
	NetworkServices []string
	// NetworkTypes are the network's resource types, without their
	// prefix, for services that list through an inventory API: these
	// types, and those whose name starts with one, such as
	// azurerm_private_dns_a_record, or azurerm_route and
	// azurerm_network_security_rule, which go with their route table and
	// security group.
	NetworkTypes []string
	// TypeFamilies are families of resource types, without their prefix,
	// that share a file when a service lists them through an inventory
	// API, by the file's name: the types named after a family, such as
	// azurerm_storage_account and azurerm_storage_container after storage,
	// go in its file, by the longest family they are named after. Where
	// another service lists some of a family's types, the family's file
	// is that service's, such as Google's gcs.tf for buckets and iam.tf
	// for the project's IAM members, which service accounts and custom
	// roles join.
	TypeFamilies map[string]string
}

// Merge is what the root the environments of a stack share reads of each
// environment's account (or subscription, or project) and region, where
// the environments' roots differ by them. The importer hands it to the
// engine as engine.MergeProfile, which has the same fields.
type Merge struct {
	// ScopeTitle is what the roots are scoped by, for the README, such as
	// Account.
	ScopeTitle string
	// ScopeData is the data source that reads the ID of the account,
	// ScopeLocal the local that holds the ID, and ScopeExpr the
	// expression that reads it; all empty for a provider with none.
	ScopeData, ScopeLocal, ScopeExpr string
	// RegionArgument is the argument of the provider block that sets the
	// region of the provider's resources, which differs between the
	// environments, such as AWS's region; empty for a provider whose
	// roots don't read their region from the provider, in which case the
	// other Region fields are empty too.
	RegionArgument string
	// RegionTitle names the region, for the variable that holds it, such
	// as AWS region.
	RegionTitle string
	// RegionData is the data source that reads the region, RegionLocal
	// the local that holds it, which also names the variable that sets
	// the provider's, and RegionExpr the expression that reads it.
	RegionData, RegionLocal, RegionExpr string
}
