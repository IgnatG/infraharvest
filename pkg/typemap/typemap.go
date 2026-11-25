// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package typemap maps between the names a cloud gives a kind of resource
// and the Terraform resource types that manage it: a Cloud Asset
// Inventory type such as compute.googleapis.com/Network, a CloudFormation
// type such as AWS::EC2::Instance, or an ARN, to aws_instance or
// google_compute_network, and a Terraform type to the types that are other
// names of it and the separate resources that hold parts of its
// configuration. It is a facade over infraharvest's built-in catalogue,
// which is built into the package: nothing is read from disk or the
// network, and what the catalogue knows about generating configuration
// (the arguments to leave out, the defaults) is not part of it.
//
// # Promises
//
// The names and the lookups below are stable. How much of a cloud the
// catalogue covers grows with each release: a lookup that finds no type
// today may find one later, so a caller treats "none" as "unknown", not as
// "unmanaged". Every slice a Resolver returns is the caller's own.
// Until infraharvest's first tagged release, everything in pkg/ is
// unstable.
//
// # Licence
//
// Like the rest of infraharvest it is AGPL-3.0-only. A product that is not
// licensed under the AGPL needs infraharvest's commercial licence to
// import it (see the licensing section of the README).
package typemap

import (
	"fmt"
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/tftypes"
)

// Resolver looks up Terraform resource types in infraharvest's built-in
// catalogue. It holds no state that changes, so a Resolver is safe for
// concurrent use and one is enough for a process.
type Resolver struct {
	// catalogues are the built-in catalogues, by the Terraform
	// provider's local name.
	catalogues map[string]*catalogue.Catalogue
	// cloudFormation maps a CloudFormation type in lower case to the AWS
	// types it is managed as, built once.
	cloudFormation map[string][]string
}

// New returns a Resolver of the built-in catalogue, which covers the
// providers aws, azapi, azurerm and google. It fails only if the
// catalogue built into the binary doesn't load, which infraharvest's own
// tests rule out.
func New() (*Resolver, error) {
	names, err := catalogue.Providers()
	if err != nil {
		return nil, fmt.Errorf("the built-in catalogue: %w", err)
	}
	r := &Resolver{catalogues: map[string]*catalogue.Catalogue{}, cloudFormation: map[string][]string{}}
	for _, name := range names {
		c, err := catalogue.Builtin(name)
		if err != nil {
			return nil, fmt.Errorf("the built-in catalogue of %s: %w", name, err)
		}
		r.catalogues[name] = c
	}
	if aws, ok := r.catalogues["aws"]; ok {
		for cfn, types := range aws.CloudFormationTypes() {
			r.cloudFormation[strings.ToLower(cfn)] = types
		}
	}
	return r, nil
}

// Providers returns the Terraform providers the catalogue covers, by
// their local names, sorted: aws, azapi, azurerm and google.
func (r *Resolver) Providers() []string {
	names := make([]string, 0, len(r.catalogues))
	for name := range r.catalogues {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// providersOf returns the providers a cloud, or a provider's name, stands
// for: "azure" is both azapi and azurerm. It returns none for a name it
// doesn't know. The case doesn't matter.
func (r *Resolver) providersOf(cloud string) []string {
	switch name := strings.ToLower(strings.TrimSpace(cloud)); name {
	case "azure":
		return []string{"azapi", "azurerm"}
	case "gcp":
		return []string{"google"}
	default:
		if _, ok := r.catalogues[name]; ok {
			return []string{name}
		}
	}
	return nil
}

// FromInventoryType returns the Terraform types a cloud's inventory API
// lists a resource of type assetType as, sorted: for Google, a Cloud Asset
// Inventory type such as compute.googleapis.com/Network; for AWS, a
// CloudFormation or Cloud Control type such as AWS::S3::Bucket. cloud is
// aws, azure, google or the name of a Terraform provider (azapi, azurerm),
// and neither it nor assetType is case sensitive. More than one type means
// that the inventory type covers several, which only the resource's name
// tells apart (see FromInventoryName). It returns none for a type the
// catalogue doesn't know, and for any Azure type, which the catalogue
// doesn't map yet.
func (r *Resolver) FromInventoryType(cloud, assetType string) []string {
	var types []string
	for _, provider := range r.providersOf(cloud) {
		types = append(types, r.catalogues[provider].InventoryTypes(assetType)...)
		if provider == "aws" {
			types = append(types, r.cloudFormation[strings.ToLower(assetType)]...)
		}
	}
	return sortedUnique(types)
}

// FromInventoryName is FromInventoryType for a resource whose inventory
// name is name, such as //compute.googleapis.com/projects/p/global/addresses/a:
// of the types of the inventory type, those whose name pattern matches it.
// An inventory type that covers one Terraform type returns it for any
// name.
func (r *Resolver) FromInventoryName(cloud, assetType, name string) []string {
	var types []string
	for _, provider := range r.providersOf(cloud) {
		types = append(types, r.catalogues[provider].FromInventory(assetType, name)...)
		if provider == "aws" {
			types = append(types, r.cloudFormation[strings.ToLower(assetType)]...)
		}
	}
	return sortedUnique(types)
}

// FromARN returns the AWS Terraform types whose resources the ARN names,
// sorted, from the ARN's service and the resource type its resource part
// starts with: arn:aws:iam::123456789012:role/admin is an aws_iam_role.
// The catalogue knows how the ARNs of about eighty types are made; for an
// ARN of any other type, or a string that is not an ARN, it returns none.
func (r *Resolver) FromARN(arn string) []string {
	aws, ok := r.catalogues["aws"]
	if !ok {
		return nil
	}
	// arn:partition:service:region:account:resource
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[2] == "" || parts[5] == "" {
		return nil
	}
	service, resourceType := parts[2], arnResourceType(parts[5])
	var types []string
	for typ, a := range aws.ARNs() {
		if a.Service == service && a.ResourceType == resourceType {
			types = append(types, typ)
		}
	}
	return sortedUnique(types)
}

// arnResourceType returns the resource type an ARN's resource part starts
// with: role/admin, function:name and /restapis/id give role, function and
// restapis; a part with no separator, such as an S3 bucket's name, gives
// "".
func arnResourceType(resource string) string {
	trimmed := strings.TrimPrefix(resource, "/")
	i := strings.IndexAny(trimmed, "/:")
	if i < 0 {
		return ""
	}
	return trimmed[:i]
}

// Canonical returns the type that typ and its other names are matched as:
// a load balancer managed as aws_alb is the aws_lb the cloud lists, and
// azurerm_linux_virtual_machine and azurerm_windows_virtual_machine are
// azurerm_virtual_machine. It returns typ itself for a type that has no
// other names.
func (r *Resolver) Canonical(tfType string) string {
	return tftypes.Canonical(tfType)
}

// Children returns the Terraform types of the separate resources that hold
// parts of a resource's configuration, such as the versioning and
// encryption of an S3 bucket, in the catalogue's order; none for a type
// that has none, or that the catalogue doesn't know.
func (r *Resolver) Children(tfType string) []string {
	provider, _, _ := strings.Cut(tfType, "_")
	c, ok := r.catalogues[provider]
	if !ok {
		return nil
	}
	e, ok := c.View(tfType)
	if !ok {
		return nil
	}
	return slices.Clone(e.Children)
}

// sortedUnique returns the sorted types without repeats, nil for none.
func sortedUnique(types []string) []string {
	if len(types) == 0 {
		return nil
	}
	slices.Sort(types)
	return slices.Compact(types)
}
