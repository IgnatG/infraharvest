// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"google.golang.org/api/dns/v1"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// dnsService lists the record sets of the project's Cloud DNS zones.
const dnsService = "dns"

// dnsRecordSetType is the Terraform type of a Cloud DNS record set: the
// records of one name and type in a managed zone.
const dnsRecordSetType = "google_dns_record_set"

// DNSRecordSetsLister lists the record sets of the project's Cloud DNS
// managed zones, public and private, the dns service. The catalogue maps
// no Cloud Asset Inventory type to record sets, so cloud_asset, which
// lists the zones themselves, doesn't find them. Zones are global, so the
// import of global lists their record sets, and a region's lists nothing.
// Listing needs dns.managedZones.list and dns.resourceRecordSets.list on
// the project, which roles/dns.reader and roles/viewer grant.
type DNSRecordSetsLister struct {
	harvest.ListerOf[listerSettings]
}

// List lists every zone's record sets, from every page. A
// project without the Cloud DNS API enabled has no zones: that is logged,
// and lists nothing. Any other failure is an error.
func (g *DNSRecordSetsLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *DNSRecordSetsLister) list() error {
	region := g.Settings().region
	if region.Name != "" {
		return nil
	}
	project, err := g.Settings().projectID()
	if err != nil {
		return err
	}
	// The record sets take a call per zone, and the zones one more.
	if !g.Wants(dnsRecordSetType) {
		return nil
	}
	ctx := g.Context()
	svc, err := dns.NewService(ctx, clientOptions(ctx)...)
	if err != nil {
		return err
	}
	zones, err := dnsZones(ctx, svc, project)
	if err != nil {
		if isServiceDisabled(err) {
			log.Printf("%s: dns: no Cloud DNS zones listed in project %s: %v", g.Provider(), project, err)
			return nil
		}
		return err
	}
	var resources []harvest.Resource
	for _, zone := range zones {
		err := svc.ResourceRecordSets.List(project, zone.Name).Pages(ctx, func(page *dns.ResourceRecordSetsListResponse) error {
			for _, set := range page.Rrsets {
				resources = append(resources, dnsRecordSet(project, zone, set, g.Provider()))
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("listing the record sets of Cloud DNS zone %s in project %s: %w", zone.Name, project, err)
		}
	}
	g.Resources = resources
	return nil
}

// dnsZones returns the project's managed zones, from every page.
func dnsZones(ctx context.Context, svc *dns.Service, project string) ([]*dns.ManagedZone, error) {
	var zones []*dns.ManagedZone
	err := svc.ManagedZones.List(project).Pages(ctx, func(page *dns.ManagedZonesListResponse) error {
		zones = append(zones, page.ManagedZones...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing the Cloud DNS zones of project %s: %w", project, err)
	}
	return zones, nil
}

// dnsRecordSet is set in zone, imported by the provider's
// projects/{project}/managedZones/{zone}/rrsets/{name}/{type} format. Its
// attributes include the zone's DNS name, and whether the set is at the
// zone's apex, so that the catalogue can tell the SOA and NS sets Cloud
// DNS creates with every zone from the user's. It is named after the zone,
// the set's name within the zone and its type (see recordSetName).
func dnsRecordSet(project string, zone *dns.ManagedZone, set *dns.ResourceRecordSet, provider string) harvest.Resource {
	id := "projects/" + project + "/managedZones/" + zone.Name + "/rrsets/" + set.Name + "/" + set.Type
	return harvest.Record(id, recordSetName(zone, set), dnsRecordSetType, provider, map[string]string{
		"project":      project,
		"managed_zone": zone.Name,
		"name":         set.Name,
		"type":         set.Type,
		"dns_name":     zone.DnsName,
		"apex":         strconv.FormatBool(strings.EqualFold(set.Name, zone.DnsName)),
	})
}

// recordSetName names set after its zone, its name without the zone's DNS
// name, which the zone's name already stands for, and its type:
// app.shop.example.com. A in the zone shop is shop-app-A, and the zone's
// apex MX set shop-MX. A name outside the zone's DNS name stays whole.
func recordSetName(zone *dns.ManagedZone, set *dns.ResourceRecordSet) string {
	parts := []string{zone.Name}
	name := strings.TrimSuffix(set.Name, ".")
	suffix := strings.TrimSuffix(zone.DnsName, ".")
	switch lower := strings.ToLower(name); {
	case strings.EqualFold(name, suffix):
	case suffix != "" && strings.HasSuffix(lower, "."+strings.ToLower(suffix)):
		parts = append(parts, name[:len(name)-len(suffix)-1])
	default:
		parts = append(parts, name)
	}
	return strings.Join(append(parts, set.Type), "-")
}
