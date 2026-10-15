// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

// The lister lists each cluster's parts with the IDs Terraform imports
// them with: its node groups, add-ons, Fargate profiles, pod identity
// associations but an add-on's, and access entries with their policies,
// which a cluster that authenticates through the aws-auth ConfigMap alone
// doesn't have.
func TestEksListsClusterPartsWithImportIDs(t *testing.T) {
	const admin = "arn:aws:iam::123456789012:role/platform/admin"
	const policy = "arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"
	useFakeAPI(t, func(call apiCall) string {
		switch call.Path {
		case "/clusters":
			return `{"clusters":["web","legacy"]}`
		case "/clusters/web":
			return `{"cluster":{"name":"web","accessConfig":{"authenticationMode":"API_AND_CONFIG_MAP"}}}`
		case "/clusters/legacy":
			return `{"cluster":{"name":"legacy","accessConfig":{"authenticationMode":"CONFIG_MAP"}}}`
		case "/clusters/web/node-groups":
			return `{"nodegroups":["system"]}`
		case "/clusters/web/addons":
			return `{"addons":["vpc-cni"]}`
		case "/clusters/web/fargate-profiles":
			return `{"fargateProfileNames":["batch"]}`
		case "/clusters/web/pod-identity-associations":
			return `{"associations":[
				{"associationId":"a-1","namespace":"apps","serviceAccount":"api"},
				{"associationId":"a-2","namespace":"kube-system","serviceAccount":"aws-node","ownerArn":"arn:aws:eks:us-east-1:123456789012:addon/web/vpc-cni/1"}]}`
		case "/clusters/web/access-entries":
			return `{"accessEntries":["` + admin + `"]}`
		case "/clusters/web/access-entries/" + admin + "/access-policies":
			return `{"associatedAccessPolicies":[{"policyArn":"` + policy + `","accessScope":{"type":"cluster"}}]}`
		case "/clusters/legacy/node-groups":
			return `{"nodegroups":[]}`
		case "/clusters/legacy/addons":
			return `{"addons":[]}`
		case "/clusters/legacy/fargate-profiles":
			return `{"fargateProfileNames":[]}`
		case "/clusters/legacy/pod-identity-associations":
			return `{"associations":[]}`
		}
		t.Errorf("unexpected call %s", call.Path)
		return `{"__type":"ResourceNotFoundException"}`
	})
	g := &EksLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_eks_cluster", "web", "legacy")
	assertIDs(t, g.Resources, "aws_eks_node_group", "web:system")
	assertIDs(t, g.Resources, "aws_eks_addon", "web:vpc-cni")
	assertIDs(t, g.Resources, "aws_eks_fargate_profile", "web:batch")
	assertIDs(t, g.Resources, "aws_eks_pod_identity_association", "web,a-1")
	assertIDs(t, g.Resources, "aws_eks_access_entry", "web:"+admin)
	assertIDs(t, g.Resources, "aws_eks_access_policy_association", "web#"+admin+"#"+policy)
	for _, r := range g.Resources {
		if r.Address.Type == "aws_eks_access_policy_association" && r.RawName != "admin_AmazonEKSClusterAdminPolicy" {
			t.Errorf("policy association named %q, want admin_AmazonEKSClusterAdminPolicy", r.RawName)
		}
	}
}
