// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/docdb"
	"github.com/aws/aws-sdk-go-v2/service/docdb/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// docdbEngine is the engine of DocumentDB clusters. DocumentDB shares its
// API with RDS and Neptune, which answer with their clusters and groups
// too.
const docdbEngine = "docdb"

// DocDBLister lists the region's DocumentDB clusters, their instances,
// the subnet groups they use and the DocumentDB cluster parameter groups.
type DocDBLister struct {
	Service
}

// List lists the clusters, the subnet groups and the cluster
// parameter groups, in that order. Any error fails the service.
func (g *DocDBLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := docdb.NewFromConfig(config)
	subnetGroups, err := g.listClusters(g.Context(), client)
	if err != nil {
		return err
	}
	g.addSubnetGroups(subnetGroups)
	return g.listClusterParameterGroups(g.Context(), client)
}

// listClusters adds each DocumentDB cluster as an aws_docdb_cluster,
// followed by its member instances as aws_docdb_cluster_instance, all by
// identifier. Only the DocumentDB clusters are asked for, by an engine
// filter, and the clusters of other engines an answer still has are left
// out. It returns the subnet groups the clusters use, in the order first
// used.
func (g *DocDBLister) listClusters(ctx context.Context, client *docdb.Client) ([]string, error) {
	var subnetGroups []string
	p := docdb.NewDescribeDBClustersPaginator(client, &docdb.DescribeDBClustersInput{
		Filters: []types.Filter{{Name: aws.String("engine"), Values: []string{docdbEngine}}},
	}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, cluster := range page.DBClusters {
			if aws.ToString(cluster.Engine) != docdbEngine {
				continue
			}
			id := aws.ToString(cluster.DBClusterIdentifier)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_docdb_cluster", "aws"))
			for _, member := range cluster.DBClusterMembers {
				instance := aws.ToString(member.DBInstanceIdentifier)
				g.Resources = append(g.Resources, harvest.RecordBare(instance, instance, "aws_docdb_cluster_instance", "aws"))
			}
			if group := aws.ToString(cluster.DBSubnetGroup); group != "" && !slices.Contains(subnetGroups, group) {
				subnetGroups = append(subnetGroups, group)
			}
		}
	}
	return subnetGroups, nil
}

// addSubnetGroups adds each of the subnet groups DocumentDB clusters use as
// an aws_docdb_subnet_group, by name. A subnet group has no engine, so the
// ones no DocumentDB cluster uses can't be told from those of RDS and
// Neptune, which the rds service lists. The one named "default", which
// AWS makes for the default VPC, is left out.
func (g *DocDBLister) addSubnetGroups(names []string) {
	for _, name := range names {
		if name == "default" {
			continue
		}
		g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_docdb_subnet_group", "aws"))
	}
}

// listClusterParameterGroups adds each DocumentDB cluster parameter group,
// one of a docdb family, as an aws_docdb_cluster_parameter_group, by name.
// The default ones, which AWS provides and which can't be changed, are
// named "default.<family>" and are left out.
func (g *DocDBLister) listClusterParameterGroups(ctx context.Context, client *docdb.Client) error {
	p := docdb.NewDescribeDBClusterParameterGroupsPaginator(client, &docdb.DescribeDBClusterParameterGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, group := range page.DBClusterParameterGroups {
			name := aws.ToString(group.DBClusterParameterGroupName)
			if !strings.HasPrefix(aws.ToString(group.DBParameterGroupFamily), docdbEngine) || strings.HasPrefix(name, "default.") {
				continue
			}
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_docdb_cluster_parameter_group", "aws"))
		}
	}
	return nil
}
