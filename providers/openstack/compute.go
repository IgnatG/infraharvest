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

package openstack

import (
	"log"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/pagination"
)

type ComputeGenerator struct {
	OpenStackService
}

// createResources iterate on all openstack_compute_instance_v2
func (g *ComputeGenerator) createResources(list *pagination.Pager, volclient *gophercloud.ServiceClient) []terraformutils.Resource {
	resources := []terraformutils.Resource{}

	err := list.EachPage(func(page pagination.Page) (bool, error) {
		servers, err := servers.ExtractServers(page)
		if err != nil {
			return false, err
		}

		for _, s := range servers {
			if volclient != nil {
				var vol []volumes.Volume
				for _, av := range s.AttachedVolumes {
					onevol, err := volumes.Get(volclient, av.ID).Extract()
					if err == nil {
						vol = append(vol, *onevol)
					}
				}

				sort.SliceStable(vol, func(i, j int) bool {
					return vol[i].Attachments[0].Device < vol[j].Attachments[0].Device
				})

				for _, v := range vol {
					// Bootable image volumes belong to the instance itself, not to a volume attachment.
					if v.Bootable == "true" && v.VolumeImageMetadata != nil {
						continue
					}
					name := s.Name + strings.ReplaceAll(v.Attachments[0].Device, "/dev/", "")
					rid := s.ID + "/" + v.ID
					resources = append(resources, terraformutils.NewResource(
						rid,
						name,
						"openstack_compute_volume_attach_v2",
						"openstack",
						map[string]string{}))
				}
			}

			resources = append(resources, terraformutils.NewResource(
				s.ID,
				s.Name,
				"openstack_compute_instance_v2",
				"openstack",
				map[string]string{}))
		}

		return true, nil
	})
	if err != nil {
		log.Println(err)
	}
	return resources
}

// Generate TerraformResources from OpenStack API,
func (g *ComputeGenerator) InitResources() error {
	opts, err := openstack.AuthOptionsFromEnv()
	if err != nil {
		return err
	}

	provider, err := openstack.AuthenticatedClient(opts)
	if err != nil {
		return err
	}

	client, err := openstack.NewComputeV2(provider, gophercloud.EndpointOpts{
		Region: g.GetArgs()["region"].(string),
	})
	if err != nil {
		return err
	}

	list := servers.List(client, nil)
	volclient, err := openstack.NewBlockStorageV3(provider, gophercloud.EndpointOpts{
		Region: g.GetArgs()["region"].(string)})
	if err != nil {
		log.Println("VolumeImageMetadata requires blockStorage API v3")
		volclient = nil
	}
	g.Resources = g.createResources(&list, volclient)

	return nil
}
