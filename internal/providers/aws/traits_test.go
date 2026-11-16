// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// AWS's traits: ARNs identify resources, the policies AWS manages in any
// partition stay where they are used, CloudFormation's tag names the
// stack, the tags Auto Scaling and the controllers of EKS clusters give
// what they create say they manage it, and the tags default_tags applies
// are among the tag attributes.
func TestTraits(t *testing.T) {
	t.Parallel()
	profile := (&Provider{}).Profile()
	traits := profile.Traits
	if role := "arn:aws:iam::123456789012:role/app"; traits.ARNPrefix == "" || !strings.HasPrefix(role, traits.ARNPrefix) || !slices.Contains(traits.IDPrefixes, traits.ARNPrefix) {
		t.Errorf("ARNPrefix %q, IDPrefixes %v: want ARNs", traits.ARNPrefix, traits.IDPrefixes)
	}
	for arn, want := range map[string]bool{
		"arn:aws:iam::aws:policy/ReadOnlyAccess":                            true,
		"arn:aws-us-gov:iam::aws:policy/ReadOnlyAccess":                     true,
		"arn:aws-cn:iam::aws:policy/service-role/AWSLambdaRole":             true,
		"arn:aws:iam::123456789012:policy/ReadOnlyAccess":                   false,
		"arn:aws:iam::123456789012:role/aws-service-role/ecs.amazonaws.com": false,
	} {
		if got := traits.WellKnownIDs.MatchString(arn); got != want {
			t.Errorf("WellKnownIDs.MatchString(%q): got %v, want %v", arn, got, want)
		}
	}
	var owners engine.OwnerTags
	for _, o := range traits.OwnerTags {
		owners = append(owners, engine.OwnerTag(o))
	}
	for _, tc := range []struct {
		tags map[string]string
		want engine.Owner
	}{
		{map[string]string{"aws:cloudformation:stack-name": "app"}, engine.Owner{Tool: "CloudFormation", Stack: "app", Tag: "aws:cloudformation:stack-name=app"}},
		{map[string]string{"aws:autoscaling:groupName": "web", "Name": "web"}, engine.Owner{Tool: "an Auto Scaling group", Tag: "aws:autoscaling:groupName=web"}},
		{map[string]string{"karpenter.sh/nodepool": "default"}, engine.Owner{Tool: "Karpenter", Tag: "karpenter.sh/nodepool=default"}},
		{map[string]string{"karpenter.k8s.aws/ec2nodeclass": "default"}, engine.Owner{Tool: "Karpenter", Tag: "karpenter.k8s.aws/ec2nodeclass=default"}},
		{map[string]string{"elbv2.k8s.aws/cluster": "prod"}, engine.Owner{Tool: "the AWS Load Balancer Controller", Tag: "elbv2.k8s.aws/cluster=prod"}},
		{map[string]string{"kubernetes.io/service-name": "shop/web"}, engine.Owner{Tool: "Kubernetes, for a LoadBalancer service", Tag: "kubernetes.io/service-name=shop/web"}},
		{map[string]string{"kubernetes.io/created-for/pvc/name": "data-0"}, engine.Owner{Tool: "Kubernetes, for a persistent volume claim", Tag: "kubernetes.io/created-for/pvc/name=data-0"}},
		{map[string]string{"ebs.csi.aws.com/cluster": "true"}, engine.Owner{Tool: "the EBS CSI driver", Tag: "ebs.csi.aws.com/cluster=true"}},
		{map[string]string{"efs.csi.aws.com/cluster": "true"}, engine.Owner{Tool: "the EFS CSI driver", Tag: "efs.csi.aws.com/cluster=true"}},
		{map[string]string{"kubernetes.io/cluster/prod": "owned"}, engine.Owner{Tool: "Kubernetes", Tag: "kubernetes.io/cluster/prod=owned"}},
		// What a cluster only uses or finds, which people create.
		{map[string]string{"kubernetes.io/cluster/prod": "shared"}, engine.Owner{}},
		{map[string]string{"kubernetes.io/cluster/": "owned"}, engine.Owner{}},
		{map[string]string{"karpenter.sh/discovery": "prod"}, engine.Owner{}},
		{map[string]string{"kubernetes.io/role/elb": "1"}, engine.Owner{}},
		{map[string]string{"ebs.csi.aws.com/cluster": "false"}, engine.Owner{}},
	} {
		got, owned := owners.OwnedBy(tc.tags)
		if got != tc.want || owned != (tc.want != engine.Owner{}) {
			t.Errorf("OwnedBy(%v): got %+v, %v; want %+v", tc.tags, got, owned, tc.want)
		}
	}
	if !slices.Contains(traits.TagAttributes, profile.DefaultTags.Attribute) || !slices.Contains(traits.MergedTagAttributes, profile.DefaultTags.Block) {
		t.Errorf("TagAttributes %v, MergedTagAttributes %v: want %s and %s", traits.TagAttributes, traits.MergedTagAttributes, profile.DefaultTags.Attribute, profile.DefaultTags.Block)
	}
}
