// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

// For returns the adapters for a provider's resources, in the order the
// engine tries them.
func For(provider string) []Adapter {
	switch provider {
	case "aws":
		return []Adapter{S3Bucket}
	}
	return nil
}
