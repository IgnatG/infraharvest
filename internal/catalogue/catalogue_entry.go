// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package catalogue

import (
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"
)

// File is one catalogue file: entries for some of a provider's types.
type File struct {
	// Provider is the Terraform provider's local name: aws, google.
	Provider string `yaml:"provider"`
	// Service is the infraharvest service that lists the file's types,
	// unless an entry names another.
	Service string            `yaml:"service,omitempty"`
	Types   map[string]*Entry `yaml:"types"`
}

// Entry is what the catalogue knows about one Terraform resource type.
// Every field is optional; an empty entry changes nothing.
type Entry struct {
	// Service is the infraharvest service that lists the type.
	Service string `yaml:"service,omitempty"`
	// ImportID builds the ID Terraform imports a resource with from what
	// its lister recorded: the first template whose fields all have
	// values (see Template). Without it, the lister's ID is the import ID.
	ImportID Templates `yaml:"import_id,omitempty"`
	// NotImportable says why Terraform can't import the type: the import
	// reports its resources as not importable instead of trying.
	NotImportable string `yaml:"not_importable,omitempty"`
	// Omit names arguments to leave out of generated configuration, or,
	// as name=value, attributes to leave out where they are set to value:
	// deprecated ones whose settings separate resources carry, and ones
	// the provider computes.
	Omit []string `yaml:"omit,omitempty"`
	// Defaults names, as path=value, the values the provider or the cloud
	// gives optional arguments it isn't told, such as a bucket's
	// storage_class=STANDARD, by their path in nested blocks
	// (soft_delete_policy.retention_duration_seconds=604800): the engine
	// leaves out the arguments set to them where the plan stays the same.
	Defaults []string `yaml:"defaults,omitempty"`
	// StateOnly names arguments the provider keeps only in state, such as
	// settings it uses when it deletes a resource: an import leaves them
	// unset, so the verification gate accepts a plan that sets them.
	StateOnly []string `yaml:"state_only,omitempty"`
	// ProviderVersions adds to Omit, Defaults and StateOnly, by a
	// constraint on the provider's version (">= 5.0", "< 5.0"), what holds
	// only for the versions it accepts, such as a default a major release
	// changed.
	ProviderVersions map[string]*Versioned `yaml:"provider_versions,omitempty"`
	// KeepEmpty names optional arguments whose empty value ("", [] or {})
	// the provider documents as a setting of its own, such as one that
	// removes what the cloud adds by default: they stay where they are set
	// empty, which the engine otherwise leaves out.
	KeepEmpty []string `yaml:"keep_empty,omitempty"`
	// Skip lists the resources the default selection leaves out, with the
	// reason, such as the ones the cloud creates itself.
	Skip []SkipRule `yaml:"skip,omitempty"`
	// Children are the types of the separate resources that hold parts of
	// a resource's configuration, imported with it when it has them, such
	// as an S3 bucket's versioning. The provider finds out which a
	// resource has.
	Children []string `yaml:"children,omitempty"`
	// DataSource reads one resource of the type by its import ID, so that
	// configuration can refer to a resource the selection leaves out.
	DataSource *DataSource `yaml:"data_source,omitempty"`
	// ARN is where the ARNs of an AWS type whose IDs aren't ARNs name its
	// resources, to find their tags.
	ARN *ARN `yaml:"arn,omitempty"`
	// CloudControl, for an AWS type listed through the Cloud Control API,
	// is its CloudFormation type.
	CloudControl *CloudControl `yaml:"cloud_control,omitempty"`
	// CloudFormationTypes, for an AWS type, are the CloudFormation types
	// a stack's resource of which the type's lister lists with the stack's
	// physical ID, each with the other Terraform types it lists that
	// resource as, in the order the --cfn-stack mapper tries them (see
	// Catalogue.CloudFormationTypes).
	CloudFormationTypes []CloudFormationType `yaml:"cloudformation_types,omitempty"`
	// ResourceExplorer, for an AWS type, is how AWS Resource Explorer's
	// index names its resources, so that the service that lists it is
	// skipped in a region whose index has none of them; or, for a type
	// that can't exist without it, the resources of the type the others
	// of the service need (see ResourceExplorerTypes).
	ResourceExplorer *ResourceExplorer `yaml:"resource_explorer,omitempty"`
	// Inventory is how the cloud's inventory API names resources of the
	// type: Cloud Asset Inventory for Google, Resource Graph for Azure.
	Inventory *Inventory `yaml:"inventory,omitempty"`
	// Placeholder is for configuration Terraform can't read back, such as
	// a Lambda function's code.
	Placeholder *Placeholder `yaml:"placeholder,omitempty"`
	// Lifecycle is lifecycle settings every resource of the type gets,
	// such as create_before_destroy for what others refer to while it is
	// replaced.
	Lifecycle *Lifecycle `yaml:"lifecycle,omitempty"`
	// Remove, in an overlay of a built-in entry, clears these of its
	// fields, which an overlay can't clear by setting them: data_source,
	// arn, cloud_control, resource_explorer, inventory, placeholder and
	// lifecycle.
	Remove []string `yaml:"remove,omitempty"`

	// SecretWhen, of the fields the engine reads (see ForType), names a
	// sensitive argument that is a secret only where another argument
	// says so.
	SecretWhen *SecretWhen `yaml:"secret_when,omitempty"`
	// BlocksAsList, of the fields the engine reads, is a nested block that
	// only names an element of a list an argument takes instead.
	BlocksAsList *BlocksAsList `yaml:"blocks_as_list,omitempty"`
	// Principal, of the fields the engine reads, is an argument that names
	// an object of the cloud's directory.
	Principal *Principal `yaml:"principal,omitempty"`
	// ModuleReferences, of the fields the engine reads, are the arguments
	// of module calls that name a resource of the type.
	ModuleReferences *ModuleReferences `yaml:"module_references,omitempty"`
	// Container, of the fields the engine reads, says the type's
	// resources hold others.
	Container *Container `yaml:"container,omitempty"`
	// DerivedIdentity, of the fields the engine reads, is the identity the
	// type's resources have of several attributes, as Terraform reads it
	// back in other resources, such as {family}:{revision}; it shares its
	// parsed form, which nothing changes, with copies.
	DerivedIdentity *Template `yaml:"derived_identity,omitempty"`
	// UnstableIDSuffix, of the fields the engine reads, starts the part of
	// the type's import IDs that changes while the resource stays the
	// same, such as azapi's ?api-version=.
	UnstableIDSuffix string `yaml:"unstable_id_suffix,omitempty"`
	// Lookups, of the fields the engine reads, are the data sources that
	// read values of the type's arguments that the cloud or the provider
	// defines; under the type "*", for every type.
	Lookups []Lookup `yaml:"lookups,omitempty"`
	// NamedBy, of the fields the engine reads, are the values other than
	// its ID that name a resource of the type, such as a load balancer's
	// DNS name; under the type "*", for every type.
	NamedBy []NamedBy `yaml:"named_by,omitempty"`
	// RootLookup, of the fields the engine reads, is how a root reads an
	// attribute of a resource of the type that another root manages,
	// wherever the root is.
	RootLookup *RootLookup `yaml:"root_lookup,omitempty"`
}

// removable are the fields an overlay's remove clears, by name.
var removable = map[string]struct {
	set   func(*Entry) bool
	clear func(*Entry)
}{
	"data_source":       {func(e *Entry) bool { return e.DataSource != nil }, func(e *Entry) { e.DataSource = nil }},
	"arn":               {func(e *Entry) bool { return e.ARN != nil }, func(e *Entry) { e.ARN = nil }},
	"cloud_control":     {func(e *Entry) bool { return e.CloudControl != nil }, func(e *Entry) { e.CloudControl = nil }},
	"resource_explorer": {func(e *Entry) bool { return e.ResourceExplorer != nil }, func(e *Entry) { e.ResourceExplorer = nil }},
	"inventory":         {func(e *Entry) bool { return e.Inventory != nil }, func(e *Entry) { e.Inventory = nil }},
	"placeholder":       {func(e *Entry) bool { return e.Placeholder != nil }, func(e *Entry) { e.Placeholder = nil }},
	"lifecycle":         {func(e *Entry) bool { return e.Lifecycle != nil }, func(e *Entry) { e.Lifecycle = nil }},
}

// Lifecycle is lifecycle settings for every resource of a type, with the
// reason, which goes in a comment above them.
type Lifecycle struct {
	CreateBeforeDestroy bool   `yaml:"create_before_destroy"`
	Reason              string `yaml:"reason"`
}

// Versioned is what an entry adds for the provider versions a constraint
// accepts (see Entry.ProviderVersions), as the entry's fields of the same
// names do for every version.
type Versioned struct {
	Omit      []string `yaml:"omit,omitempty"`
	Defaults  []string `yaml:"defaults,omitempty"`
	StateOnly []string `yaml:"state_only,omitempty"`
}

// Placeholder sets arguments that Terraform can't read back from the cloud,
// such as where a Lambda function's code comes from, to placeholders,
// when the generated configuration sets none of OneOf, and makes Terraform
// ignore changes to Ignore: what is deployed stays as it is.
type Placeholder struct {
	// OneOf are the arguments one of which the provider requires.
	OneOf []string `yaml:"one_of"`
	// Set has, by argument, its placeholder value: {argument} in it is
	// the value of one of the resource's string arguments.
	Set map[string]string `yaml:"set"`
	// Ignore are the arguments Terraform then ignores changes to.
	Ignore []string `yaml:"ignore"`
	// Reason says why, in a comment above lifecycle.ignore_changes.
	Reason string `yaml:"reason"`
}

// SkipRule matches resources by their ID or attributes: all the
// conditions it sets must hold. A rule with all set matches every
// resource of the type.
type SkipRule struct {
	Reason string `yaml:"reason"`
	All    bool   `yaml:"all,omitempty"`
	// IDs matches resources with one of these IDs.
	IDs []string `yaml:"ids,omitempty"`
	// IDPrefixes matches resources whose ID starts with one of these.
	IDPrefixes []string `yaml:"id_prefixes,omitempty"`
	// Attributes matches resources whose attributes have these values.
	Attributes map[string]string `yaml:"attributes,omitempty"`
	// AttributesPresent matches resources that have these attributes,
	// whatever their value, such as a label a service sets on what it
	// creates with a value that differs for each.
	AttributesPresent []string `yaml:"attributes_present,omitempty"`
	// AttributePrefixes matches resources whose attributes start with one
	// of these, such as a name the cloud gives what it creates.
	AttributePrefixes map[string][]string `yaml:"attribute_prefixes,omitempty"`
	// AttributeSuffixes matches resources whose attributes end with one of
	// these, such as the domain of a service account the cloud manages.
	AttributeSuffixes map[string][]string `yaml:"attribute_suffixes,omitempty"`
	// IgnoreCase compares IDs and attributes whatever their case, as Azure
	// names are.
	IgnoreCase bool `yaml:"ignore_case,omitempty"`
}

// Inventory is a resource type of a cloud's inventory API, such as
// compute.googleapis.com/Network or Microsoft.Network/networkWatchers, and,
// when the type covers several Terraform types, the pattern of the names
// of the resources of this one: * stands for one segment of the name,
// between slashes. Both match whatever the case.
type Inventory struct {
	Type string `yaml:"type"`
	Name string `yaml:"name,omitempty"`
}

// DataSource is a data source's type and the argument that takes the
// resource's import ID.
type DataSource struct {
	Type     string `yaml:"type"`
	Argument string `yaml:"argument"`
}

// ARN is an ARN's service and the resource type its resource part starts
// with: "" if the part is the name alone, as for an S3 bucket.
type ARN struct {
	Service      string `yaml:"service"`
	ResourceType string `yaml:"resource_type"`
}

// CloudControl is the CloudFormation type Cloud Control lists a type as,
// and the one property of its primary identifier, whose value Cloud
// Control returns as each resource's identifier and which is also the
// Terraform import ID.
type CloudControl struct {
	Type       string `yaml:"type"`
	Identifier string `yaml:"identifier"`
}

// CloudFormationType is a CloudFormation type a stack's resource of which
// is listed with its physical ID as the entry's Terraform type and, in
// Also, as others: an AWS::IAM::ServiceLinkedRole as an
// aws_iam_service_linked_role and an aws_iam_role. In YAML, a type with no
// others is its name alone.
type CloudFormationType struct {
	Type string   `yaml:"type"`
	Also []string `yaml:"also,omitempty"`
}

// UnmarshalYAML reads a CloudFormation type's name alone, or the type with
// the others it is listed as.
func (t *CloudFormationType) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*t = CloudFormationType{}
		return node.Decode(&t.Type)
	}
	type plain CloudFormationType
	return node.Decode((*plain)(t))
}

// ResourceExplorer is how Resource Explorer names the resources of an AWS
// type, service:resource_type, as in ec2:volume. In YAML it is arn, for
// the service and resource type of the entry's arn, or a list of them.
type ResourceExplorer struct {
	// FromARN takes the type from the entry's arn.
	FromARN bool
	// Types are the Resource Explorer types, without FromARN.
	Types []string
}

// UnmarshalYAML reads arn, or a list of Resource Explorer types.
func (r *ResourceExplorer) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		if node.Value != "arn" {
			return fmt.Errorf("line %d: resource_explorer: want arn, or a list of Resource Explorer types", node.Line)
		}
		*r = ResourceExplorer{FromARN: true}
		return nil
	}
	*r = ResourceExplorer{}
	return node.Decode(&r.Types)
}

// ResourceExplorerTypes returns the Resource Explorer types of the entry
// (see ResourceExplorer): its own types, or service:resource_type of its
// arn; nil without them.
func (e *Entry) ResourceExplorerTypes() []string {
	switch r := e.ResourceExplorer; {
	case r == nil:
		return nil
	case r.FromARN && e.ARN != nil:
		return []string{e.ARN.Service + ":" + e.ARN.ResourceType}
	default:
		return slices.Clone(r.Types)
	}
}

// CloudFormationTypes returns, by CloudFormation type, the Terraform types
// a stack's resource of the type is listed as with its physical ID: those
// the entries' cloudformation_types give it, in their order, then those of
// the cloud_control entries of the type, by Terraform type. A listed
// resource with the physical ID of a stack's resource is the stack's only
// when its type is one of them, or holds a part of one under the same ID,
// so that a hand-made role is not taken for the instance profile of the
// same name a stack made; a CloudFormation type that isn't here, such as a
// custom resource's, manages whatever has its physical ID.
func (c *Catalogue) CloudFormationTypes() map[string][]string {
	types := map[string][]string{}
	add := func(cfn, typ string) {
		if !slices.Contains(types[cfn], typ) {
			types[cfn] = append(types[cfn], typ)
		}
	}
	for _, typ := range c.Types() {
		for _, t := range c.entries[typ].CloudFormationTypes {
			add(t.Type, typ)
			for _, also := range t.Also {
				add(t.Type, also)
			}
		}
	}
	for _, typ := range c.Types() {
		if cc := c.entries[typ].CloudControl; cc != nil {
			add(cc.Type, typ)
		}
	}
	return types
}
