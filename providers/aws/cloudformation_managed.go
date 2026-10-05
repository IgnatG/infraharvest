// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// reasonCloudFormation is why the default selection leaves out a resource
// a CloudFormation stack (or the CDK, through one) manages: importing it
// would give it two owners.
func reasonCloudFormation(stack string) string {
	return "managed by CloudFormation stack " + stack
}

// liveStackStatuses are the statuses of every stack that still manages
// resources: all but DELETE_COMPLETE.
var liveStackStatuses = func() []cftypes.StackStatus {
	var live []cftypes.StackStatus
	for _, s := range cftypes.StackStatus("").Values() {
		if s != cftypes.StackStatusDeleteComplete {
			live = append(live, s)
		}
	}
	return live
}()

// cloudFormationManaged returns the physical IDs of the resources the
// CloudFormation stacks of the region config names manage, with the
// stack's name. For the global services, such as IAM, it asks every
// region the account has enabled: a stack in any of them can create a
// role.
func (p *AWSProvider) cloudFormationManaged(ctx context.Context, config aws.Config) (map[string]string, error) {
	regions := []string{config.Region}
	if p.region == GlobalRegion {
		regional := config.Copy()
		regional.Region = "us-east-1"
		out, err := ec2.NewFromConfig(regional).DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
		if err != nil {
			return nil, fmt.Errorf("enabled regions: %w", err)
		}
		regions = regions[:0]
		for _, r := range out.Regions {
			regions = append(regions, aws.ToString(r.RegionName))
		}
	}
	managed := map[string]string{}
	for _, region := range regions {
		regional := config.Copy()
		regional.Region = region
		if err := stackResources(ctx, cloudformation.NewFromConfig(regional), managed); err != nil {
			return managed, fmt.Errorf("CloudFormation stacks in %s: %w", region, err)
		}
	}
	return managed, nil
}

// stackResources adds the physical IDs of the resources of every live
// stack to managed, nested stacks included.
func stackResources(ctx context.Context, svc *cloudformation.Client, managed map[string]string) error {
	stacks := cloudformation.NewListStacksPaginator(svc, &cloudformation.ListStacksInput{StackStatusFilter: liveStackStatuses})
	for stacks.HasMorePages() {
		page, err := stacks.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, s := range page.StackSummaries {
			name := aws.ToString(s.StackName)
			resources := cloudformation.NewListStackResourcesPaginator(svc, &cloudformation.ListStackResourcesInput{StackName: s.StackId})
			for resources.HasMorePages() {
				page, err := resources.NextPage(ctx)
				if err != nil {
					return fmt.Errorf("stack %s: %w", name, err)
				}
				for _, r := range page.StackResourceSummaries {
					if id := aws.ToString(r.PhysicalResourceId); id != "" {
						managed[id] = name
					}
				}
			}
		}
	}
	return nil
}
