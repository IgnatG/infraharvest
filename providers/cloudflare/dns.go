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
	"github.com/cloudflare/cloudflare-go/v7/dns"
	"github.com/cloudflare/cloudflare-go/v7/zones"
)

type DNSGenerator struct {
	CloudflareService
}

func zoneResource(zone zones.Zone) terraformutils.Resource {
	return terraformutils.NewResource(
		zone.ID,
		zone.Name,
		"cloudflare_zone",
		"cloudflare",
		map[string]string{
			"id": zone.ID,
		})
}

// recordResources records a zone's DNS records as cloudflare_dns_record
// (cloudflare_record before provider 5), imported as <zone_id>/<record_id>.
func recordResources(zoneID, zoneName string, records []dns.RecordResponse) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	for _, record := range records {
		resources = append(resources, terraformutils.NewResource(
			zoneID+"/"+record.ID,
			fmt.Sprintf("%s_%s_%s", record.Type, zoneName, record.ID),
			"cloudflare_dns_record",
			"cloudflare",
			map[string]string{
				"zone_id": zoneID,
				"domain":  zoneName,
				"name":    record.Name,
			}))
	}
	return resources
}

func listRecords(ctx context.Context, client *cloudflare.Client, zoneID string) ([]dns.RecordResponse, error) {
	return collect(client.DNS.Records.ListAutoPaging(ctx, dns.RecordListParams{ZoneID: cloudflare.F(zoneID)}))
}

func (g *DNSGenerator) InitResources() error {
	ctx := g.Context()
	client, err := g.initializeAPI()
	if err != nil {
		log.Println(err)
		return err
	}

	zoneList, err := listZones(ctx, client)
	if err != nil {
		log.Println(err)
		return err
	}

	for _, zone := range zoneList {
		g.Resources = append(g.Resources, zoneResource(zone))
		records, err := listRecords(ctx, client, zone.ID)
		if err != nil {
			log.Println(err)
			return err
		}
		g.Resources = append(g.Resources, recordResources(zone.ID, zone.Name, records)...)
	}
	return nil
}
