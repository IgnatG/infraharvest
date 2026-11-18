// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"slices"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// recordDirectConnectAPI answers Direct Connect with two pages of gateways
// and a deleted one, a private, a public, a transit and a deleted virtual
// interface, and a connection and a rejected one. The operations in
// failing answer with an error.
func recordDirectConnectAPI(t *testing.T, failing ...string) func(apiCall) string {
	return func(call apiCall) string {
		for _, op := range failing {
			if call.Op == op {
				return recordJSONError("DirectConnectClientException")
			}
		}
		switch call.Op {
		case "DescribeDirectConnectGateways":
			if recordSecondPage(call) {
				return `{"directConnectGateways":[{"directConnectGatewayId":"dxgw-2","directConnectGatewayName":"backup","amazonSideAsn":64513,"directConnectGatewayState":"available"},
					{"directConnectGatewayId":"dxgw-old","directConnectGatewayName":"old","amazonSideAsn":64514,"directConnectGatewayState":"deleted"}]}`
			}
			return fmt.Sprintf(`{"directConnectGateways":[{"directConnectGatewayId":"dxgw-1","directConnectGatewayName":"main","amazonSideAsn":64512,"directConnectGatewayState":"available"}],
				"nextToken":%q}`, nextPageToken)
		case "DescribeVirtualInterfaces":
			return `{"virtualInterfaces":[
				{"virtualInterfaceId":"dxvif-1","virtualInterfaceName":"app-private","virtualInterfaceType":"private","connectionId":"dxcon-1","vlan":101},
				{"virtualInterfaceId":"dxvif-2","virtualInterfaceName":"app-public","virtualInterfaceType":"public","connectionId":"dxcon-1","vlan":102},
				{"virtualInterfaceId":"dxvif-3","virtualInterfaceName":"app-transit","virtualInterfaceType":"transit","connectionId":"dxcon-1","vlan":103},
				{"virtualInterfaceId":"dxvif-4","virtualInterfaceName":"app-old","virtualInterfaceType":"private","virtualInterfaceState":"deleted","connectionId":"dxcon-1","vlan":104}]}`
		case "DescribeConnections":
			return `{"connections":[{"connectionId":"dxcon-1","connectionName":"primary","connectionState":"available","bandwidth":"10Gbps","location":"EqDC2"},
				{"connectionId":"dxcon-2","connectionName":"refused","connectionState":"rejected","bandwidth":"1Gbps","location":"EqDC2"}]}`
		}
		return recordUnexpected(t, call)
	}
}

// The dx lister lists, in order, private, public and transit virtual
// interfaces, then connections, leaving out those deleted or rejected. It
// doesn't ask for the gateways, which are global: dx_gateway lists them
// once.
func TestRecordDirectConnectListsInterfacesAndConnections(t *testing.T) {
	t.Parallel()
	api := recordDirectConnectAPI(t)
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op == "DescribeDirectConnectGateways" {
			return recordUnexpected(t, call)
		}
		return api(call)
	})
	g := &DirectConnectLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_dx_private_virtual_interface", "dxvif-1", "app-private", nil},
		recordedResource{"aws_dx_public_virtual_interface", "dxvif-2", "app-public", nil},
		recordedResource{"aws_dx_transit_virtual_interface", "dxvif-3", "app-transit", nil},
		recordedResource{"aws_dx_connection", "dxcon-1", "primary", nil},
	)
}

// The dx_gateway lister lists the gateways across pages, leaving out a
// deleted one.
func TestRecordDirectConnectGatewayListsGateways(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, recordDirectConnectAPI(t))
	g := &DirectConnectGatewayLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_dx_gateway", "dxgw-1", "dxgw-1", nil},
		recordedResource{"aws_dx_gateway", "dxgw-2", "dxgw-2", nil},
	)
}

// Dx_gateway is listed once, in us-east-1, not in every region.
func TestDirectConnectGatewaysAreListedOnce(t *testing.T) {
	t.Parallel()
	if !slices.Contains(EastOnlyServices, "dx_gateway") {
		t.Errorf("dx_gateway isn't among the services listed once in %s: %v", GlobalHomeRegion, EastOnlyServices)
	}
}

// Each list fails its service.
func TestRecordDirectConnectReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"DescribeVirtualInterfaces", "DescribeConnections"} {
		t.Run(failing, func(t *testing.T) {
			ctx := useFakeAPI(t, recordDirectConnectAPI(t, failing))
			g := &DirectConnectLister{}

			assertRecordedError(t, listError(ctx, g), "DirectConnectClientException")
		})
	}
	ctx := useFakeAPI(t, recordDirectConnectAPI(t, "DescribeDirectConnectGateways"))
	g := &DirectConnectGatewayLister{}

	assertRecordedError(t, listError(ctx, g), "DirectConnectClientException")
}
