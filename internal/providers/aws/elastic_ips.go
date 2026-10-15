// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
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
	resources := make([]harvest.Resource, 0, len(out.Addresses))
	for _, address := range out.Addresses {
		id := aws.ToString(address.AllocationId)
		if id == "" {
			continue
		}
		resources = append(resources, harvest.RecordBare(id, id, "aws_eip", "aws"))
	}
	g.Resources = resources
	return nil
}
