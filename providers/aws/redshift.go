// Copyright 2018 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aws

import (
	"strings"

	"github.com/IgnatG/infraharvest/terraformutils"

	"github.com/aws/aws-sdk-go-v2/service/redshift"
)

type RedshiftGenerator struct {
	AWSService
}

func (g *RedshiftGenerator) loadClusters(svc *redshift.Client) error {
	p := redshift.NewDescribeClustersPaginator(svc, &redshift.DescribeClustersInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, db := range page.Clusters {
			resourceName := StringValue(db.ClusterIdentifier)
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				resourceName,
				resourceName,
				"aws_redshift_cluster",
				"aws"))
		}
	}
	return nil
}

func (g *RedshiftGenerator) loadParameterGroups(svc *redshift.Client) error {
	p := redshift.NewDescribeClusterParameterGroupsPaginator(svc, &redshift.DescribeClusterParameterGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, parameterGroup := range page.ParameterGroups {
			resourceName := StringValue(parameterGroup.ParameterGroupName)
			if strings.Contains(resourceName, ".") {
				continue // skip default Default ParameterGroups like default.mysql5.6
			}
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				resourceName,
				resourceName,
				"aws_redshift_parameter_group",
				"aws"))
		}
	}
	return nil
}

func (g *RedshiftGenerator) loadSubnetGroups(svc *redshift.Client) error {
	p := redshift.NewDescribeClusterSubnetGroupsPaginator(svc, &redshift.DescribeClusterSubnetGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, subnet := range page.ClusterSubnetGroups {
			resourceName := StringValue(subnet.ClusterSubnetGroupName)
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				resourceName,
				resourceName,
				"aws_redshift_subnet_group",
				"aws"))
		}
	}
	return nil
}

func (g *RedshiftGenerator) loadEventSubscription(svc *redshift.Client) error {
	p := redshift.NewDescribeEventSubscriptionsPaginator(svc, &redshift.DescribeEventSubscriptionsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, eventSubscription := range page.EventSubscriptionsList {
			resourceName := StringValue(eventSubscription.CustomerAwsId)
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				resourceName,
				resourceName,
				"aws_redshift_event_subscription",
				"aws"))
		}
	}
	return nil
}

func (g *RedshiftGenerator) loadSnapshotSchedules(svc *redshift.Client) error {
	p := redshift.NewDescribeSnapshotSchedulesPaginator(svc, &redshift.DescribeSnapshotSchedulesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, snapshotSchedule := range page.SnapshotSchedules {
			resourceName := StringValue(snapshotSchedule.ScheduleIdentifier)
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				resourceName,
				resourceName,
				"aws_redshift_snapshot_schedule",
				"aws"))

			for _, associatedCluster := range snapshotSchedule.AssociatedClusters {
				clusterName := StringValue(associatedCluster.ClusterIdentifier)
				g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
					clusterName+"/"+resourceName,
					clusterName+"_"+resourceName,
					"aws_redshift_snapshot_schedule_association",
					"aws"))
			}
		}
	}
	return nil
}

// Generate TerraformResources from AWS API,
// from each database create 1 TerraformResource.
// Need only database name as ID for terraform resource
// AWS api support paging
func (g *RedshiftGenerator) InitResources() error {
	config, e := g.generateConfig()
	if e != nil {
		return e
	}
	svc := redshift.NewFromConfig(config)

	if err := g.loadClusters(svc); err != nil {
		return err
	}
	if err := g.loadParameterGroups(svc); err != nil {
		return err
	}
	if err := g.loadSubnetGroups(svc); err != nil {
		return err
	}
	if err := g.loadEventSubscription(svc); err != nil {
		return err
	}
	if err := g.loadSnapshotSchedules(svc); err != nil {
		return err
	}

	return nil
}
