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

package commercetools

import (
	"context"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/labd/commercetools-go-sdk/platform"
)

type ProductTypeGenerator struct {
	CommercetoolsService
}

// InitResources generates Terraform Resources from Commercetools API
func (g *ProductTypeGenerator) InitResources() error {
	client, err := g.client()
	if err != nil {
		return err
	}
	ctx := context.Background()
	items, err := listAll(func(where []string) ([]platform.ProductType, error) {
		page, err := client.ProductTypes().Get().Sort(sortByID).Limit(pageSize).WithTotal(false).Where(where).Execute(ctx)
		if err != nil {
			return nil, err
		}
		return page.Results, nil
	}, func(item platform.ProductType) string { return item.ID })
	if err != nil {
		return err
	}
	g.Resources = productTypeResources(items)
	return nil
}

func productTypeResources(items []platform.ProductType) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, item := range items {
		name := deref(item.Key)
		if name == "" {
			name = normalizeResourceName(item.Name)
		}
		resources = append(resources, terraformutils.NewResource(
			item.ID,
			name,
			"commercetools_product_type",
			"commercetools",
			map[string]string{}))
	}
	return resources
}
