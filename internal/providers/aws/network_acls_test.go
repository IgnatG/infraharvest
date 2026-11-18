// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The lister lists every network ACL by ID, across pages: a VPC's default
// ACL as aws_default_network_acl, any other as aws_network_acl.
func TestRecordNaclListsNetworkACLs(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeNetworkAcls" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return recordEC2(call.Op, `<networkAclSet>
				<item><networkAclId>acl-unflagged</networkAclId><vpcId>vpc-2</vpcId></item>
				</networkAclSet><nextToken></nextToken>`)
		}
		return recordEC2(call.Op, fmt.Sprintf(`<networkAclSet>
			<item><networkAclId>acl-default</networkAclId><vpcId>vpc-1</vpcId><default>true</default></item>
			<item><networkAclId>acl-private</networkAclId><vpcId>vpc-1</vpcId><default>false</default>
				<entrySet><item><ruleNumber>100</ruleNumber><protocol>-1</protocol><ruleAction>allow</ruleAction><egress>false</egress><cidrBlock>10.0.0.0/16</cidrBlock></item></entrySet></item>
			</networkAclSet><nextToken>%s</nextToken>`, nextPageToken))
	})
	g := &NaclLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_default_network_acl", "acl-default", "acl-default", map[string]string{"as_created": "true"}},
		recordedResource{"aws_network_acl", "acl-private", "acl-private", nil},
		recordedResource{"aws_network_acl", "acl-unflagged", "acl-unflagged", nil},
	)
}

func TestRecordNaclReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordEC2Error()
	})
	g := &NaclLister{}

	assertRecordedError(t, listError(ctx, g), "UnauthorizedOperation")
}

// A default ACL is as AWS created it when it has no tags and no rules but
// the allow-all ones AWS gives it, IPv4 and IPv6, besides the catch-all
// denies: only then is it recorded as_created, which the catalogue skips
// by default. One that is tagged or has rules of its own, as the
// terraform-aws-vpc module's, is not.
func TestRecordNaclMarksDefaultACLsAsCreated(t *testing.T) {
	t.Parallel()
	const allowAll = `<item><ruleNumber>100</ruleNumber><protocol>-1</protocol><ruleAction>allow</ruleAction><egress>false</egress><cidrBlock>0.0.0.0/0</cidrBlock></item>
		<item><ruleNumber>100</ruleNumber><protocol>-1</protocol><ruleAction>allow</ruleAction><egress>true</egress><cidrBlock>0.0.0.0/0</cidrBlock></item>
		<item><ruleNumber>32767</ruleNumber><protocol>-1</protocol><ruleAction>deny</ruleAction><egress>false</egress><cidrBlock>0.0.0.0/0</cidrBlock></item>
		<item><ruleNumber>32767</ruleNumber><protocol>-1</protocol><ruleAction>deny</ruleAction><egress>true</egress><cidrBlock>0.0.0.0/0</cidrBlock></item>`
	const allowAllIPv6 = `<item><ruleNumber>101</ruleNumber><protocol>-1</protocol><ruleAction>allow</ruleAction><egress>false</egress><ipv6CidrBlock>::/0</ipv6CidrBlock></item>
		<item><ruleNumber>101</ruleNumber><protocol>-1</protocol><ruleAction>allow</ruleAction><egress>true</egress><ipv6CidrBlock>::/0</ipv6CidrBlock></item>
		<item><ruleNumber>32768</ruleNumber><protocol>-1</protocol><ruleAction>deny</ruleAction><egress>false</egress><ipv6CidrBlock>::/0</ipv6CidrBlock></item>`
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeNetworkAcls" {
			return recordUnexpected(t, call)
		}
		acl := func(id, entries, extra string) string {
			return `<item><networkAclId>` + id + `</networkAclId><vpcId>vpc-1</vpcId><default>true</default><entrySet>` + entries + `</entrySet>` + extra + `</item>`
		}
		return recordEC2(call.Op, `<networkAclSet>`+
			acl("acl-new", allowAll, "")+
			acl("acl-new-ipv6", allowAll+allowAllIPv6, "")+
			acl("acl-tagged", allowAll, `<tagSet><item><key>Name</key><value>main-default</value></item></tagSet>`)+
			acl("acl-hardened", allowAll+`<item><ruleNumber>90</ruleNumber><protocol>6</protocol><ruleAction>deny</ruleAction><egress>false</egress><cidrBlock>0.0.0.0/0</cidrBlock><portRange><from>22</from><to>22</to></portRange></item>`, "")+
			acl("acl-narrowed", `<item><ruleNumber>100</ruleNumber><protocol>-1</protocol><ruleAction>allow</ruleAction><egress>false</egress><cidrBlock>10.0.0.0/16</cidrBlock></item>`, "")+
			`</networkAclSet>`)
	})
	g := &NaclLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	asCreated := map[string]string{"as_created": "true"}
	assertRecorded(t, resources,
		recordedResource{"aws_default_network_acl", "acl-new", "acl-new", asCreated},
		recordedResource{"aws_default_network_acl", "acl-new-ipv6", "acl-new-ipv6", asCreated},
		recordedResource{"aws_default_network_acl", "acl-tagged", "acl-tagged", nil},
		recordedResource{"aws_default_network_acl", "acl-hardened", "acl-hardened", nil},
		recordedResource{"aws_default_network_acl", "acl-narrowed", "acl-narrowed", nil},
	)
}
