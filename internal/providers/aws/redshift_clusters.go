// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/redshift"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// RedshiftLister lists the service "redshift": the provisioned Redshift
// clusters, the parameter groups, the subnet groups, the event
// subscriptions, and the snapshot schedules with their cluster
// associations.
type RedshiftLister struct {
	Service
}

// List lists, in this order, clusters, parameter groups, subnet
// groups, event subscriptions and snapshot schedules. Any error fails the
// service.
func (g *RedshiftLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *RedshiftLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := redshift.NewFromConfig(config)
	for _, list := range []func(*redshift.Client) error{
		g.listClusters,
		g.listParameterGroups,
		g.listSubnetGroups,
		g.listEventSubscriptions,
		g.listSnapshotSchedules,
	} {
		if err := list(client); err != nil {
			return err
		}
	}
	return nil
}

// listClusters records every cluster by its identifier.
func (g *RedshiftLister) listClusters(client *redshift.Client) error {
	p := redshift.NewDescribeClustersPaginator(client, &redshift.DescribeClustersInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, cluster := range page.Clusters {
			id := aws.ToString(cluster.ClusterIdentifier)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_redshift_cluster", "aws"))
		}
	}
	return nil
}

// listParameterGroups records every parameter group by name, but the
// default ones AWS provides, whose names ("default.redshift-1.0") are the
// only ones that may hold a dot.
func (g *RedshiftLister) listParameterGroups(client *redshift.Client) error {
	p := redshift.NewDescribeClusterParameterGroupsPaginator(client, &redshift.DescribeClusterParameterGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, group := range page.ParameterGroups {
			name := aws.ToString(group.ParameterGroupName)
			if strings.Contains(name, ".") {
				continue
			}
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_redshift_parameter_group", "aws"))
		}
	}
	return nil
}

// listSubnetGroups records every subnet group by name, the one named
// "default" included.
func (g *RedshiftLister) listSubnetGroups(client *redshift.Client) error {
	p := redshift.NewDescribeClusterSubnetGroupsPaginator(client, &redshift.DescribeClusterSubnetGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, group := range page.ClusterSubnetGroups {
			name := aws.ToString(group.ClusterSubnetGroupName)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_redshift_subnet_group", "aws"))
		}
	}
	return nil
}

// listEventSubscriptions records every event subscription by its name
// (CustSubscriptionId), which is what Terraform imports it by.
func (g *RedshiftLister) listEventSubscriptions(client *redshift.Client) error {
	p := redshift.NewDescribeEventSubscriptionsPaginator(client, &redshift.DescribeEventSubscriptionsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, subscription := range page.EventSubscriptionsList {
			id := aws.ToString(subscription.CustSubscriptionId)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_redshift_event_subscription", "aws"))
		}
	}
	return nil
}

// listSnapshotSchedules records every snapshot schedule by its identifier,
// each followed by one association per cluster it is associated with, by
// Terraform's import ID "<cluster>/<schedule>" and named
// "<cluster>_<schedule>".
func (g *RedshiftLister) listSnapshotSchedules(client *redshift.Client) error {
	p := redshift.NewDescribeSnapshotSchedulesPaginator(client, &redshift.DescribeSnapshotSchedulesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, schedule := range page.SnapshotSchedules {
			scheduleID := aws.ToString(schedule.ScheduleIdentifier)
			g.Resources = append(g.Resources, harvest.RecordBare(scheduleID, scheduleID, "aws_redshift_snapshot_schedule", "aws"))
			for _, association := range schedule.AssociatedClusters {
				clusterID := aws.ToString(association.ClusterIdentifier)
				g.Resources = append(g.Resources, harvest.RecordBare(
					clusterID+"/"+scheduleID,
					clusterID+"_"+scheduleID,
					"aws_redshift_snapshot_schedule_association",
					"aws"))
			}
		}
	}
	return nil
}
