// Copyright 2019 The Terraformer Authors.
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
	"fmt"
	"strconv"
	"strings"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

type EcsGenerator struct {
	AWSService
}

func (g *EcsGenerator) InitResources() error {
	config, e := g.generateConfig()
	if e != nil {
		return e
	}
	svc := ecs.NewFromConfig(config)

	p := ecs.NewListClustersPaginator(svc, &ecs.ListClustersInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, e := p.NextPage(g.Context())
		if e != nil {
			return e
		}
		for _, clusterArn := range page.ClusterArns {
			arnParts := strings.Split(clusterArn, "/")
			clusterName := arnParts[len(arnParts)-1]

			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				clusterArn,
				clusterName,
				"aws_ecs_cluster",
				"aws"))

			servicePage := ecs.NewListServicesPaginator(svc, &ecs.ListServicesInput{
				Cluster: &clusterArn,
			}, stopOnDuplicateToken)
			for servicePage.HasMorePages() {
				serviceNextPage, err := servicePage.NextPage(g.Context())
				if err != nil {
					fmt.Println(err.Error())
					continue
				}
				for _, serviceArn := range serviceNextPage.ServiceArns {
					arnParts := strings.Split(serviceArn, "/")
					serviceName := arnParts[len(arnParts)-1]

					serResp, err := svc.DescribeServices(g.Context(), &ecs.DescribeServicesInput{
						Services: []string{
							serviceName,
						},
						Cluster: &clusterArn,
					})
					if err != nil {
						fmt.Println(err.Error())
						continue
					}
					serviceDetails := serResp.Services[0]

					g.Resources = append(g.Resources, terraformutils.NewResource(
						serviceArn,
						clusterName+"_"+serviceName,
						"aws_ecs_service",
						"aws",
						map[string]string{
							"task_definition": StringValue(serviceDetails.TaskDefinition),
							"cluster":         clusterName,
							"name":            serviceName,
							"id":              serviceArn,
						}))
				}
			}
		}
	}

	taskDefinitionsMap := map[string]terraformutils.Resource{}
	latestRevisions := map[string]int{}
	taskDefinitionsPage := ecs.NewListTaskDefinitionsPaginator(svc, &ecs.ListTaskDefinitionsInput{}, stopOnDuplicateToken)
	for taskDefinitionsPage.HasMorePages() {
		taskDefinitionsNextPage, e := taskDefinitionsPage.NextPage(g.Context())
		if e != nil {
			fmt.Println(e.Error())
			continue
		}
		for _, taskDefinitionArn := range taskDefinitionsNextPage.TaskDefinitionArns {
			arnParts := strings.Split(taskDefinitionArn, ":")
			definitionWithFamily := arnParts[len(arnParts)-2]
			revision, _ := strconv.Atoi(arnParts[len(arnParts)-1])

			// fetch only latest revision of task definitions
			if latest, ok := latestRevisions[definitionWithFamily]; !ok || latest < revision {
				latestRevisions[definitionWithFamily] = revision
				taskDefinitionsMap[definitionWithFamily] = terraformutils.NewResource(
					taskDefinitionArn,
					definitionWithFamily,
					"aws_ecs_task_definition",
					"aws",
					map[string]string{
						"task_definition":       taskDefinitionArn,
						"container_definitions": "{}",
						"family":                "test-task",
						"arn":                   taskDefinitionArn,
					})

			}
		}
	}
	for _, v := range taskDefinitionsMap {
		g.Resources = append(g.Resources, v)
	}

	return nil
}
