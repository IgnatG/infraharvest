// Copyright 2022 The Terraformer Authors.
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

package tencentcloud

import (
	"github.com/IgnatG/infraharvest/terraformutils"
	clb "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/clb/v20180317"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
)

type ClbGenerator struct {
	TencentCloudService
}

func (g *ClbGenerator) InitResources() error {
	args := g.GetArgs()
	region := args["region"].(string)
	credential := args["credential"].(common.Credential)
	profile := NewTencentCloudClientProfile()
	client, err := clb.NewClient(&credential, region, profile)
	if err != nil {
		return err
	}

	request := clb.NewDescribeLoadBalancersRequest()
	filters := make([]string, 0)
	for _, filter := range g.Filter {
		if filter.FieldPath == "id" && filter.IsApplicable("tencentcloud_clb_instance") {
			filters = append(filters, filter.AcceptableValues...)
		}
	}
	for i := range filters {
		request.LoadBalancerIds = append(request.LoadBalancerIds, &filters[i])
	}

	var offset int64
	var pageSize int64 = 50
	allInstances := make([]*clb.LoadBalancer, 0)

	for {
		request.Offset = &offset
		request.Limit = &pageSize
		response, err := client.DescribeLoadBalancers(request)
		if err != nil {
			return err
		}

		allInstances = append(allInstances, response.Response.LoadBalancerSet...)
		if len(response.Response.LoadBalancerSet) < int(pageSize) {
			break
		}
		offset += pageSize
	}

	for _, instance := range allInstances {
		resource := terraformutils.NewResource(
			*instance.LoadBalancerId,
			*instance.LoadBalancerName+"_"+*instance.LoadBalancerId,
			"tencentcloud_clb_instance",
			"tencentcloud",
			map[string]string{})

		g.Resources = append(g.Resources, resource)

		if err := g.loadListener(client, *instance.LoadBalancerId); err != nil {
			return err
		}
	}

	return nil
}

func (g *ClbGenerator) loadListener(client *clb.Client, loadBalancerID string) error {
	request := clb.NewDescribeTargetsRequest()
	request.LoadBalancerId = &loadBalancerID
	response, err := client.DescribeTargets(request)
	if err != nil {
		return err
	}

	for _, listener := range response.Response.Listeners {
		resource := terraformutils.NewResource(
			loadBalancerID+"#"+*listener.ListenerId,
			*listener.ListenerId,
			"tencentcloud_clb_listener",
			"tencentcloud",
			map[string]string{
				"scheduler": "WRR",
			})

		g.Resources = append(g.Resources, resource)
		if len(listener.Targets) > 0 {
			attachmentResource := terraformutils.NewResource(
				"#"+*listener.ListenerId+"#"+loadBalancerID,
				*listener.ListenerId,
				"tencentcloud_clb_attachment",
				"tencentcloud",
				map[string]string{})

			g.Resources = append(g.Resources, attachmentResource)
		}

		for _, rule := range listener.Rules {
			ruleResource := terraformutils.NewResource(
				loadBalancerID+"#"+*listener.ListenerId+"#"+*rule.LocationId,
				*rule.LocationId,
				"tencentcloud_clb_listener_rule",
				"tencentcloud",
				map[string]string{})

			g.Resources = append(g.Resources, ruleResource)

			if len(rule.Targets) > 0 {
				attachmentResource := terraformutils.NewResource(
					*rule.LocationId+"#"+*listener.ListenerId+"#"+loadBalancerID,
					*rule.LocationId,
					"tencentcloud_clb_attachment",
					"tencentcloud",
					map[string]string{})

				g.Resources = append(g.Resources, attachmentResource)
			}
		}

	}

	return nil
}
