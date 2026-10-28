// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"maps"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

const recordRouteTablesPage1 = `<routeTableSet>
	<item><routeTableId>rtb-main</routeTableId><vpcId>vpc-1</vpcId><ownerId>123456789012</ownerId>
		<associationSet>
			<item><main>true</main><routeTableAssociationId>rtbassoc-main</routeTableAssociationId><routeTableId>rtb-main</routeTableId><associationState><state>associated</state></associationState></item>
			<item><main>false</main><routeTableAssociationId>rtbassoc-a</routeTableAssociationId><routeTableId>rtb-main</routeTableId><subnetId>subnet-a</subnetId><associationState><state>associated</state></associationState></item>
		</associationSet>
		<routeSet><item><destinationCidrBlock>10.0.0.0/16</destinationCidrBlock><gatewayId>local</gatewayId><state>active</state><origin>CreateRouteTable</origin></item></routeSet></item>
	</routeTableSet>`

// The lister lists each route table by ID, across pages, followed by its
// associations, each by its association ID: the main one as
// aws_main_route_table_association named after the VPC, a subnet's or a
// gateway's as aws_route_table_association named after the subnet or
// gateway. An association with none of these is left out. A main route
// table is named after its VPC; one table without a subnet or with
// several, after its ID.
func TestRecordRouteTableListsTablesAndAssociations(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeRouteTables" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return recordEC2(call.Op, `<routeTableSet>
				<item><routeTableId>rtb-edge</routeTableId><vpcId>vpc-1</vpcId>
					<associationSet>
						<item><main>false</main><routeTableAssociationId>rtbassoc-igw</routeTableAssociationId><routeTableId>rtb-edge</routeTableId><gatewayId>igw-1</gatewayId></item>
						<item><main>false</main><routeTableAssociationId>rtbassoc-odd</routeTableAssociationId><routeTableId>rtb-edge</routeTableId></item>
					</associationSet></item>
				<item><routeTableId>rtb-lonely</routeTableId><vpcId>vpc-2</vpcId><associationSet/></item>
				</routeTableSet><nextToken></nextToken>`)
		}
		return recordEC2(call.Op, fmt.Sprintf(`%s<nextToken>%s</nextToken>`, recordRouteTablesPage1, nextPageToken))
	})
	g := &RouteTableLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_route_table", "rtb-main", "vpc-1 main", map[string]string{"main": "true", "vpc_id": "vpc-1"}},
		recordedResource{"aws_main_route_table_association", "rtbassoc-main", "vpc-1", map[string]string{
			"vpc_id": "vpc-1", "route_table_id": "rtb-main"}},
		recordedResource{"aws_route_table_association", "rtbassoc-a", "subnet-a", map[string]string{
			"subnet_id": "subnet-a", "route_table_id": "rtb-main"}},
		recordedResource{"aws_route_table", "rtb-edge", "rtb-edge", map[string]string{"vpc_id": "vpc-1"}},
		recordedResource{"aws_route_table_association", "rtbassoc-igw", "igw-1", map[string]string{
			"gateway_id": "igw-1", "route_table_id": "rtb-edge"}},
		recordedResource{"aws_route_table", "rtb-lonely", "rtb-lonely", map[string]string{"vpc_id": "vpc-2"}},
	)
}

// A main route table is as AWS created it when it has no tags, no routes
// but the local ones, no propagated routes, and no association but the
// main one: only then is it recorded as_created, which the catalogue
// skips by default.
func TestRecordRouteTableMarksMainTablesAsCreated(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeRouteTables" {
			return recordUnexpected(t, call)
		}
		mainTable := func(id, extra string) string {
			return `<item><routeTableId>` + id + `</routeTableId><vpcId>vpc-1</vpcId>
				<associationSet><item><main>true</main><routeTableAssociationId>rtbassoc-` + id + `</routeTableAssociationId><routeTableId>` + id + `</routeTableId></item></associationSet>
				<routeSet><item><destinationCidrBlock>10.0.0.0/16</destinationCidrBlock><gatewayId>local</gatewayId></item>
					<item><destinationIpv6CidrBlock>2001:db8::/56</destinationIpv6CidrBlock><gatewayId>local</gatewayId></item></routeSet>` + extra + `</item>`
		}
		return recordEC2(call.Op, `<routeTableSet>`+
			mainTable("rtb-new", "")+
			mainTable("rtb-tagged", `<tagSet><item><key>Name</key><value>main</value></item></tagSet>`)+
			mainTable("rtb-propagating", `<propagatingVgwSet><item><gatewayId>vgw-1</gatewayId></item></propagatingVgwSet>`)+
			`</routeTableSet>`)
	})
	g := &RouteTableLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	got := map[string]map[string]string{}
	for _, r := range g.Resources {
		if r.Address.Type == "aws_route_table" {
			got[r.Observed.ID] = r.Observed.Attributes
		}
	}
	want := map[string]map[string]string{
		"rtb-new":         {"main": "true", "as_created": "true", "vpc_id": "vpc-1"},
		"rtb-tagged":      {"main": "true", "vpc_id": "vpc-1"},
		"rtb-propagating": {"main": "true", "vpc_id": "vpc-1"},
	}
	for id, attributes := range want {
		if !maps.Equal(got[id], attributes) {
			t.Errorf("%s: recorded %v, want %v", id, got[id], attributes)
		}
	}
}

// An error on any page fails the service.
func TestRecordRouteTableReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if recordSecondPage(call) {
			return recordEC2Error()
		}
		return recordEC2(call.Op, fmt.Sprintf(`%s<nextToken>%s</nextToken>`, recordRouteTablesPage1, nextPageToken))
	})
	g := &RouteTableLister{}
	g.SetContext(ctx)

	assertRecordedError(t, g.List(), "UnauthorizedOperation")
}

// A route table without a Name tag is named after what it serves, by ID,
// which the import names after that resource: before, it was labelled
// rtb_0a1b2c3d.
func TestRouteTableName(t *testing.T) {
	t.Parallel()
	association := func(main bool, subnet string) types.RouteTableAssociation {
		return types.RouteTableAssociation{Main: aws.Bool(main), SubnetId: aws.String(subnet)}
	}
	for _, tc := range []struct {
		associations []types.RouteTableAssociation
		want         string
	}{
		{[]types.RouteTableAssociation{association(false, "subnet-a"), association(true, "")}, "vpc-1 main"},
		{[]types.RouteTableAssociation{association(false, "subnet-a")}, "subnet-a"},
		{[]types.RouteTableAssociation{association(false, "subnet-a"), association(false, "subnet-b")}, "rtb-1"},
		{nil, "rtb-1"},
	} {
		table := &types.RouteTable{RouteTableId: aws.String("rtb-1"), VpcId: aws.String("vpc-1"), Associations: tc.associations}
		if got := routeTableName(table); got != tc.want {
			t.Errorf("routeTableName(%+v): got %q, want %q", tc.associations, got, tc.want)
		}
	}
}
