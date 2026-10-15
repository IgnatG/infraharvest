// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"strings"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Ec2Lister lists the EC2 instances of a region as aws_instance, each by
// its instance ID and named "<instance ID>_<Name tag>".
//
// Instances that are terminated or shutting down are left out. Each
// instance records its user data, which Terraform's aws_instance holds as
// user_data_base64, and source_dest_check "true", the value Terraform
// assumes when nothing says otherwise.
//
// Tag filters given with Type=instance or Type=ec2_instance, or with no
// type, are passed to DescribeInstances, so only matching instances are
// listed.
type Ec2Lister struct {
	Service
}

// ec2FilterServiceNames are the Services that filters on EC2 instances
// are written with: the Terraform type without aws_, which --id uses
// too, and the service's name.
var ec2FilterServiceNames = []string{"instance", "ec2_instance"}

// List lists the live instances, page by page. A DescribeInstances
// error is returned and ends the listing.
func (g *Ec2Lister) List() error {
	cfg, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ec2.NewFromConfig(cfg)
	ctx := g.Context()

	input := &ec2.DescribeInstancesInput{
		Filters: ec2TagFilters(g.Filters(), ec2FilterServiceNames...),
	}
	pages := ec2.NewDescribeInstancesPaginator(client, input, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, reservation := range page.Reservations {
			for i := range reservation.Instances {
				instance := &reservation.Instances[i]
				if instanceGone(instance) {
					continue
				}
				id := aws.ToString(instance.InstanceId)
				g.Resources = append(g.Resources, harvest.Record(
					id,
					id+"_"+instanceNameTag(instance.Tags),
					"aws_instance",
					"aws",
					map[string]string{
						"user_data_base64":  instanceUserData(ctx, client, id),
						"source_dest_check": "true",
					}))
			}
		}
	}
	return nil
}

// instanceGone reports whether an instance is terminated or shutting down,
// and so has nothing left to import. An instance without a state is kept.
func instanceGone(instance *types.Instance) bool {
	if instance.State == nil {
		return false
	}
	switch instance.State.Name {
	case types.InstanceStateNameTerminated, types.InstanceStateNameShuttingDown:
		return true
	}
	return false
}

// instanceNameTag returns the value of an instance's Name tag, or "" when
// it has none. The key is matched in any case, and when several tags match,
// the last one wins.
func instanceNameTag(tags []types.Tag) string {
	name := ""
	for _, tag := range tags {
		if strings.EqualFold(aws.ToString(tag.Key), "name") {
			name = aws.ToString(tag.Value)
		}
	}
	return name
}

// instanceUserData returns an instance's user data, base64 encoded as the
// API returns it. DescribeInstances does not include user data, so it takes
// one more call per instance. When that call fails, or the instance has no
// user data, it returns "": the user data is a convenience, not a reason to
// fail the listing.
func instanceUserData(ctx context.Context, client *ec2.Client, instanceID string) string {
	out, err := client.DescribeInstanceAttribute(ctx, &ec2.DescribeInstanceAttributeInput{
		InstanceId: aws.String(instanceID),
		Attribute:  types.InstanceAttributeNameUserData,
	})
	if err != nil || out.UserData == nil {
		return ""
	}
	return aws.ToString(out.UserData.Value)
}
