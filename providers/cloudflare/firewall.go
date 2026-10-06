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

package cloudflare

import (
	"context"
	"fmt"
	"log"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/filters"
	"github.com/cloudflare/cloudflare-go/v7/firewall"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/rate_limits"
	"github.com/cloudflare/cloudflare-go/v7/zones"
)

type FirewallGenerator struct {
	CloudflareService
}

// zoneLockdownResources records a zone's lockdowns as
// cloudflare_zone_lockdown, imported as <zone_id>/<lockdown_id>.
func zoneLockdownResources(zoneID, zoneName string, lockdowns []firewall.Lockdown) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	for _, lockdown := range lockdowns {
		resources = append(resources, terraformutils.NewResource(
			zoneID+"/"+lockdown.ID,
			fmt.Sprintf("%s_%s", zoneName, lockdown.ID),
			"cloudflare_zone_lockdown",
			"cloudflare",
			map[string]string{
				"zone_id": zoneID,
				"zone":    zoneName,
			}))
	}
	return resources
}

// accountAccessRuleResources records an account's IP access rules as
// cloudflare_access_rule, imported as accounts/<account_id>/<rule_id>.
func accountAccessRuleResources(accountID string, rules []firewall.AccessRuleListResponse) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	for _, rule := range rules {
		resources = append(resources, terraformutils.NewSimpleResource(
			"accounts/"+accountID+"/"+rule.ID,
			rule.ID,
			"cloudflare_access_rule",
			"cloudflare"))
	}
	return resources
}

// zoneAccessRuleResources records a zone's IP access rules as
// cloudflare_access_rule, imported as zones/<zone_id>/<rule_id>. A zone's
// list includes its account's rules (scope organization), which
// accountAccessRuleResources records, so they are left out here. It also
// includes the rules of the user who owns the zone (scope user), which the
// provider can only import by account or zone, so they are left out too,
// and logged.
func zoneAccessRuleResources(zoneID, zoneName string, rules []firewall.AccessRuleListResponse) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	userRules := 0
	for _, rule := range rules {
		switch rule.Scope.Type {
		case firewall.AccessRuleListResponseScopeTypeOrganization:
			continue
		case firewall.AccessRuleListResponseScopeTypeUser:
			userRules++
			continue
		}
		resources = append(resources, terraformutils.NewResource(
			"zones/"+zoneID+"/"+rule.ID,
			fmt.Sprintf("%s_%s", zoneName, rule.ID),
			"cloudflare_access_rule",
			"cloudflare",
			map[string]string{
				"zone_id": zoneID,
			}))
	}
	if userRules > 0 {
		log.Printf("cloudflare: zone %s: skipping %d user-level IP access rules: the Cloudflare provider imports access rules by account or zone only", zoneName, userRules)
	}
	return resources
}

// filterResources records a zone's filters as cloudflare_filter, imported
// as <zone_id>/<filter_id>.
func filterResources(zoneID, zoneName string, list []filters.FirewallFilter) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	for _, filter := range list {
		resources = append(resources, terraformutils.NewResource(
			zoneID+"/"+filter.ID,
			fmt.Sprintf("%s_%s", zoneName, filter.ID),
			"cloudflare_filter",
			"cloudflare",
			map[string]string{
				"zone_id": zoneID,
			}))
	}
	return resources
}

// firewallRuleResources records a zone's firewall rules as
// cloudflare_firewall_rule, imported as <zone_id>/<rule_id>.
func firewallRuleResources(zoneID, zoneName string, rules []firewall.FirewallRule) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	for _, rule := range rules {
		resources = append(resources, terraformutils.NewResource(
			zoneID+"/"+rule.ID,
			fmt.Sprintf("%s_%s", zoneName, rule.ID),
			"cloudflare_firewall_rule",
			"cloudflare",
			map[string]string{
				"zone_id": zoneID,
			}))
	}
	return resources
}

// rateLimitResources records a zone's rate limits as cloudflare_rate_limit,
// imported as <zone_id>/<rate_limit_id>.
func rateLimitResources(zoneID string, ids []string) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, id := range ids {
		resources = append(resources, terraformutils.NewSimpleResource(
			zoneID+"/"+id,
			fmt.Sprintf("%s_%s", zoneID, id),
			"cloudflare_rate_limit",
			"cloudflare"))
	}
	return resources
}

// rateLimitPage is a page of the rate limits API, which the SDK lists
// without a response type.
type rateLimitPage struct {
	Result []struct {
		ID string `json:"id"`
	} `json:"result"`
	ResultInfo struct {
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

// listRateLimitIDs pages through a zone's rate limits.
func listRateLimitIDs(ctx context.Context, client *cloudflare.Client, zoneID string) ([]string, error) {
	var ids []string
	for page := 1; ; page++ {
		var res rateLimitPage
		params := rate_limits.RateLimitListParams{ZoneID: cloudflare.F(zoneID), Page: cloudflare.F(float64(page))}
		if err := client.RateLimits.List(ctx, params, option.WithResponseBodyInto(&res)); err != nil { //nolint:staticcheck // lists what the deprecated API still holds
			return ids, err
		}
		for _, r := range res.Result {
			ids = append(ids, r.ID)
		}
		if len(res.Result) == 0 || page >= res.ResultInfo.TotalPages {
			return ids, nil
		}
	}
}

// zoneLister lists one kind of a zone's firewall resources.
type zoneLister func(ctx context.Context, client *cloudflare.Client, zone zones.Zone) ([]terraformutils.Resource, error)

func listFirewallRules(ctx context.Context, client *cloudflare.Client, zone zones.Zone) ([]terraformutils.Resource, error) {
	rules, err := collect(client.Firewall.Rules.ListAutoPaging(ctx, firewall.RuleListParams{ZoneID: cloudflare.F(zone.ID)})) //nolint:staticcheck // lists what the deprecated API still holds
	if err != nil {
		if retired(err, "Firewall Rules", zone.Name) {
			return nil, nil
		}
		return nil, err
	}
	return firewallRuleResources(zone.ID, zone.Name, rules), nil
}

func listFilters(ctx context.Context, client *cloudflare.Client, zone zones.Zone) ([]terraformutils.Resource, error) {
	list, err := collect(client.Filters.ListAutoPaging(ctx, filters.FilterListParams{ZoneID: cloudflare.F(zone.ID)})) //nolint:staticcheck // lists what the deprecated API still holds
	if err != nil {
		if retired(err, "Filters", zone.Name) {
			return nil, nil
		}
		return nil, err
	}
	return filterResources(zone.ID, zone.Name, list), nil
}

func listZoneAccessRules(ctx context.Context, client *cloudflare.Client, zone zones.Zone) ([]terraformutils.Resource, error) {
	rules, err := collect(client.Firewall.AccessRules.ListAutoPaging(ctx, firewall.AccessRuleListParams{ZoneID: cloudflare.F(zone.ID)}))
	if err != nil {
		return nil, err
	}
	return zoneAccessRuleResources(zone.ID, zone.Name, rules), nil
}

func listZoneLockdowns(ctx context.Context, client *cloudflare.Client, zone zones.Zone) ([]terraformutils.Resource, error) {
	lockdowns, err := collect(client.Firewall.Lockdowns.ListAutoPaging(ctx, firewall.LockdownListParams{ZoneID: cloudflare.F(zone.ID)}))
	if err != nil {
		return nil, err
	}
	return zoneLockdownResources(zone.ID, zone.Name, lockdowns), nil
}

func listRateLimits(ctx context.Context, client *cloudflare.Client, zone zones.Zone) ([]terraformutils.Resource, error) {
	ids, err := listRateLimitIDs(ctx, client, zone.ID)
	if err != nil {
		if retired(err, "Rate Limiting", zone.Name) {
			return nil, nil
		}
		return nil, err
	}
	return rateLimitResources(zone.ID, ids), nil
}

func (g *FirewallGenerator) InitResources() error {
	ctx := g.Context()
	client, err := g.initializeAPI()
	if err != nil {
		return err
	}

	if accountID := g.accountID(); accountID != "" {
		rules, err := collect(client.Firewall.AccessRules.ListAutoPaging(ctx, firewall.AccessRuleListParams{AccountID: cloudflare.F(accountID)}))
		if err != nil {
			return err
		}
		g.Resources = append(g.Resources, accountAccessRuleResources(accountID, rules)...)
	}

	zoneList, err := listZones(ctx, client)
	if err != nil {
		return err
	}

	listers := []zoneLister{
		listFirewallRules,
		listFilters,
		listZoneAccessRules,
		listZoneLockdowns,
		listRateLimits,
	}

	for _, zone := range zoneList {
		for _, list := range listers {
			resources, err := list(ctx, client, zone)
			if err != nil {
				return err
			}
			g.Resources = append(g.Resources, resources...)
		}
	}

	return nil
}
