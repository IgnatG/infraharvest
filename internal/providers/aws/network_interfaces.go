// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// NetworkInterfaceLister lists the service "eni": the network interfaces
// of a region that someone created, as aws_network_interface, each by its
// ID.
//
// Most network interfaces are AWS's, for a service: Lambda's, a load
// balancer's, a NAT gateway's, a VPC endpoint's, an ECS task's or an RDS
// instance's. DescribeNetworkInterfaces is asked for those AWS doesn't
// manage, of the types people create (interface, efa, efa-only, trunk),
// so the others aren't listed at all. An instance's primary interface,
// which the instance creates and deletes, is the instance's too.
type NetworkInterfaceLister struct {
	Service
}

// ownInterfaceTypes are the network interface types people create; the
// other types are AWS services'.
var ownInterfaceTypes = []string{"interface", "efa", "efa-only", "trunk"}

// List lists the network interfaces, page by page. An API error is
// returned and ends the listing.
func (g *NetworkInterfaceLister) List() error {
	cfg, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ec2.NewFromConfig(cfg)
	input := &ec2.DescribeNetworkInterfacesInput{Filters: []types.Filter{
		{Name: aws.String("requester-managed"), Values: []string{"false"}},
		{Name: aws.String("interface-type"), Values: ownInterfaceTypes},
	}}
	pages := ec2.NewDescribeNetworkInterfacesPaginator(client, input, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, eni := range page.NetworkInterfaces {
			if primaryOfInstance(eni) || aws.ToBool(eni.RequesterManaged) {
				continue
			}
			id := aws.ToString(eni.NetworkInterfaceId)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_network_interface", "aws"))
		}
	}
	return nil
}

// primaryOfInstance reports whether eni is an instance's primary network
// interface, at device index 0, which the instance owns.
func primaryOfInstance(eni types.NetworkInterface) bool {
	a := eni.Attachment
	return a != nil && aws.ToString(a.InstanceId) != "" && aws.ToInt32(a.DeviceIndex) == 0
}
