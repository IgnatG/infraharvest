// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// elastiCacheDeleting is the status ElastiCache gives clusters and
// replication groups while it deletes them; they are left out, since they
// are gone before Terraform could import them.
const elastiCacheDeleting = "deleting"

// ElastiCacheLister lists a region's ElastiCache clusters, parameter
// groups, replication groups and subnet groups, each by its name, which
// is the ID Terraform imports it with.
type ElastiCacheLister struct {
	Service
}

// List lists clusters, parameter groups, replication groups and
// subnet groups, in that order, and returns the first error.
func (g *ElastiCacheLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := elasticache.NewFromConfig(config)
	for _, list := range []func(*elasticache.Client) error{
		g.listCacheClusters,
		g.listParameterGroups,
		g.listReplicationGroups,
		g.listSubnetGroups,
	} {
		if err := list(svc); err != nil {
			return err
		}
	}
	return nil
}

// listCacheClusters adds the clusters that stand on their own. The nodes
// of a replication group are clusters too, but the group manages them, so
// they are left out with the clusters being deleted.
func (g *ElastiCacheLister) listCacheClusters(svc *elasticache.Client) error {
	p := elasticache.NewDescribeCacheClustersPaginator(svc, &elasticache.DescribeCacheClustersInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, cluster := range page.CacheClusters {
			if aws.ToString(cluster.ReplicationGroupId) != "" || aws.ToString(cluster.CacheClusterStatus) == elastiCacheDeleting {
				continue
			}
			id := aws.ToString(cluster.CacheClusterId)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_elasticache_cluster", "aws"))
		}
	}
	return nil
}

// listParameterGroups adds every parameter group. The default ones, which
// ElastiCache provides and can't be changed, are listed too; the
// catalogue's skip rule leaves them out.
func (g *ElastiCacheLister) listParameterGroups(svc *elasticache.Client) error {
	p := elasticache.NewDescribeCacheParameterGroupsPaginator(svc, &elasticache.DescribeCacheParameterGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, group := range page.CacheParameterGroups {
			name := aws.ToString(group.CacheParameterGroupName)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_elasticache_parameter_group", "aws"))
		}
	}
	return nil
}

// listReplicationGroups adds the replication groups, but those being
// deleted.
func (g *ElastiCacheLister) listReplicationGroups(svc *elasticache.Client) error {
	p := elasticache.NewDescribeReplicationGroupsPaginator(svc, &elasticache.DescribeReplicationGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, group := range page.ReplicationGroups {
			if aws.ToString(group.Status) == elastiCacheDeleting {
				continue
			}
			id := aws.ToString(group.ReplicationGroupId)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_elasticache_replication_group", "aws"))
		}
	}
	return nil
}

// listSubnetGroups adds every subnet group. The default one is listed too;
// the catalogue's skip rule leaves it out.
func (g *ElastiCacheLister) listSubnetGroups(svc *elasticache.Client) error {
	p := elasticache.NewDescribeCacheSubnetGroupsPaginator(svc, &elasticache.DescribeCacheSubnetGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, group := range page.CacheSubnetGroups {
			name := aws.ToString(group.CacheSubnetGroupName)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_elasticache_subnet_group", "aws"))
		}
	}
	return nil
}
