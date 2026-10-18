// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// CloudFormationLister lists the CloudFormation stacks, stack sets and
// stack set instances of a region (service cloudformation).
type CloudFormationLister struct {
	Service
}

// List lists the stacks, then each stack set followed by its
// instances. Every error ends the listing.
func (g *CloudFormationLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := cloudformation.NewFromConfig(config)
	if err := g.listStacks(client); err != nil {
		return err
	}
	return g.listStackSets(client)
}

// listStacks records every stack but the deleted ones, nested stacks
// included, by stack ID, with its name: the catalogue leaves out by name
// the stacks AWS services deploy and manage (Elastic Beanstalk's, a stack
// set's, Service Catalog's). The request asks only for the live stacks, so
// that 90 days of deleted ones aren't paged through; a deleted stack in the
// answer is still left out.
func (g *CloudFormationLister) listStacks(client *cloudformation.Client) error {
	pages := cloudformation.NewListStacksPaginator(client, &cloudformation.ListStacksInput{StackStatusFilter: liveStackStatuses}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, stack := range page.StackSummaries {
			if stack.StackStatus == cftypes.StackStatusDeleteComplete {
				continue
			}
			name := aws.ToString(stack.StackName)
			g.Resources = append(g.Resources, harvest.Record(
				aws.ToString(stack.StackId), name, "aws_cloudformation_stack", "aws", map[string]string{"name": name}))
		}
	}
	return nil
}

// listStackSets records every stack set but the deleted ones by its name,
// which is the ID Terraform gives a stack set and imports it by, each
// followed by its instances, recorded as <set name>,<account>,<region>.
// Both lists are read across pages.
func (g *CloudFormationLister) listStackSets(client *cloudformation.Client) error {
	sets := cloudformation.NewListStackSetsPaginator(client, &cloudformation.ListStackSetsInput{}, stopOnDuplicateToken)
	for sets.HasMorePages() {
		page, err := sets.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, set := range page.Summaries {
			if set.Status == cftypes.StackSetStatusDeleted {
				continue
			}
			name := aws.ToString(set.StackSetName)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_cloudformation_stack_set", "aws"))
			if err := g.listStackInstances(client, name); err != nil {
				return err
			}
		}
	}
	return nil
}

// listStackInstances records the instances of the stack set setName as
// <set name>,<account>,<region>, Terraform's import ID for an instance
// deployed to an account.
func (g *CloudFormationLister) listStackInstances(client *cloudformation.Client, setName string) error {
	input := &cloudformation.ListStackInstancesInput{StackSetName: aws.String(setName)}
	instances := cloudformation.NewListStackInstancesPaginator(client, input, stopOnDuplicateToken)
	for instances.HasMorePages() {
		page, err := instances.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, instance := range page.Summaries {
			id := strings.Join([]string{setName, aws.ToString(instance.Account), aws.ToString(instance.Region)}, ",")
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_cloudformation_stack_set_instance", "aws"))
		}
	}
	return nil
}
