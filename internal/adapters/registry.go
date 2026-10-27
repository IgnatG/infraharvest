// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"maps"
	"slices"
	"sort"
)

// byFamily has the adapters of each family of curated modules, in the order
// the engine tries them: by a name of the family's own, which a provider's
// profile names (see harvest.Profile.Modules), rather than the Terraform
// provider, so that google --beta, whose provider is google-beta, gets
// google's. Nothing outside the package changes them: For and the
// functions named after each adapter return copies.
var byFamily = map[string][]Adapter{
	"aws":    {awsS3Bucket, awsIAMRole, awsVPC},
	"azure":  {azureUserAssignedIdentity},
	"google": {googleNetwork, googleStorageBucket, googleServiceAccount, googleProjectIAM, googleKMS},
}

// For returns copies of the adapters of a family of curated modules, such
// as azure (see harvest.Profile.Modules), in the order the engine tries
// them.
func For(family string) []Adapter {
	list := byFamily[family]
	if list == nil {
		return nil
	}
	copies := make([]Adapter, len(list))
	for i, a := range list {
		copies[i] = a.clone()
	}
	return copies
}

// Providers returns the families of curated modules, sorted.
func Providers() []string {
	names := make([]string, 0, len(byFamily))
	for name := range byFamily {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
