// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"log"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// ElasticIPLister lists the Elastic IP addresses of a region as aws_eip,
// each by its allocation ID, whether it is associated with anything or not.
type ElasticIPLister struct {
	Service
}

// List lists the Elastic IPs with one DescribeAddresses call; the
// API does not page. An address without an allocation ID, an EC2-Classic
// one, has nothing Terraform imports it by and is left out. A
// DescribeAddresses error fails the service.
//
// An address is named after what it belongs to, by ID, which the import
// names after that resource's Name tag: the instance it is associated
// with, or the NAT gateway that uses it. An address that belongs to
// neither, or whose NAT gateways can't be listed, which is logged, is
// named after its allocation ID. A Name tag of its own comes first.
func (g *ElasticIPLister) List() error {
	cfg, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ec2.NewFromConfig(cfg)
	out, err := client.DescribeAddresses(g.Context(), &ec2.DescribeAddressesInput{})
	if err != nil {
		return err
	}
	var gateways map[string]string
	resources := make([]harvest.Resource, 0, len(out.Addresses))
	for _, address := range out.Addresses {
		id := aws.ToString(address.AllocationId)
		if id == "" {
			continue
		}
		name := aws.ToString(address.InstanceId)
		if name == "" && aws.ToString(address.NetworkInterfaceId) != "" {
			if gateways == nil {
				gateways = g.natGatewaysByAllocation(client)
			}
			name = gateways[id]
		}
		if name == "" {
			name = id
		}
		resources = append(resources, harvest.RecordBare(id, name, "aws_eip", "aws"))
	}
	g.Resources = resources
	return nil
}

// natGatewaysByAllocation returns the ID of the NAT gateway that uses each
// Elastic IP, by allocation ID. A listing error is logged and ends the
// listing there: it only costs the addresses their names.
func (g *ElasticIPLister) natGatewaysByAllocation(client *ec2.Client) map[string]string {
	gateways := map[string]string{}
	pages := ec2.NewDescribeNatGatewaysPaginator(client, &ec2.DescribeNatGatewaysInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			log.Printf("aws: eip: can't list the NAT gateways that name Elastic IPs; naming them by allocation ID: %v", err)
			return gateways
		}
		for _, gateway := range page.NatGateways {
			for _, address := range gateway.NatGatewayAddresses {
				if allocation := aws.ToString(address.AllocationId); allocation != "" {
					gateways[allocation] = aws.ToString(gateway.NatGatewayId)
				}
			}
		}
	}
	return gateways
}
