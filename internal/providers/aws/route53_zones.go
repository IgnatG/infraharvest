// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// Route53Lister lists the global service "route53": the hosted zones,
// public and private, each with all its record sets, and the health
// checks. A zone or health check another service created, such as Cloud
// Map for a namespace, records that service as linked_service, and so do
// the records of such a zone (see recordLinkedService).
type Route53Lister struct {
	Service
}

// List lists the zones with their records, then the health checks. A
// zone whose records fail to list doesn't stop the other zones or the
// health checks: once the zones are listed, the errors of their records
// and of the health checks are returned together as a partial error (see
// harvest.Partial), so what was listed is imported and the listing reads
// as incomplete rather than as a zone with no records. A zone list that
// fails fails the service.
func (g *Route53Lister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *Route53Lister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := route53.NewFromConfig(config)
	var parts harvest.Parts
	err = g.listHostedZones(client, &parts)
	parts.Add(nil, "", g.listHealthChecks(client))
	if err != nil {
		return parts.Fail(err)
	}
	return parts.Err()
}

// listHostedZones records every hosted zone by its ID without the
// "/hostedzone/" prefix, named after its domain, each followed by its
// records. The zone's domain is recorded with its trailing dot. It adds
// to parts the errors of the zones whose records failed to list, and
// returns the error of the zone list.
func (g *Route53Lister) listHostedZones(client *route53.Client, parts *harvest.Parts) error {
	p := route53.NewListHostedZonesPaginator(client, &route53.ListHostedZonesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return fmt.Errorf("route53: hosted zones: %w", err)
		}
		for _, zone := range page.HostedZones {
			zoneID := strings.TrimPrefix(aws.ToString(zone.Id), "/hostedzone/")
			domain := aws.ToString(zone.Name)
			attributes := map[string]string{
				"name":          domain,
				"force_destroy": "false",
			}
			recordLinkedService(attributes, zone.LinkedService)
			g.Resources = append(g.Resources, harvest.Record(zoneID, strings.TrimSuffix(domain, "."), "aws_route53_zone", "aws", attributes))
			// A call or more per zone, for records the import may not
			// keep.
			if g.Wants("aws_route53_record") {
				parts.Add(nil, "", g.listRecords(client, zoneID, domain, zone.LinkedService))
			}
		}
	}
	return nil
}

// recordPosition is where a page of record sets starts: Route 53 pages
// record sets by name, type and set identifier together.
type recordPosition struct {
	name, recordType, identifier string
}

// listRecords records every record set of the zone zoneID, whose domain
// is domain and which linked, when not nil, says another service created,
// NS and SOA included. The pages are walked by hand, from the name, type
// and set identifier that each truncated page gives for the next, and stop
// should a page point at the position it was asked for.
func (g *Route53Lister) listRecords(client *route53.Client, zoneID, domain string, linked *types.LinkedService) error {
	input := &route53.ListResourceRecordSetsInput{HostedZoneId: aws.String(zoneID)}
	for {
		page, err := client.ListResourceRecordSets(g.Context(), input)
		if err != nil {
			return fmt.Errorf("route53: records of zone %s: %w", zoneID, err)
		}
		for _, recordSet := range page.ResourceRecordSets {
			g.addRecord(zoneID, domain, linked, recordSet)
		}
		if !page.IsTruncated {
			return nil
		}
		asked := recordPosition{aws.ToString(input.StartRecordName), string(input.StartRecordType), aws.ToString(input.StartRecordIdentifier)}
		next := recordPosition{aws.ToString(page.NextRecordName), string(page.NextRecordType), aws.ToString(page.NextRecordIdentifier)}
		if next == asked {
			return fmt.Errorf("route53: records of zone %s: pagination did not advance past %q %s %q", zoneID, next.name, next.recordType, next.identifier)
		}
		input.StartRecordName = page.NextRecordName
		input.StartRecordType = page.NextRecordType
		input.StartRecordIdentifier = page.NextRecordIdentifier
	}
}

// addRecord records the record set recordSet of the zone zoneID by
// Terraform's import ID, "<zone>_<name>_<type>_<set identifier>", which
// ends in "_" for a record set without a set identifier. The ID keeps the
// name's trailing dot; the name attribute does not. Route 53 returns the
// wildcard label "*" escaped as "\052", which is turned back into "*".
// The record set is named after its name within the zone's domain, its
// type and its set identifier (see recordName). It records whether the set
// is at the zone's apex (apex), where Route 53 creates the zone's NS and
// SOA sets with the zone and won't delete them, and the service that
// created the zone, linked, if another did.
func (g *Route53Lister) addRecord(zoneID, domain string, linked *types.LinkedService, recordSet types.ResourceRecordSet) {
	name := strings.Replace(aws.ToString(recordSet.Name), `\052`, "*", 1)
	recordType := string(recordSet.Type)
	setIdentifier := aws.ToString(recordSet.SetIdentifier)
	id := zoneID + "_" + name + "_" + recordType + "_" + setIdentifier
	attributes := map[string]string{
		"name":           strings.TrimSuffix(name, "."),
		"zone_id":        zoneID,
		"type":           recordType,
		"set_identifier": setIdentifier,
		"apex":           strconv.FormatBool(name == domain),
	}
	recordLinkedService(attributes, linked)
	g.Resources = append(g.Resources, harvest.Record(id, recordName(name, domain, recordType, setIdentifier), "aws_route53_record", "aws", attributes))
}

// listHealthChecks records every health check by ID, named after what it
// checks, its domain name or IP address, and its type: "example.com
// HTTPS", with the service that created it, if another did.
func (g *Route53Lister) listHealthChecks(client *route53.Client) error {
	p := route53.NewListHealthChecksPaginator(client, &route53.ListHealthChecksInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return fmt.Errorf("route53: health checks: %w", err)
		}
		for _, check := range page.HealthChecks {
			id := aws.ToString(check.Id)
			name := ""
			if config := check.HealthCheckConfig; config != nil {
				name = strings.TrimSpace(cmp.Or(aws.ToString(config.FullyQualifiedDomainName), aws.ToString(config.IPAddress)) + " " + string(config.Type))
			}
			attributes := map[string]string{}
			recordLinkedService(attributes, check.LinkedService)
			g.Resources = append(g.Resources, harvest.Record(id, cmp.Or(name, id), "aws_route53_health_check", "aws", attributes))
		}
	}
	return nil
}

// recordName names a record set: its name, name, with the zone's domain,
// domain, left out (wildcard for *, the domain itself for the zone's
// apex), then its type and its set identifier, if it has one, as in
// "www CNAME blue".
func recordName(name, domain, recordType, setIdentifier string) string {
	relative := strings.TrimSuffix(strings.TrimSuffix(name, "."+domain), ".")
	switch {
	case name == domain:
		relative = strings.TrimSuffix(domain, ".")
	case relative == "*":
		relative = "wildcard"
	}
	return strings.TrimSpace(relative + " " + recordType + " " + setIdentifier)
}

// recordLinkedService records, as linked_service, the principal of the
// service that created a hosted zone or health check, linked, when another
// service did, such as servicediscovery.amazonaws.com for a Cloud Map
// namespace. Route 53 can't change or delete what another service created,
// which changes it as it pleases: the catalogue leaves it out.
func recordLinkedService(attributes map[string]string, linked *types.LinkedService) {
	if linked != nil {
		attributes["linked_service"] = aws.ToString(linked.ServicePrincipal)
	}
}
