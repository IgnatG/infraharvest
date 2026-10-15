// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"
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
// with \052 unescaped to *.
func TestRecordRoute53ListsZonesRecordsAndHealthChecks(t *testing.T) {
	useFakeAPI(t, recordRoute53API(t))
	g := &Route53Lister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	record := func(id, name, typ, setID string) recordedResource {
		return recordedResource{"aws_route53_record", id, id, map[string]string{
			"name": name, "zone_id": "Z1", "type": typ, "set_identifier": setID}}
	}
	assertRecorded(t, g.Resources,
		recordedResource{"aws_route53_zone", "Z1", "Z1_example.com", map[string]string{"name": "example.com.", "force_destroy": "false"}},
		record("Z1_example.com._NS_", "example.com", "NS", ""),
		record("Z1_example.com._SOA_", "example.com", "SOA", ""),
		record("Z1_*.example.com._A_", "*.example.com", "A", ""),
		record("Z1_www.example.com._CNAME_blue", "www.example.com", "CNAME", "blue"),
		record("Z1_www.example.com._CNAME_green", "www.example.com", "CNAME", "green"),
		// A zone whose records can't be listed is listed without them.
		recordedResource{"aws_route53_zone", "Z2", "Z2_internal.example", map[string]string{"name": "internal.example.", "force_destroy": "false"}},
		recordedResource{"aws_route53_health_check", "hc-1", "hc-1_HTTPS", nil},
		recordedResource{"aws_route53_health_check", "hc-2", "hc-2_TCP", nil},
	)
}

// Errors are logged, not returned; what was listed before one is kept.
func TestRecordRoute53SwallowsErrors(t *testing.T) {
	t.Run("zones", func(t *testing.T) {
		useFakeAPI(t, recordRoute53API(t, "/2013-04-01/hostedzone"))
		g := &Route53Lister{}

		if err := g.List(); err != nil {
			t.Errorf("want errors swallowed, got %v", err)
		}

		assertRecorded(t, g.Resources,
			recordedResource{"aws_route53_health_check", "hc-1", "hc-1_HTTPS", nil},
			recordedResource{"aws_route53_health_check", "hc-2", "hc-2_TCP", nil},
		)
	})
	t.Run("health checks", func(t *testing.T) {
		useFakeAPI(t, recordRoute53API(t, "/2013-04-01/healthcheck"))
		g := &Route53Lister{}

		if err := g.List(); err != nil {
			t.Errorf("want errors swallowed, got %v", err)
		}

		assertIDs(t, g.Resources, "aws_route53_zone", "Z1", "Z2")
		assertIDs(t, g.Resources, "aws_route53_health_check")
	})
}
