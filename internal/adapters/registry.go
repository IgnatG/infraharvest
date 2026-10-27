// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import "sort"

// byFamily has the adapters of each family of curated modules, in the order
// the engine tries them: by a name of the family's own, which a provider's
// profile names (see harvest.Profile.Modules), rather than the Terraform
// provider, so that google --beta, whose provider is google-beta, gets
// google's.
var byFamily = map[string][]Adapter{
	"aws":    {S3Bucket, IAMRole, VPC},
	"azure":  {AzureUserAssignedIdentity},
	"google": {GoogleNetwork, GoogleStorageBucket, GoogleServiceAccount, GoogleProjectIAM, GoogleKMS},
}

// For returns the adapters of a family of curated modules, such as azure
// (see harvest.Profile.Modules), in the order the engine tries them.
func For(family string) []Adapter {
	return byFamily[family]
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
