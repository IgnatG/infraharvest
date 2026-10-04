// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "github.com/IgnatG/infraharvest/terraformutils"

// DataSources names the data sources that read a resource by its import
// ID, and whose id is that ID, so that configuration can refer to
// resources the selection leaves out: data.aws_vpc.<label>.id.
func (AWSProvider) DataSources() map[string]terraformutils.DataSource {
	return map[string]terraformutils.DataSource{
		"aws_iam_policy":       {Type: "aws_iam_policy", Argument: "arn"},
		"aws_iam_role":         {Type: "aws_iam_role", Argument: "name"},
		"aws_internet_gateway": {Type: "aws_internet_gateway", Argument: "internet_gateway_id"},
		"aws_kms_key":          {Type: "aws_kms_key", Argument: "key_id"},
		"aws_nat_gateway":      {Type: "aws_nat_gateway", Argument: "id"},
		"aws_route53_zone":     {Type: "aws_route53_zone", Argument: "zone_id"},
		"aws_route_table":      {Type: "aws_route_table", Argument: "route_table_id"},
		"aws_s3_bucket":        {Type: "aws_s3_bucket", Argument: "bucket"},
		"aws_security_group":   {Type: "aws_security_group", Argument: "id"},
		"aws_subnet":           {Type: "aws_subnet", Argument: "id"},
		"aws_vpc":              {Type: "aws_vpc", Argument: "id"},
		"aws_vpc_endpoint":     {Type: "aws_vpc_endpoint", Argument: "id"},
	}
}
