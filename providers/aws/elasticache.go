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

	"github.com/aws/aws-sdk-go-v2/service/elasticache"
)

type ElastiCacheGenerator struct {
	AWSService
}

func (g *ElastiCacheGenerator) loadCacheClusters(svc *elasticache.Client) error {
	p := elasticache.NewDescribeCacheClustersPaginator(svc, &elasticache.DescribeCacheClustersInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, cluster := range page.CacheClusters {
			resourceName := StringValue(cluster.CacheClusterId)
			resource := terraformutils.NewSimpleResource(
				resourceName,
				resourceName,
				"aws_elasticache_cluster",
				"aws")

			g.Resources = append(g.Resources, resource)
		}
	}
	return nil
}

func (g *ElastiCacheGenerator) loadParameterGroups(svc *elasticache.Client) error {
	p := elasticache.NewDescribeCacheParameterGroupsPaginator(svc, &elasticache.DescribeCacheParameterGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, parameterGroup := range page.CacheParameterGroups {
			resourceName := StringValue(parameterGroup.CacheParameterGroupName)
			if strings.Contains(resourceName, ".") {
				continue // skip default Default ParameterGroups like default.redis5.0
			}
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				resourceName,
				resourceName,
				"aws_elasticache_parameter_group",
				"aws"))
		}
	}
	return nil
}

func (g *ElastiCacheGenerator) loadSubnetGroups(svc *elasticache.Client) error {
	p := elasticache.NewDescribeCacheSubnetGroupsPaginator(svc, &elasticache.DescribeCacheSubnetGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, subnet := range page.CacheSubnetGroups {
			resourceName := StringValue(subnet.CacheSubnetGroupName)
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				resourceName,
				resourceName,
				"aws_elasticache_subnet_group",
				"aws"))
		}
	}
	return nil
}

func (g *ElastiCacheGenerator) loadReplicationGroups(svc *elasticache.Client) error {
	p := elasticache.NewDescribeReplicationGroupsPaginator(svc, &elasticache.DescribeReplicationGroupsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, replicationGroup := range page.ReplicationGroups {
			resourceName := StringValue(replicationGroup.ReplicationGroupId)
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				resourceName,
				resourceName,
				"aws_elasticache_replication_group",
				"aws"))
		}
	}
	return nil
}

// Generate TerraformResources from AWS API,
// from each database create 1 TerraformResource.
// Need only database name as ID for terraform resource
// AWS api support paging
func (g *ElastiCacheGenerator) InitResources() error {
	config, e := g.generateConfig()
	if e != nil {
		return e
	}
	svc := elasticache.NewFromConfig(config)

	if err := g.loadCacheClusters(svc); err != nil {
		return err
	}
	if err := g.loadParameterGroups(svc); err != nil {
		return err
	}
	if err := g.loadReplicationGroups(svc); err != nil {
		return err
	}
	if err := g.loadSubnetGroups(svc); err != nil {
		return err
	}

	return nil
}
