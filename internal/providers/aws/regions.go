// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

// The regions a call of the provider can list in, besides a named region
// such as eu-west-2.
const (
	// GlobalPseudoRegion is the pseudo-region of the global services, such as
	// IAM: they are listed once per account, not once per region. The SDK
	// resolves each service's global endpoint for it.
	GlobalPseudoRegion = "aws-global"
	// GlobalHomeRegion is us-east-1, where the public partition
	// keeps what is global but tied to a region, such as WAFv2's
	// CloudFront scope, and where the provider block of global services
	// points.
	GlobalHomeRegion = "us-east-1"
	// ProfileRegion lists in the region the profile or the environment
	// resolves to, when no --regions is given.
	ProfileRegion = ""
)

var (
	// GlobalServices are the services listed once, in
	// GlobalPseudoRegion, rather than in each region.
	GlobalServices = servicesOfScope(scopeGlobal)
	// EastOnlyServices are the services listed once, in
	// GlobalHomeRegion, rather than in each region.
	EastOnlyServices = servicesOfScope(scopeEastOnly)
)
