// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// AutoScalingLister lists the service "auto_scaling": the Auto Scaling
// groups, the launch configurations, and the EC2 launch templates groups
// are launched from.
type AutoScalingLister struct {
	Service
}

// autoScalingGroupAttributes are the arguments recorded for every Auto
// Scaling group. They exist only in Terraform, so an import would leave
// them unset; these are the provider's defaults.
func autoScalingGroupAttributes() map[string]string {
	return map[string]string{
		"force_delete":              "false",
		"metrics_granularity":       "1Minute",
		"wait_for_capacity_timeout": "10m",
	}
}

// List lists, in this order, the groups, the launch
// configurations and the launch templates. Any error fails the service.
func (g *AutoScalingLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := autoscaling.NewFromConfig(config)
	if err := g.listGroups(client); err != nil {
		return err
	}
	if err := g.listLaunchConfigurations(client); err != nil {
		return err
	}
	return g.listLaunchTemplates(ec2.NewFromConfig(config))
}

// listGroups records every Auto Scaling group by name, with its tags as
// tags.<key>, which the answer has already: those of an EKS managed node
// group tell that EKS manages it (see excludeEKSNodeGroups), and
// the target groups it registers its instances in (see
// targetGroupsAttribute).
func (g *AutoScalingLister) listGroups(client *autoscaling.Client) error {
	p := autoscaling.NewDescribeAutoScalingGroupsPaginator(client, &autoscaling.DescribeAutoScalingGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, group := range page.AutoScalingGroups {
			name := aws.ToString(group.AutoScalingGroupName)
			attributes := autoScalingGroupAttributes()
			for _, tag := range group.Tags {
				attributes["tags."+aws.ToString(tag.Key)] = aws.ToString(tag.Value)
			}
			recordTargetGroups(attributes, autoScalingGroupTargetGroups(group))
			g.Resources = append(g.Resources, harvest.Record(name, name, "aws_autoscaling_group", "aws", attributes))
		}
	}
	return nil
}

// listLaunchConfigurations records every launch configuration by name.
// One with user data is recorded with user_data_base64 set to "=", which
// marks that it has user data without copying the data itself.
func (g *AutoScalingLister) listLaunchConfigurations(client *autoscaling.Client) error {
	p := autoscaling.NewDescribeLaunchConfigurationsPaginator(client, &autoscaling.DescribeLaunchConfigurationsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, configuration := range page.LaunchConfigurations {
			name := aws.ToString(configuration.LaunchConfigurationName)
			attributes := map[string]string{}
			if aws.ToString(configuration.UserData) != "" {
				attributes["user_data_base64"] = "="
			}
			g.Resources = append(g.Resources, harvest.Record(name, name, "aws_launch_configuration", "aws", attributes))
		}
	}
	return nil
}

// listLaunchTemplates records every EC2 launch template by ID, named after
// its name, with its tags as tags.<key>, which the answer has already.
func (g *AutoScalingLister) listLaunchTemplates(client *ec2.Client) error {
	p := ec2.NewDescribeLaunchTemplatesPaginator(client, &ec2.DescribeLaunchTemplatesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, template := range page.LaunchTemplates {
			attributes := map[string]string{}
			for _, tag := range template.Tags {
				attributes["tags."+aws.ToString(tag.Key)] = aws.ToString(tag.Value)
			}
			g.Resources = append(g.Resources, harvest.Record(
				aws.ToString(template.LaunchTemplateId),
				aws.ToString(template.LaunchTemplateName),
				"aws_launch_template",
				"aws",
				attributes))
		}
	}
	return nil
}
