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

// ownerTags are the tags AWS, and the controllers that run in EKS
// clusters, give what they create and then manage themselves, which an
// import would give a second owner: CloudFormation's stack tag; the tag
// Auto Scaling gives the instances it launches, which it replaces as it
// scales; Karpenter's, on the instances, launch templates and instance
// profiles it creates for its node pools; the AWS Load Balancer
// Controller's, on the load balancers, target groups, listeners, rules
// and security groups it creates for services and ingresses; the tag the
// Kubernetes cloud provider gives the load balancers it creates for
// LoadBalancer services; the tags the in-tree volume plugin and the EBS
// and EFS CSI drivers give the volumes, snapshots and access points they
// create for persistent volume claims; and kubernetes.io/cluster/<name>
// set to owned, which marks what a cluster created and deletes with it.
// The same key set to shared, as on the VPC and subnets a cluster uses,
// marks what the cluster only uses, and Karpenter's karpenter.sh/discovery
// marks subnets and security groups it only finds: neither says anything
// about who manages them.
var ownerTags = []harvest.OwnerTag{
	{Key: "aws:cloudformation:stack-name", Tool: "CloudFormation", Stack: true},
	{Key: "aws:autoscaling:groupName", Tool: "an Auto Scaling group"},
	{Key: "karpenter.sh/nodepool", Tool: "Karpenter"},
	{Key: "karpenter.sh/nodeclaim", Tool: "Karpenter"},
	{Key: "karpenter.sh/provisioner-name", Tool: "Karpenter"},
	{Key: "karpenter.k8s.aws/ec2nodeclass", Tool: "Karpenter"},
	{Key: "elbv2.k8s.aws/cluster", Tool: "the AWS Load Balancer Controller"},
	{Key: "kubernetes.io/service-name", Tool: "Kubernetes, for a LoadBalancer service"},
	{Key: "kubernetes.io/created-for/pvc/name", Tool: "Kubernetes, for a persistent volume claim"},
	{Key: "ebs.csi.aws.com/cluster", Value: "true", Tool: "the EBS CSI driver"},
	{Key: "efs.csi.aws.com/cluster", Value: "true", Tool: "the EFS CSI driver"},
	{Key: "kubernetes.io/cluster/", Value: "owned", Tool: "Kubernetes", KeyPrefix: true},
}

// traits returns what AWS's resources and the values that identify them
// are like: ARNs identify a resource on their own, ahead of its other
// attributes, and name resources whose IDs are other values; the
// attributes the provider computes that identify their resources are an
// API's execution ARN and root resource, a KMS key's key ID, a hosted
// zone's ID, a load balancer's DNS name, zone and ARN suffix, the domain
// names of a distribution and a bucket, and the unique IDs of IAM
// identities (and the names other providers' resources have, which some
// AWS ones have too); a function's function_name and a bucket's bucket
// are names; CloudFormation, Auto Scaling and the controllers of EKS
// clusters tag what they create and manage (see ownerTags); and resources
// take tags, which tags_all merges with the provider's default_tags, and
// some, such as an EKS node group, labels.
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
		OwnerTags:           ownerTags,
		TagAttributes:       []string{"tags", "labels"},
		MergedTagAttributes: []string{"tags_all", "default_tags"},
	}
}
