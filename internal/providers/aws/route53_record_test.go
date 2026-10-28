// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

const recordRoute53NS = `xmlns="https://route53.amazonaws.com/doc/2013-04-01/"`

func recordRoute53Error() string {
	return `<ErrorResponse ` + recordRoute53NS + `><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>recorded failure</Message></Error><RequestId>r-1</RequestId></ErrorResponse>`
}

// recordRoute53API answers the Route 53 lister: zones Z1 (public, records
// on two truncated pages) and Z2 (private, records that can't be listed)
// on two pages, and two pages of health checks. Paths in failing fail.
func recordRoute53API(t *testing.T, failing ...string) func(apiCall) string {
	return func(call apiCall) string {
		for _, path := range failing {
			if call.Path == path {
				return recordRoute53Error()
			}
		}
		switch call.Path {
		case "/2013-04-01/hostedzone":
			if recordSecondPage(call) {
				return `<ListHostedZonesResponse ` + recordRoute53NS + `><HostedZones>
					<HostedZone><Id>/hostedzone/Z2</Id><Name>internal.example.</Name><CallerReference>c-2</CallerReference><Config><PrivateZone>true</PrivateZone></Config></HostedZone>
					</HostedZones><IsTruncated>false</IsTruncated><MaxItems>100</MaxItems></ListHostedZonesResponse>`
			}
			return `<ListHostedZonesResponse ` + recordRoute53NS + `><HostedZones>
				<HostedZone><Id>/hostedzone/Z1</Id><Name>example.com.</Name><CallerReference>c-1</CallerReference><Config><PrivateZone>false</PrivateZone></Config><ResourceRecordSetCount>5</ResourceRecordSetCount></HostedZone>
				</HostedZones><IsTruncated>true</IsTruncated><NextMarker>` + nextPageToken + `</NextMarker><MaxItems>100</MaxItems></ListHostedZonesResponse>`
		case "/2013-04-01/hostedzone/Z1/rrset":
			if call.Query.Get("name") != "" {
				if got := call.Query.Get("name") + " " + call.Query.Get("type") + " " + call.Query.Get("identifier"); got != "www.example.com. CNAME blue" {
					t.Errorf("records continued from %q", got)
				}
				return `<ListResourceRecordSetsResponse ` + recordRoute53NS + `><ResourceRecordSets>
					<ResourceRecordSet><Name>www.example.com.</Name><Type>CNAME</Type><SetIdentifier>blue</SetIdentifier><Weight>90</Weight><TTL>60</TTL><ResourceRecords><ResourceRecord><Value>blue.example.com</Value></ResourceRecord></ResourceRecords></ResourceRecordSet>
					<ResourceRecordSet><Name>www.example.com.</Name><Type>CNAME</Type><SetIdentifier>green</SetIdentifier><Weight>10</Weight><TTL>60</TTL><ResourceRecords><ResourceRecord><Value>green.example.com</Value></ResourceRecord></ResourceRecords></ResourceRecordSet>
					</ResourceRecordSets><IsTruncated>false</IsTruncated><MaxItems>300</MaxItems></ListResourceRecordSetsResponse>`
			}
			return `<ListResourceRecordSetsResponse ` + recordRoute53NS + `><ResourceRecordSets>
				<ResourceRecordSet><Name>example.com.</Name><Type>NS</Type><TTL>172800</TTL><ResourceRecords><ResourceRecord><Value>ns-1.awsdns-01.org.</Value></ResourceRecord></ResourceRecords></ResourceRecordSet>
				<ResourceRecordSet><Name>example.com.</Name><Type>SOA</Type><TTL>900</TTL><ResourceRecords><ResourceRecord><Value>ns-1.awsdns-01.org. awsdns-hostmaster.amazon.com. 1 7200 900 1209600 86400</Value></ResourceRecord></ResourceRecords></ResourceRecordSet>
				<ResourceRecordSet><Name>\052.example.com.</Name><Type>A</Type><AliasTarget><HostedZoneId>Z35SXDOTRQ7X7K</HostedZoneId><DNSName>web-1.us-east-1.elb.amazonaws.com.</DNSName><EvaluateTargetHealth>false</EvaluateTargetHealth></AliasTarget></ResourceRecordSet>
				</ResourceRecordSets><IsTruncated>true</IsTruncated><NextRecordName>www.example.com.</NextRecordName><NextRecordType>CNAME</NextRecordType><NextRecordIdentifier>blue</NextRecordIdentifier><MaxItems>3</MaxItems></ListResourceRecordSetsResponse>`
		case "/2013-04-01/hostedzone/Z2/rrset":
			return recordRoute53Error()
		case "/2013-04-01/healthcheck":
			id, token := recordPage(call, "hc")
			typ := map[string]string{"hc-1": "HTTPS", "hc-2": "TCP"}[id]
			return fmt.Sprintf(`<ListHealthChecksResponse %s><HealthChecks>
				<HealthCheck><Id>%s</Id><CallerReference>c-%s</CallerReference><HealthCheckConfig><Type>%s</Type><FullyQualifiedDomainName>example.com</FullyQualifiedDomainName></HealthCheckConfig><HealthCheckVersion>1</HealthCheckVersion></HealthCheck>
				</HealthChecks><IsTruncated>%t</IsTruncated><Marker></Marker><NextMarker>%s</NextMarker><MaxItems>100</MaxItems></ListHealthChecksResponse>`,
				recordRoute53NS, id, id, typ, token != "", token)
		}
		return recordUnexpected(t, call)
	}
}

// The lister lists each zone followed by all its records, NS and SOA
// included, then the health checks. A record is imported as
// <zone>_<name>_<type>_<set identifier>, its name with the trailing dot and
// with \052 unescaped to *, and records whether it is at the zone's apex.
func TestRecordRoute53ListsZonesRecordsAndHealthChecks(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, recordRoute53API(t))
	g := &Route53Lister{}
	g.SetContext(ctx)

	// Zone Z2's records fail to list: the error says so, and is partial,
	// as the other zone and the health checks are listed all the same.
	if err := g.List(); !harvest.IsPartial(err) || !strings.Contains(err.Error(), "records of zone Z2") {
		t.Fatalf("want the partial error of zone Z2's records, got %v", err)
	}

	record := func(id, label, name, typ, setID, apex string) recordedResource {
		return recordedResource{"aws_route53_record", id, label, map[string]string{
			"name": name, "zone_id": "Z1", "type": typ, "set_identifier": setID, "apex": apex}}
	}
	assertRecorded(t, g.Resources,
		recordedResource{"aws_route53_zone", "Z1", "example.com", map[string]string{"name": "example.com.", "force_destroy": "false"}},
		record("Z1_example.com._NS_", "example.com NS", "example.com", "NS", "", "true"),
		record("Z1_example.com._SOA_", "example.com SOA", "example.com", "SOA", "", "true"),
		record("Z1_*.example.com._A_", "wildcard A", "*.example.com", "A", "", "false"),
		record("Z1_www.example.com._CNAME_blue", "www CNAME blue", "www.example.com", "CNAME", "blue", "false"),
		record("Z1_www.example.com._CNAME_green", "www CNAME green", "www.example.com", "CNAME", "green", "false"),
		// A zone whose records can't be listed is listed without them.
		recordedResource{"aws_route53_zone", "Z2", "internal.example", map[string]string{"name": "internal.example.", "force_destroy": "false"}},
		recordedResource{"aws_route53_health_check", "hc-1", "example.com HTTPS", nil},
		recordedResource{"aws_route53_health_check", "hc-2", "example.com TCP", nil},
	)
}

// Errors are returned, and what was listed besides is kept. A zone list
// that fails fails the service; health checks that fail to list, once the
// zones are listed, leave the listing partial.
func TestRecordRoute53ReturnsErrors(t *testing.T) {
	t.Parallel()
	t.Run("zones", func(t *testing.T) {
		ctx := useFakeAPI(t, recordRoute53API(t, "/2013-04-01/hostedzone"))
		g := &Route53Lister{}
		g.SetContext(ctx)

		if err := g.List(); err == nil || harvest.IsPartial(err) {
			t.Errorf("want the error returned, not partial, got %v", err)
		}

		assertRecorded(t, g.Resources,
			recordedResource{"aws_route53_health_check", "hc-1", "example.com HTTPS", nil},
			recordedResource{"aws_route53_health_check", "hc-2", "example.com TCP", nil},
		)
	})
	t.Run("health checks", func(t *testing.T) {
		ctx := useFakeAPI(t, recordRoute53API(t, "/2013-04-01/healthcheck"))
		g := &Route53Lister{}
		g.SetContext(ctx)

		if err := g.List(); !harvest.IsPartial(err) || !strings.Contains(err.Error(), "health checks") {
			t.Errorf("want the partial error of the health checks, got %v", err)
		}

		assertIDs(t, g.Resources, "aws_route53_zone", "Z1", "Z2")
		assertIDs(t, g.Resources, "aws_route53_health_check")
	})
}
