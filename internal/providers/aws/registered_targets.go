// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// targetAttachmentType is the type the alb lister records registered
// targets as, with the ARN of their target group as target_group_arn.
const targetAttachmentType = "aws_lb_target_group_attachment"

// loadBalancerControllerTag is the tag the AWS Load Balancer Controller
// of an EKS cluster gives the target groups it creates for the cluster's
// Ingresses and Services, with the cluster's name as its value; it
// registers the pods in them.
const loadBalancerControllerTag = "tags.elbv2.k8s.aws/cluster"

// reasonRegisteredTarget is why the default selection leaves out a target
// that owner, an Auto Scaling group, an ECS service or the AWS Load
// Balancer Controller of an EKS cluster, registers in its target group:
// the owner registers and deregisters its targets as it scales and
// deploys, so Terraform would register or deregister live targets on apply
// and destroy.
func reasonRegisteredTarget(owner string) string {
	return "registered by " + owner + ", which manages its targets"
}

// excludeRegisteredTargets adds to excluded the target group attachments
// of resources whose target group an Auto Scaling group, an ECS service or
// the AWS Load Balancer Controller of an EKS cluster registers its targets
// in. The controller's target groups are told by their tag, from the
// listed target groups, without a call. Auto Scaling and ECS are asked
// only when an attachment of some other target group was listed, and ECS
// only for target groups no Auto Scaling group claims. An error of either
// is returned, joined, and what the other found is still excluded.
func (p *Provider) excludeRegisteredTargets(ctx context.Context, config aws.Config, resources []harvest.Resource, excluded map[string]string) error {
	groups := map[string]bool{}
	for _, r := range resources {
		if r.Address.Type == targetAttachmentType {
			if arn := r.Observed.Attributes["target_group_arn"]; arn != "" {
				groups[arn] = true
			}
		}
	}
	if len(groups) == 0 {
		return nil
	}
	owners := map[string]string{}
	for _, r := range resources {
		if r.Address.Type == "aws_lb_target_group" {
			if cluster := r.Observed.Attributes[loadBalancerControllerTag]; cluster != "" {
				claimTargetGroup(groups, owners, r.Observed.ID, "the AWS Load Balancer Controller of EKS cluster "+cluster)
			}
		}
	}
	var err error
	if len(owners) < len(groups) {
		err = autoScalingTargetGroups(ctx, autoscaling.NewFromConfig(config), groups, owners)
	}
	if len(owners) < len(groups) {
		err = errors.Join(err, ecsTargetGroups(ctx, ecs.NewFromConfig(config), groups, owners))
	}
	for _, r := range resources {
		if r.Address.Type != targetAttachmentType {
			continue
		}
		if owner, ok := owners[r.Observed.Attributes["target_group_arn"]]; ok {
			excluded[r.Address.Type+" "+r.Observed.ID] = reasonRegisteredTarget(owner)
		}
	}
	return err
}

// claimTargetGroup records owner as the owner of the target group arn when
// arn is one of groups and has no owner yet.
func claimTargetGroup(groups map[string]bool, owners map[string]string, arn, owner string) {
	if _, claimed := owners[arn]; groups[arn] && !claimed {
		owners[arn] = owner
	}
}

// autoScalingTargetGroups adds to owners each of groups that an Auto
// Scaling group of the region registers its instances in, through its
// target groups or its traffic sources, which DescribeAutoScalingGroups
// returns with the group, named "Auto Scaling group <name>".
func autoScalingTargetGroups(ctx context.Context, client *autoscaling.Client, groups map[string]bool, owners map[string]string) error {
	pages := autoscaling.NewDescribeAutoScalingGroupsPaginator(client, &autoscaling.DescribeAutoScalingGroupsInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("find the target groups of Auto Scaling groups: %w", err)
		}
		for _, group := range page.AutoScalingGroups {
			owner := "Auto Scaling group " + aws.ToString(group.AutoScalingGroupName)
			for _, arn := range group.TargetGroupARNs {
				claimTargetGroup(groups, owners, arn, owner)
			}
			for _, source := range group.TrafficSources {
				claimTargetGroup(groups, owners, aws.ToString(source.Identifier), owner)
			}
		}
	}
	return nil
}

// ecsTargetGroups adds to owners each of groups that an ECS service of the
// region registers its tasks in, named "ECS service <cluster>/<service>":
// the target groups of the service's load balancers, their alternate
// target groups for blue/green deployments, and those of its task sets.
// Services are described ten at a time. A cluster whose services can't be
// read is reported, and the other clusters are still asked.
func ecsTargetGroups(ctx context.Context, client *ecs.Client, groups map[string]bool, owners map[string]string) error {
	var errs []error
	clusters := ecs.NewListClustersPaginator(client, &ecs.ListClustersInput{}, stopOnDuplicateToken)
	for clusters.HasMorePages() {
		page, err := clusters.NextPage(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("find the target groups of ECS services: %w", err))
			return errors.Join(errs...)
		}
		for _, clusterARN := range page.ClusterArns {
			if err := ecsClusterTargetGroups(ctx, client, clusterARN, groups, owners); err != nil {
				errs = append(errs, fmt.Errorf("find the target groups of the ECS services of cluster %s: %w", clusterARN, err))
			}
		}
	}
	return errors.Join(errs...)
}

// ecsClusterTargetGroups adds to owners each of groups that a service of
// the ECS cluster clusterARN registers its tasks in (see ecsTargetGroups).
func ecsClusterTargetGroups(ctx context.Context, client *ecs.Client, clusterARN string, groups map[string]bool, owners map[string]string) error {
	clusterName := arnName(clusterARN)
	services := ecs.NewListServicesPaginator(client, &ecs.ListServicesInput{Cluster: aws.String(clusterARN)}, stopOnDuplicateToken)
	for services.HasMorePages() {
		page, err := services.NextPage(ctx)
		if err != nil {
			return err
		}
		for batch := range slices.Chunk(page.ServiceArns, ecsDescribeServicesLimit) {
			out, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{
				Cluster:  aws.String(clusterARN),
				Services: batch,
			})
			if err != nil {
				return err
			}
			for _, service := range out.Services {
				owner := "ECS service " + clusterName + "/" + aws.ToString(service.ServiceName)
				claimLoadBalancers(groups, owners, service.LoadBalancers, owner)
				for _, set := range service.TaskSets {
					claimLoadBalancers(groups, owners, set.LoadBalancers, owner)
				}
			}
		}
	}
	return nil
}

// claimLoadBalancers records owner as the owner of the target groups, of
// groups, that the ECS load balancers loadBalancers name, their alternate
// target groups included.
func claimLoadBalancers(groups map[string]bool, owners map[string]string, loadBalancers []ecstypes.LoadBalancer, owner string) {
	for _, lb := range loadBalancers {
		claimTargetGroup(groups, owners, aws.ToString(lb.TargetGroupArn), owner)
		if lb.AdvancedConfiguration != nil {
			claimTargetGroup(groups, owners, aws.ToString(lb.AdvancedConfiguration.AlternateTargetGroupArn), owner)
		}
	}
}
