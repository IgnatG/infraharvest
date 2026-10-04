// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

// byProvider has each provider's adapters, in the order the engine tries
// them.
var byProvider = map[string][]Adapter{
	"aws": {S3Bucket},
}

// For returns the adapters for a provider's resources, in the order the
// engine tries them.
func For(provider string) []Adapter {
	return byProvider[provider]
}
