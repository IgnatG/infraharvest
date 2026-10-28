// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// elastiCacheResponse wraps the result of a call of the ElastiCache query
// API.
func elastiCacheResponse(op, result string) string {
	return `<` + op + `Response xmlns="http://elasticache.amazonaws.com/doc/2015-02-02/"><` + op + `Result>` + result +
		`</` + op + `Result></` + op + `Response>`
}

// The lister leaves out the nodes of replication groups, which the groups
// manage, and what is being deleted. Default parameter and subnet groups
// are listed, for the catalogue to leave out.
func TestElastiCacheListsWhatTerraformManages(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "DescribeCacheClusters":
			return elastiCacheResponse(call.Op, `<CacheClusters>
				<CacheCluster><CacheClusterId>sessions</CacheClusterId><CacheClusterStatus>available</CacheClusterStatus></CacheCluster>
				<CacheCluster><CacheClusterId>queue-001</CacheClusterId><CacheClusterStatus>available</CacheClusterStatus><ReplicationGroupId>queue</ReplicationGroupId></CacheCluster>
				<CacheCluster><CacheClusterId>old</CacheClusterId><CacheClusterStatus>deleting</CacheClusterStatus></CacheCluster>
				</CacheClusters>`)
		case "DescribeReplicationGroups":
			return elastiCacheResponse(call.Op, `<ReplicationGroups>
				<ReplicationGroup><ReplicationGroupId>queue</ReplicationGroupId><Status>available</Status></ReplicationGroup>
				<ReplicationGroup><ReplicationGroupId>gone</ReplicationGroupId><Status>deleting</Status></ReplicationGroup>
				</ReplicationGroups>`)
		case "DescribeCacheParameterGroups":
			// The catalogue leaves out the default one.
			return elastiCacheResponse(call.Op, `<CacheParameterGroups>
				<CacheParameterGroup><CacheParameterGroupName>default.valkey8</CacheParameterGroupName></CacheParameterGroup>
				<CacheParameterGroup><CacheParameterGroupName>queue-valkey8</CacheParameterGroupName></CacheParameterGroup>
				</CacheParameterGroups>`)
		case "DescribeCacheSubnetGroups":
			// The catalogue leaves out the default one.
			return elastiCacheResponse(call.Op, `<CacheSubnetGroups>
				<CacheSubnetGroup><CacheSubnetGroupName>default</CacheSubnetGroupName></CacheSubnetGroup>
				<CacheSubnetGroup><CacheSubnetGroupName>queue</CacheSubnetGroupName></CacheSubnetGroup>
				</CacheSubnetGroups>`)
		}
		t.Errorf("unexpected call %s", call.Op)
		return `<ErrorResponse><Error><Code>Unexpected</Code></Error></ErrorResponse>`
	})
	g := &ElastiCacheLister{}

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertIDs(t, resources, "aws_elasticache_cluster", "sessions")
	assertIDs(t, resources, "aws_elasticache_replication_group", "queue")
	assertIDs(t, resources, "aws_elasticache_parameter_group", "default.valkey8", "queue-valkey8")
	assertIDs(t, resources, "aws_elasticache_subnet_group", "default", "queue")
}
