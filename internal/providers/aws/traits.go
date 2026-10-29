// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"regexp"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// awsManagedPolicy matches the ARNs of AWS-managed policies, such as
// arn:aws:iam::aws:policy/ReadOnlyAccess, in every partition: their names
// say what they grant, so they read better where they are used than as a
// local.
var awsManagedPolicy = regexp.MustCompile(`^arn:aws[a-z-]*:iam::aws:policy/`)

// traits returns what AWS's resources and the values that identify them
// are like: ARNs identify a resource on their own, ahead of its other
// attributes, and name resources whose IDs are other values; the
// attributes the provider computes that identify their resources are an
// API's execution ARN and root resource, a KMS key's key ID, a hosted
// zone's ID, a load balancer's DNS name, zone and ARN suffix, the domain
// names of a distribution and a bucket, and the unique IDs of IAM
// identities (and the names other providers' resources have, which some
// AWS ones have too); a function's function_name and a bucket's bucket
// are names; CloudFormation tags what its stacks create with the stack's
// name; and resources take tags, which tags_all merges with the
// provider's default_tags, and some, such as an EKS node group, labels.
func traits() harvest.Traits {
	return harvest.Traits{
		IDPrefixes:        []string{"arn:"},
		ARNPrefix:         "arn:",
		WellKnownIDs:      awsManagedPolicy,
		PrimaryIdentities: []string{"arn"},
		NameAttributes:    []string{"function_name", "bucket"},
		IdentityAttributes: []string{
			"execution_arn", "root_resource_id", "key_id", "zone_id", "hosted_zone_id", "dns_name", "domain_name",
			"bucket_regional_domain_name", "bucket_domain_name", "fqdn", "arn_suffix", "principal_id", "client_id", "unique_id",
		},
		OwnerTags:           []harvest.OwnerTag{{Key: "aws:cloudformation:stack-name", Tool: "CloudFormation", Stack: true}},
		TagAttributes:       []string{"tags", "labels"},
		MergedTagAttributes: []string{"tags_all", "default_tags"},
	}
}
