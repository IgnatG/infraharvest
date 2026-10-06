// Copyright 2020 The Terraformer Authors.
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

package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v4"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type RedisGenerator struct {
	AzureService
}

func (g *RedisGenerator) listRedisServers() ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	redisClient, err := armredis.NewClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	redisServers, err := listAllLenient(ctx, redisClient.NewListBySubscriptionPager(nil),
		func(p armredis.ClientListBySubscriptionResponse) []*armredis.ResourceInfo { return p.Value })
	if err != nil {
		return nil, err
	}

	for _, redisServer := range redisServers {
		resources = append(resources, terraformutils.NewSimpleResource(
			*redisServer.ID,
			*redisServer.Name,
			"azurerm_redis_cache",
			g.ProviderName))
	}

	return resources, nil
}

func (g *RedisGenerator) InitResources() error {
	functions := []func() ([]terraformutils.Resource, error){
		g.listRedisServers,
	}

	for _, f := range functions {
		resources, err := f()
		if err != nil {
			return err
		}
		g.Resources = append(g.Resources, resources...)
	}

	return nil
}
