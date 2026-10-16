// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"regexp"
	"strings"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// VpcEndpointLister lists the VPC endpoints of a region as
// aws_vpc_endpoint, each by its ID. Endpoints of every type (gateway,
// interface, load balancer) and in every state are listed, each named after
// its service (see vpcEndpointName).
type VpcEndpointLister struct {
	Service
}

// List lists every VPC endpoint, page by page. An API error is
// returned and ends the listing.
func (g *VpcEndpointLister) List() error {
	cfg, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ec2.NewFromConfig(cfg)

	pages := ec2.NewDescribeVpcEndpointsPaginator(client, &ec2.DescribeVpcEndpointsInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, endpoint := range page.VpcEndpoints {
			id := aws.ToString(endpoint.VpcEndpointId)
			g.Resources = append(g.Resources, harvest.RecordBare(id, vpcEndpointName(aws.ToString(endpoint.ServiceName), id), "aws_vpc_endpoint", "aws"))
		}
	}
	return nil
}

// regionPart matches a region in a service name: eu-west-2, us-gov-east-1.
var regionPart = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)

// vpcEndpointName names an endpoint after what follows the region in its
// service's name: s3 for com.amazonaws.eu-west-2.s3, ecr.dkr for
// com.amazonaws.eu-west-2.ecr.dkr. An endpoint service of another account
// (com.amazonaws.vpce.eu-west-2.vpce-svc-...) is named after its own ID.
func vpcEndpointName(serviceName, id string) string {
	parts := strings.Split(serviceName, ".")
	for i, part := range parts {
		if regionPart.MatchString(part) && i+1 < len(parts) {
			if rest := strings.Join(parts[i+1:], "."); !strings.HasPrefix(rest, "vpce-svc-") {
				return rest
			}
			break
		}
	}
	return id
}
