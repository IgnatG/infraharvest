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

package gcp

import (
	"fmt"
	"log"

	"github.com/IgnatG/infraharvest/terraformutils"

	container "google.golang.org/api/container/v1beta1"
)

type GkeGenerator struct {
	GCPService
}

func (g *GkeGenerator) initClusters(clusters *container.ListClustersResponse) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	for _, cluster := range clusters.Clusters {
		if _, exist := cluster.ResourceLabels["goog-composer-environment"]; exist { // don't manage composer clusters
			continue
		}
		resource := terraformutils.NewResource(
			cluster.Name,
			cluster.Name,
			"google_container_cluster",
			g.ProviderName,
			map[string]string{
				"name":     cluster.Name,
				"project":  g.GetArgs()["project"].(string),
				"location": cluster.Location,
				"zone":     cluster.Zone,
			})

		resources = append(resources, resource)
		resources = append(resources, g.initNodePools(cluster.NodePools, cluster.Name, cluster.Location)...)
	}
	return resources
}

func (g *GkeGenerator) initNodePools(nodePools []*container.NodePool, clusterName, location string) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	for _, nodePool := range nodePools {
		resources = append(resources, terraformutils.NewResource(
			fmt.Sprintf("%s/%s/%s", location, clusterName, nodePool.Name),
			clusterName+"_"+nodePool.Name,
			"google_container_node_pool",
			g.ProviderName,
			map[string]string{
				"location": location,
				"zone":     location,
				"project":  g.GetArgs()["project"].(string),
				"cluster":  clusterName,
				"name":     nodePool.Name,
			}))
	}
	return resources
}

// Generate TerraformResources from GCP API,
func (g *GkeGenerator) InitResources() error {
	ctx := g.Context()
	service, err := container.NewService(ctx, clientOptions()...)
	if err != nil {
		log.Print(err)
		return err
	}
	// GKE support zone and regional cluster, api use location, it's can be region or zone, for all "-"
	location := fmt.Sprintf("projects/%s/locations/%s", g.GetArgs()["project"].(string), "-")
	clusters, err := service.Projects.Locations.Clusters.List(location).Do()
	if err != nil {
		log.Print(err)
		return err
	}

	g.Resources = g.initClusters(clusters)
	return nil
}
