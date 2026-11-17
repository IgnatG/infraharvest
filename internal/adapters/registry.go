// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"maps"
	"slices"
	"sort"
)

// Registry has the adapters of each family of curated modules, in the
// order the engine tries them: by a name of the family's own, which a
// provider's profile names (see harvest.Profile.Modules), rather than the
// Terraform provider, so that google --beta, whose provider is
// google-beta, gets google's.
//
// A registry is a value of its own: a run is handed the one it uses (see
// importer.Run.WithRegistry), so that two runs in a process, or a library
// that adds or replaces modules, never see each other's. Builtin returns
// one with the curated modules infraharvest ships. For and Providers
// return copies, so that changing them leaves the registry as it is.
// A registry is not safe for concurrent changes: a run reads it alone.
type Registry struct {
	byFamily map[string][]Adapter
}

// builtin has the curated adapters of each family, in the order the engine
// tries them. It is the definitions only, which nothing changes: Builtin
// copies them into each registry it returns.
var builtin = map[string][]Adapter{
	"aws":    {awsS3Bucket, awsIAMRole, awsVPC},
	"azure":  {azureUserAssignedIdentity},
	"google": {googleNetwork, googleStorageBucket, googleServiceAccount, googleProjectIAM, googleKMS},
}

// NewRegistry returns a registry with no adapters, for a caller that
// brings its own set (see Registry.Add).
func NewRegistry() *Registry {
	return &Registry{byFamily: map[string][]Adapter{}}
}

// Builtin returns a registry with the curated modules infraharvest ships,
// each call its own: changing one registry leaves the others, and the
// built-in definitions, as they are.
func Builtin() *Registry {
	r := NewRegistry()
	for family, list := range builtin {
		r.byFamily[family] = cloneAll(list)
	}
	return r
}

// Clone returns a registry with copies of r's adapters.
func (r *Registry) Clone() *Registry {
	c := NewRegistry()
	if r == nil {
		return c
	}
	for family, list := range r.byFamily {
		c.byFamily[family] = cloneAll(list)
	}
	return c
}

// For returns copies of the adapters of a family of curated modules, such
// as azure (see harvest.Profile.Modules), in the order the engine tries
// them.
func (r *Registry) For(family string) []Adapter {
	if r == nil {
		return nil
	}
	list := r.byFamily[family]
	if list == nil {
		return nil
	}
	return cloneAll(list)
}

// Providers returns the families of curated modules, sorted.
func (r *Registry) Providers() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.byFamily))
	for name := range r.byFamily {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Add appends copies of list to the adapters of a family, after those it
// has, which the engine tries first.
func (r *Registry) Add(family string, list ...Adapter) {
	r.byFamily[family] = append(r.byFamily[family], cloneAll(list)...)
}

// Replace makes copies of list the adapters of a family, instead of those
// it has; none removes the family.
func (r *Registry) Replace(family string, list ...Adapter) {
	if len(list) == 0 {
		delete(r.byFamily, family)
		return
	}
	r.byFamily[family] = cloneAll(list)
}

func cloneAll(list []Adapter) []Adapter {
	copies := make([]Adapter, len(list))
	for i, a := range list {
		copies[i] = a.clone()
	}
	return copies
}

// S3Bucket returns a copy of the adapter of terraform-aws-modules/s3-bucket/aws.
func S3Bucket() Adapter { return awsS3Bucket.clone() }

// IAMRole returns a copy of the adapter of the iam-role module of
// terraform-aws-modules/iam/aws.
func IAMRole() Adapter { return awsIAMRole.clone() }

// VPC returns a copy of the adapter of terraform-aws-modules/vpc/aws.
func VPC() Adapter { return awsVPC.clone() }

// AzureUserAssignedIdentity returns a copy of the adapter of
// Azure/avm-res-managedidentity-userassignedidentity/azurerm.
func AzureUserAssignedIdentity() Adapter { return azureUserAssignedIdentity.clone() }

// GoogleNetwork returns a copy of the adapter of
// terraform-google-modules/network/google.
func GoogleNetwork() Adapter { return googleNetwork.clone() }

// GoogleStorageBucket returns a copy of the adapter of the simple_bucket
// module of terraform-google-modules/cloud-storage/google.
func GoogleStorageBucket() Adapter { return googleStorageBucket.clone() }

// GoogleServiceAccount returns a copy of the adapter of the simple-sa
// module of terraform-google-modules/service-accounts/google.
func GoogleServiceAccount() Adapter { return googleServiceAccount.clone() }

// GoogleProjectIAM returns a copy of the adapter of the projects_iam
// module of terraform-google-modules/iam/google.
func GoogleProjectIAM() Adapter { return googleProjectIAM.clone() }

// GoogleKMS returns a copy of the adapter of terraform-google-modules/kms/google.
func GoogleKMS() Adapter { return googleKMS.clone() }

// clone returns a copy of a whose slices and maps are its own, so that
// changing them leaves a as it is. Its funcs are shared, which nothing can
// change, and so are its defaults' values, which cty makes immutable.
func (a Adapter) clone() Adapter {
	a.Members = slices.Clone(a.Members)
	a.Repeated = slices.Clone(a.Repeated)
	a.Through = slices.Clone(a.Through)
	a.Inputs = slices.Clone(a.Inputs)
	a.Outputs = slices.Clone(a.Outputs)
	a.Targets = slices.Clone(a.Targets)
	a.Providers = slices.Clone(a.Providers)
	a.Defaults = maps.Clone(a.Defaults)
	a.Inverse = maps.Clone(a.Inverse)
	return a
}
