// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// EksLister lists the EKS clusters of a region, each with the parts
// Terraform manages as resources of their own: node groups, add-ons,
// Fargate profiles, pod identity associations, and access entries with
// the access policies associated with them.
//
// The IDs are the import IDs Terraform documents for each type, which
// join the cluster name and the part's name with ":", "," or "#".
type EksLister struct {
	Service
}

// List lists every cluster, each after its parts. Any error stops
// the listing and is returned: a cluster without some of its parts would
// make a configuration that plans to delete them. The clusters of a page
// are read resourceParallel at a time, as each takes several calls, and
// recorded in the page's order.
func (g *EksLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *EksLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := eks.NewFromConfig(config)
	clusters := eks.NewListClustersPaginator(svc, &eks.ListClustersInput{}, stopOnDuplicateToken)
	for clusters.HasMorePages() {
		page, err := clusters.NextPage(g.Context())
		if err != nil {
			return err
		}
		resources := make([][]harvest.Resource, len(page.Clusters))
		errs := make([]error, len(page.Clusters))
		inParallel(len(page.Clusters), resourceParallel, func(i int) {
			resources[i], errs[i] = g.cluster(page.Clusters[i], svc)
		})
		for i := range page.Clusters {
			if errs[i] != nil {
				return errs[i]
			}
			g.Resources = append(g.Resources, resources[i]...)
		}
	}
	return nil
}

// cluster returns the parts of the cluster clusterName, then the cluster.
func (g *EksLister) cluster(clusterName string, svc *eks.Client) ([]harvest.Resource, error) {
	var resources []harvest.Resource
	// Each part takes a call or more per cluster, so those of types the
	// import doesn't keep are skipped.
	parts := []struct {
		list  func(string, *eks.Client, *[]harvest.Resource) error
		types []string
	}{
		{g.getNodeGroups, []string{"aws_eks_node_group"}},
		{g.getAddons, []string{"aws_eks_addon"}},
		{g.getFargateProfiles, []string{"aws_eks_fargate_profile"}},
		{g.getPodIdentityAssociations, []string{"aws_eks_pod_identity_association"}},
		{g.getAccessEntries, []string{"aws_eks_access_entry", "aws_eks_access_policy_association"}},
	}
	for _, part := range parts {
		if !g.WantsAny(part.types...) {
			continue
		}
		if err := part.list(clusterName, svc, &resources); err != nil {
			return nil, err
		}
	}
	return append(resources, harvest.RecordBare(clusterName, clusterName, "aws_eks_cluster", "aws")), nil
}

// getNodeGroups adds to out the cluster's managed node groups, imported as
// "<cluster>:<node group>".
func (g *EksLister) getNodeGroups(clusterName string, svc *eks.Client, out *[]harvest.Resource) error {
	p := eks.NewListNodegroupsPaginator(svc, &eks.ListNodegroupsInput{ClusterName: aws.String(clusterName)}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, nodeGroup := range page.Nodegroups {
			*out = append(*out, harvest.RecordBare(clusterName+":"+nodeGroup, nodeGroup, "aws_eks_node_group", "aws"))
		}
	}
	return nil
}

// getAddons adds to out the cluster's add-ons, imported as "<cluster>:<add-on>".
func (g *EksLister) getAddons(clusterName string, svc *eks.Client, out *[]harvest.Resource) error {
	p := eks.NewListAddonsPaginator(svc, &eks.ListAddonsInput{ClusterName: aws.String(clusterName)}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, addon := range page.Addons {
			*out = append(*out, harvest.RecordBare(clusterName+":"+addon, addon, "aws_eks_addon", "aws"))
		}
	}
	return nil
}

// getFargateProfiles adds to out the cluster's Fargate profiles, imported as
// "<cluster>:<profile>".
func (g *EksLister) getFargateProfiles(clusterName string, svc *eks.Client, out *[]harvest.Resource) error {
	p := eks.NewListFargateProfilesPaginator(svc, &eks.ListFargateProfilesInput{ClusterName: aws.String(clusterName)}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, profile := range page.FargateProfileNames {
			*out = append(*out, harvest.RecordBare(clusterName+":"+profile, profile, "aws_eks_fargate_profile", "aws"))
		}
	}
	return nil
}

// getPodIdentityAssociations adds to out the cluster's pod identity associations,
// imported as "<cluster>,<association ID>" and named after the service
// account they give a role to. An association with an owner belongs to
// that owner, such as an add-on that created it, and is left out.
func (g *EksLister) getPodIdentityAssociations(clusterName string, svc *eks.Client, out *[]harvest.Resource) error {
	p := eks.NewListPodIdentityAssociationsPaginator(svc, &eks.ListPodIdentityAssociationsInput{ClusterName: aws.String(clusterName)}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, association := range page.Associations {
			if aws.ToString(association.OwnerArn) != "" {
				continue
			}
			id := clusterName + "," + aws.ToString(association.AssociationId)
			name := aws.ToString(association.Namespace) + "_" + aws.ToString(association.ServiceAccount)
			*out = append(*out, harvest.RecordBare(id, name, "aws_eks_pod_identity_association", "aws"))
		}
	}
	return nil
}

// getAccessEntries adds to out the cluster's access entries, imported as
// "<cluster>:<principal ARN>", each followed by the access policies
// associated with it. A cluster that authenticates through the aws-auth
// ConfigMap alone has no access entries and rejects the call to list them,
// so its authentication mode is looked up first.
func (g *EksLister) getAccessEntries(clusterName string, svc *eks.Client, out *[]harvest.Resource) error {
	described, err := svc.DescribeCluster(g.Context(), &eks.DescribeClusterInput{Name: aws.String(clusterName)})
	if err != nil {
		return err
	}
	cluster := described.Cluster
	if cluster == nil || cluster.AccessConfig == nil || cluster.AccessConfig.AuthenticationMode == ekstypes.AuthenticationModeConfigMap {
		return nil
	}
	p := eks.NewListAccessEntriesPaginator(svc, &eks.ListAccessEntriesInput{ClusterName: aws.String(clusterName)}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, principal := range page.AccessEntries {
			*out = append(*out, harvest.RecordBare(clusterName+":"+principal, arnName(principal), "aws_eks_access_entry", "aws"))
			// A call per entry, for associations the import may not keep.
			if !g.Wants("aws_eks_access_policy_association") {
				continue
			}
			if err := g.getAccessPolicyAssociations(clusterName, principal, svc, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// getAccessPolicyAssociations adds to out the access policies associated with
// one access entry, imported as "<cluster>#<principal ARN>#<policy ARN>"
// and named after the principal and the policy.
func (g *EksLister) getAccessPolicyAssociations(clusterName, principal string, svc *eks.Client, out *[]harvest.Resource) error {
	p := eks.NewListAssociatedAccessPoliciesPaginator(svc, &eks.ListAssociatedAccessPoliciesInput{
		ClusterName:  aws.String(clusterName),
		PrincipalArn: aws.String(principal),
	}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, policy := range page.AssociatedAccessPolicies {
			policyARN := aws.ToString(policy.PolicyArn)
			id := clusterName + "#" + principal + "#" + policyARN
			name := arnName(principal) + "_" + arnName(policyARN)
			*out = append(*out, harvest.RecordBare(id, name, "aws_eks_access_policy_association", "aws"))
		}
	}
	return nil
}

// arnName returns what follows the last "/" of an ARN, such as a role's
// name without its path, or the whole ARN when it has no "/".
func arnName(arn string) string {
	return arn[strings.LastIndex(arn, "/")+1:]
}
