// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import "sort"

// byProvider has each provider command's adapters, in the order the engine
// tries them: by the command, not the Terraform provider, so that google
// --beta, whose provider is google-beta, gets google's.
var byProvider = map[string][]Adapter{
	"aws":    {S3Bucket, IAMRole, VPC},
	"azure":  {AzureUserAssignedIdentity},
	"google": {GoogleNetwork, GoogleStorageBucket, GoogleServiceAccount, GoogleProjectIAM, GoogleKMS},
}

// For returns the adapters for the resources of a provider command, such
// as azure (see harvest.Profile.Command), in the order the engine tries
// them.
func For(command string) []Adapter {
	return byProvider[command]
}

// Providers returns the provider commands with adapters, sorted.
func Providers() []string {
	names := make([]string, 0, len(byProvider))
	for name := range byProvider {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
