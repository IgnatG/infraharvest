// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import "sort"

// byProvider has each provider's adapters, in the order the engine tries
// them.
var byProvider = map[string][]Adapter{
	"aws": {S3Bucket, IAMRole},
}

// For returns the adapters for a provider's resources, in the order the
// engine tries them.
func For(provider string) []Adapter {
	return byProvider[provider]
}

// Providers returns the providers with adapters, sorted.
func Providers() []string {
	names := make([]string, 0, len(byProvider))
	for name := range byProvider {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
