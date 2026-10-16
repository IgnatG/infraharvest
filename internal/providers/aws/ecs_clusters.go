// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"log"
	"slices"
	"strconv"
	"strings"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// EcsLister lists the service "ecs": the ECS clusters, each with its
// services, and the latest active revision of each task definition family.
type EcsLister struct {
	Service
}

// List lists the clusters with their services, then the task
// definitions. Failing to list clusters or task definitions fails the
// service; failing to list or describe a cluster's services is logged and
// leaves out only those services.
func (g *EcsLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ecs.NewFromConfig(config)
	if err := g.listClusters(client); err != nil {
		return err
	}
	return g.listTaskDefinitions(client)
}

// listClusters records every cluster by ARN, named after its name (the
// ARN's last path segment), each followed by its services.
func (g *EcsLister) listClusters(client *ecs.Client) error {
	p := ecs.NewListClustersPaginator(client, &ecs.ListClustersInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, clusterARN := range page.ClusterArns {
			clusterName := arnName(clusterARN)
			g.Resources = append(g.Resources, harvest.RecordBare(clusterARN, clusterName, "aws_ecs_cluster", "aws"))
			g.listServices(client, clusterARN, clusterName)
		}
	}
	return nil
}

// ecsDescribeServicesLimit is the most services one DescribeServices call
// describes.
const ecsDescribeServicesLimit = 10

// listServices records the services of the cluster clusterARN, describing
// them ten at a time. An error is logged and ends the cluster's services:
// the cluster stays.
func (g *EcsLister) listServices(client *ecs.Client, clusterARN, clusterName string) {
	p := ecs.NewListServicesPaginator(client, &ecs.ListServicesInput{Cluster: aws.String(clusterARN)}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			log.Printf("aws: ecs: services of cluster %s: %v", clusterARN, err)
			return
		}
		for batch := range slices.Chunk(page.ServiceArns, ecsDescribeServicesLimit) {
			g.addServices(client, clusterARN, clusterName, batch)
		}
	}
}

// addServices describes the services serviceARNs of the cluster clusterARN
// for their task definitions, and records each by ARN, named
// "<cluster>_<service>", in the order given. The cluster and service names
// are recorded too, for Terraform's import ID "<cluster>/<service>". When
// the description fails, it is logged and the services are left out; a
// service the answer doesn't describe, one deleted since it was listed, is
// logged and left out.
func (g *EcsLister) addServices(client *ecs.Client, clusterARN, clusterName string, serviceARNs []string) {
	out, err := client.DescribeServices(g.Context(), &ecs.DescribeServicesInput{
		Cluster:  aws.String(clusterARN),
		Services: serviceARNs,
	})
	if err != nil {
		log.Printf("aws: ecs: services %s: %v", strings.Join(serviceARNs, ", "), err)
		return
	}
	described := map[string]types.Service{}
	for _, service := range out.Services {
		described[aws.ToString(service.ServiceName)] = service
	}
	for _, serviceARN := range serviceARNs {
		serviceName := arnName(serviceARN)
		service, ok := described[serviceName]
		if !ok {
			log.Printf("aws: ecs: service %s: not found", serviceARN)
			continue
		}
		g.Resources = append(g.Resources, harvest.Record(serviceARN, clusterName+"_"+serviceName, "aws_ecs_service", "aws", map[string]string{
			"task_definition": aws.ToString(service.TaskDefinition),
			"cluster":         clusterName,
			"name":            serviceName,
			"id":              serviceARN,
		}))
	}
}

// latestTaskDefinition is the highest revision of a task definition family
// seen so far, and its ARN.
type latestTaskDefinition struct {
	arn      string
	revision int
}

// listTaskDefinitions records the latest active revision of each task
// definition family by its ARN, named after the family, which it also
// records as an attribute, in the order the families are first listed.
// Older revisions are left out: they are history, and Terraform manages a
// family as one resource. An error is returned, as the latest revisions
// are not known until every page is read.
func (g *EcsLister) listTaskDefinitions(client *ecs.Client) error {
	var families []string
	latest := map[string]latestTaskDefinition{}
	input := &ecs.ListTaskDefinitionsInput{Status: types.TaskDefinitionStatusActive}
	p := ecs.NewListTaskDefinitionsPaginator(client, input, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, arn := range page.TaskDefinitionArns {
			family, revision, ok := splitTaskDefinitionARN(arn)
			if !ok {
				continue
			}
			current, seen := latest[family]
			if !seen {
				families = append(families, family)
			}
			if !seen || revision > current.revision {
				latest[family] = latestTaskDefinition{arn: arn, revision: revision}
			}
		}
	}
	for _, family := range families {
		arn, name := latest[family].arn, strings.TrimPrefix(family, "task-definition/")
		g.Resources = append(g.Resources, harvest.Record(arn, name, "aws_ecs_task_definition", "aws", map[string]string{
			"task_definition":       arn,
			"container_definitions": "{}",
			"family":                name,
			"arn":                   arn,
		}))
	}
	return nil
}

// splitTaskDefinitionARN splits a task definition ARN,
// "arn:...:task-definition/<family>:<revision>", into its family part
// ("task-definition/<family>") and its revision. ok is false when the ARN
// does not end in a numeric revision.
func splitTaskDefinitionARN(arn string) (family string, revision int, ok bool) {
	i := strings.LastIndex(arn, ":")
	if i < 0 {
		return "", 0, false
	}
	revision, err := strconv.Atoi(arn[i+1:])
	if err != nil {
		return "", 0, false
	}
	head := arn[:i]
	return head[strings.LastIndex(head, ":")+1:], revision, true
}
