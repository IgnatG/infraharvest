// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"
)

// The lister lists every network ACL by ID, across pages: a VPC's default
// ACL as aws_default_network_acl (the catalogue skips those), any other as
// aws_network_acl.
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
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_default_network_acl", "acl-default", "acl-default", nil},
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
	g.SetContext(ctx)

	assertRecordedError(t, g.List(), "UnauthorizedOperation")
}
