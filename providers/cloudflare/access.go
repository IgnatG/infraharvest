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

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/zero_trust"
)

type AccessGenerator struct {
	CloudflareService
}

// accessApplicationResources records an account's or a zone's Access
// applications as cloudflare_zero_trust_access_application
// (cloudflare_access_application before provider 5), imported as
// accounts/<account_id>/<app_id> or zones/<zone_id>/<app_id>. scope is
// accounts or zones, and ownerID the account's or zone's ID.
func accessApplicationResources(scope, ownerID string, apps []zero_trust.AccessApplicationListResponse) []terraformutils.Resource {
	ownerAttribute := "zone_id"
	if scope == "accounts" {
		ownerAttribute = "account_id"
	}
	resources := []terraformutils.Resource{}
	for _, app := range apps {
		resources = append(resources, terraformutils.NewResource(
			scope+"/"+ownerID+"/"+app.ID,
			fmt.Sprintf("%s_%s", app.Name, app.ID),
			"cloudflare_zero_trust_access_application",
			"cloudflare",
			map[string]string{
				ownerAttribute: ownerID,
				"name":         app.Name,
			}))
	}
	return resources
}

func listAccessApplications(ctx context.Context, client *cloudflare.Client, params zero_trust.AccessApplicationListParams) ([]zero_trust.AccessApplicationListResponse, error) {
	return collect(client.ZeroTrust.Access.Applications.ListAutoPaging(ctx, params))
}

func (g *AccessGenerator) InitResources() error {
	ctx := g.Context()
	client, err := g.initializeAPI()
	if err != nil {
		return err
	}

	if accountID := g.accountID(); accountID != "" {
		apps, err := listAccessApplications(ctx, client, zero_trust.AccessApplicationListParams{AccountID: cloudflare.F(accountID)})
		if err != nil {
			return err
		}
		g.Resources = append(g.Resources, accessApplicationResources("accounts", accountID, apps)...)
	}

	zoneList, err := listZones(ctx, client)
	if err != nil {
		return err
	}

	for _, zone := range zoneList {
		apps, err := listAccessApplications(ctx, client, zero_trust.AccessApplicationListParams{ZoneID: cloudflare.F(zone.ID)})
		if err != nil {
			return err
		}
		g.Resources = append(g.Resources, accessApplicationResources("zones", zone.ID, apps)...)
	}

	return nil
}
