// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"log"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/directconnect"
	"github.com/aws/aws-sdk-go-v2/service/directconnect/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// virtualInterfaceTypes maps the Direct Connect virtual interface types to
// their Terraform types. A type not among them is logged and left out.
var virtualInterfaceTypes = map[string]string{
	"private": "aws_dx_private_virtual_interface",
	"public":  "aws_dx_public_virtual_interface",
	"transit": "aws_dx_transit_virtual_interface",
}

// DirectConnectLister lists the service "dx": the Direct Connect virtual
// interfaces and connections of a region. The gateways, which are global,
// are the service "dx_gateway" (see DirectConnectGatewayLister).
type DirectConnectLister struct {
	Service
}

// List lists the virtual interfaces, then the connections, each by its ID,
// leaving out those being deleted, deleted or rejected: Direct Connect
// keeps reporting them for a while, and Terraform reads them as gone. Any
// error fails the service.
func (g *DirectConnectLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := directconnect.NewFromConfig(config)
	ctx := g.Context()
	if err := g.listVirtualInterfaces(ctx, client); err != nil {
		return err
	}
	return g.listConnections(ctx, client)
}

// DirectConnectGatewayLister lists the service "dx_gateway": the Direct
// Connect gateways. A gateway is global, and every region's API answers
// with all of them, so the service is listed once, in GlobalHomeRegion,
// not in each region, which would put the same gateway in several roots.
type DirectConnectGatewayLister struct {
	Service
}

// List lists the gateways by ID, leaving out those being deleted or
// deleted. Any error fails the service.
func (g *DirectConnectGatewayLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	return g.listGateways(g.Context(), directconnect.NewFromConfig(config))
}

// listGateways adds each Direct Connect gateway as an aws_dx_gateway,
// imported and named by its ID. The SDK has no paginator for it.
func (g *DirectConnectGatewayLister) listGateways(ctx context.Context, client *directconnect.Client) error {
	return paginateByMarker(func(token *string) (*string, error) {
		out, err := client.DescribeDirectConnectGateways(ctx, &directconnect.DescribeDirectConnectGatewaysInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, gateway := range out.DirectConnectGateways {
			switch gateway.DirectConnectGatewayState {
			case types.DirectConnectGatewayStateDeleting, types.DirectConnectGatewayStateDeleted:
				continue
			}
			id := aws.ToString(gateway.DirectConnectGatewayId)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_dx_gateway", "aws"))
		}
		return out.NextToken, nil
	})
}

// listVirtualInterfaces adds each private, public or transit virtual
// interface, imported by its ID and named after its name; other types are
// logged and skipped.
func (g *DirectConnectLister) listVirtualInterfaces(ctx context.Context, client *directconnect.Client) error {
	return paginateByMarker(func(token *string) (*string, error) {
		out, err := client.DescribeVirtualInterfaces(ctx, &directconnect.DescribeVirtualInterfacesInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, vif := range out.VirtualInterfaces {
			switch vif.VirtualInterfaceState {
			case types.VirtualInterfaceStateDeleting, types.VirtualInterfaceStateDeleted, types.VirtualInterfaceStateRejected:
				continue
			}
			id, kind := aws.ToString(vif.VirtualInterfaceId), aws.ToString(vif.VirtualInterfaceType)
			tfType, ok := virtualInterfaceTypes[kind]
			if !ok {
				log.Printf("aws: dx: skipping virtual interface %s of type %q, which is not imported", id, kind)
				continue
			}
			g.Resources = append(g.Resources, harvest.RecordBare(id, aws.ToString(vif.VirtualInterfaceName), tfType, "aws"))
		}
		return out.NextToken, nil
	})
}

// listConnections adds each connection as an aws_dx_connection, imported
// by its ID and named after its name.
func (g *DirectConnectLister) listConnections(ctx context.Context, client *directconnect.Client) error {
	return paginateByMarker(func(token *string) (*string, error) {
		out, err := client.DescribeConnections(ctx, &directconnect.DescribeConnectionsInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, connection := range out.Connections {
			switch connection.ConnectionState {
			case types.ConnectionStateDeleting, types.ConnectionStateDeleted, types.ConnectionStateRejected:
				continue
			}
			g.Resources = append(g.Resources, harvest.RecordBare(
				aws.ToString(connection.ConnectionId), aws.ToString(connection.ConnectionName), "aws_dx_connection", "aws"))
		}
		return out.NextToken, nil
	})
}
