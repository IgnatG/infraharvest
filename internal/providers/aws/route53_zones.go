// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"log"
	"strings"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
)

// Route53Lister lists the global service "route53": the hosted zones,
// public and private, each with all its record sets, and the health
// checks.
type Route53Lister struct {
	Service
}

// List lists the zones with their records, then the health
// checks. Listing errors are logged, not returned: what was listed before
// one is kept, and the rest of the service is still listed.
func (g *Route53Lister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := route53.NewFromConfig(config)
	g.listHostedZones(client)
	g.listHealthChecks(client)
	return nil
}

// listHostedZones records every hosted zone by its ID without the
// "/hostedzone/" prefix, named "<ID>_<domain>", each followed by its
// records. The zone's domain is recorded with its trailing dot.
func (g *Route53Lister) listHostedZones(client *route53.Client) {
	p := route53.NewListHostedZonesPaginator(client, &route53.ListHostedZonesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			log.Printf("aws: route53: hosted zones: %v", err)
			return
		}
		for _, zone := range page.HostedZones {
			zoneID := strings.TrimPrefix(aws.ToString(zone.Id), "/hostedzone/")
			domain := aws.ToString(zone.Name)
			g.Resources = append(g.Resources, harvest.Record(zoneID, zoneID+"_"+strings.TrimSuffix(domain, "."), "aws_route53_zone", "aws", map[string]string{
				"name":          domain,
				"force_destroy": "false",
			}))
			g.listRecords(client, zoneID)
		}
	}
}

// recordPosition is where a page of record sets starts: Route 53 pages
// record sets by name, type and set identifier together.
type recordPosition struct {
	name, recordType, identifier string
}

// listRecords records every record set of the zone zoneID, NS and SOA
// included. The pages are walked by hand, from the name, type and set
// identifier that each truncated page gives for the next, and stop should
// a page point at the position it was asked for. An error is logged; the
// records read before it are kept.
func (g *Route53Lister) listRecords(client *route53.Client, zoneID string) {
	input := &route53.ListResourceRecordSetsInput{HostedZoneId: aws.String(zoneID)}
	for {
		page, err := client.ListResourceRecordSets(g.Context(), input)
		if err != nil {
			log.Printf("aws: route53: records of zone %s: %v", zoneID, err)
			return
		}
		for _, recordSet := range page.ResourceRecordSets {
			g.addRecord(zoneID, recordSet)
		}
		if !page.IsTruncated {
			return
		}
		asked := recordPosition{aws.ToString(input.StartRecordName), string(input.StartRecordType), aws.ToString(input.StartRecordIdentifier)}
		next := recordPosition{aws.ToString(page.NextRecordName), string(page.NextRecordType), aws.ToString(page.NextRecordIdentifier)}
		if next == asked {
			log.Printf("aws: route53: records of zone %s: pagination did not advance past %q %s %q", zoneID, next.name, next.recordType, next.identifier)
			return
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
func (g *Route53Lister) addRecord(zoneID string, recordSet types.ResourceRecordSet) {
	name := strings.Replace(aws.ToString(recordSet.Name), `\052`, "*", 1)
	recordType := string(recordSet.Type)
	setIdentifier := aws.ToString(recordSet.SetIdentifier)
	id := zoneID + "_" + name + "_" + recordType + "_" + setIdentifier
	g.Resources = append(g.Resources, harvest.Record(id, id, "aws_route53_record", "aws", map[string]string{
		"name":           strings.TrimSuffix(name, "."),
		"zone_id":        zoneID,
		"type":           recordType,
		"set_identifier": setIdentifier,
	}))
}

// listHealthChecks records every health check by ID, named
// "<ID>_<check type>". An error is logged; the checks read before it are
// kept.
func (g *Route53Lister) listHealthChecks(client *route53.Client) {
	p := route53.NewListHealthChecksPaginator(client, &route53.ListHealthChecksInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			log.Printf("aws: route53: health checks: %v", err)
			return
		}
		for _, check := range page.HealthChecks {
			id := aws.ToString(check.Id)
			checkType := ""
			if check.HealthCheckConfig != nil {
				checkType = string(check.HealthCheckConfig.Type)
			}
			g.Resources = append(g.Resources, harvest.RecordBare(id, id+"_"+checkType, "aws_route53_health_check", "aws"))
		}
	}
}
