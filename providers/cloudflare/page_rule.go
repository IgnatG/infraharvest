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

package cloudflare

import (
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/page_rules"
)

type PageRulesGenerator struct {
	CloudflareService
}

// pageRuleResources records a zone's page rules as cloudflare_page_rule,
// imported as <zone_id>/<page_rule_id>.
func pageRuleResources(zoneID string, pageRules []page_rules.PageRule) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, pageRule := range pageRules {
		resources = append(resources, terraformutils.NewResource(
			zoneID+"/"+pageRule.ID,
			pageRule.ID,
			"cloudflare_page_rule",
			"cloudflare",
			map[string]string{
				"zone_id": zoneID,
			}))
	}
	return resources
}

func (g *PageRulesGenerator) InitResources() error {
	ctx := g.Context()
	client, err := g.initializeAPI()
	if err != nil {
		return err
	}

	zoneList, err := listZones(ctx, client)
	if err != nil {
		return err
	}

	for _, zone := range zoneList {
		// The page rules API isn't paginated: it returns a zone's rules at once.
		pageRules, err := client.PageRules.List(ctx, page_rules.PageRuleListParams{ZoneID: cloudflare.F(zone.ID)})
		if err != nil {
			return err
		}
		if pageRules != nil {
			g.Resources = append(g.Resources, pageRuleResources(zone.ID, *pageRules)...)
		}
	}

	return nil
}
