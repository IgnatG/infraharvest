// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/IgnatG/infraharvest/terraformutils"
)

// ImportID returns the ID Terraform imports r with, where it differs from
// the ID the lister records, and false for types Terraform can't import.
func (AWSProvider) ImportID(r terraformutils.Resource) (string, bool) {
	switch r.InstanceInfo.Type {
	case "aws_main_route_table_association":
		// No import support; the VPC's main_route_table_id records it.
		return "", false
	case "aws_route_table_association":
		attrs := r.InstanceState.Attributes
		return attrs["subnet_id"] + "/" + attrs["route_table_id"], true
	case "aws_ecs_cluster":
		// The lister records the ARN; Terraform imports by name and builds
		// the ARN itself.
		id := r.InstanceState.ID
		return id[strings.LastIndex(id, "/")+1:], true
	case "aws_ecs_service":
		attrs := r.InstanceState.Attributes
		return attrs["cluster"] + "/" + attrs["name"], true
	case "aws_lambda_permission":
		attrs := r.InstanceState.Attributes
		return attrs["function_name"] + "/" + attrs["statement_id"], true
	case "aws_volume_attachment":
		attrs := r.InstanceState.Attributes
		return attrs["device_name"] + ":" + attrs["volume_id"] + ":" + attrs["instance_id"], true
	}
	return r.InstanceState.ID, true
}

// OmittedArguments leaves out of generated configuration the deprecated
// aws_s3_bucket arguments (see s3BucketArguments); aws_iam_role's
// inline_policy and managed_policy_arns, deprecated in favour of the
// aws_iam_role_policy and aws_iam_role_policy_attachment resources the
// IAM lister imports with each role; and from every resource: region,
// which defaults to the provider's, the region the resources were listed
// in, and tags_all, which the provider computes from tags and
// default_tags.
func (AWSProvider) OmittedArguments() map[string][]string {
	return map[string][]string{
		"*":             {"region", "tags_all"},
		"aws_iam_role":  {"inline_policy", "managed_policy_arns"},
		"aws_s3_bucket": s3BucketArguments,
	}
}

// DefaultTags describes the provider's default_tags: tags it applies to
// every resource. Keys starting with aws: are AWS's own.
func (AWSProvider) DefaultTags() (attribute, block, reservedPrefix string) {
	return "tags", "default_tags", "aws:"
}

// StateOnlyArguments are arguments the AWS provider keeps only in state:
// settings it uses when it deletes or updates a resource, and settings that
// don't apply to every kind of a resource. Import leaves them unset; the
// provider's own import tests ignore them too.
func (AWSProvider) StateOnlyArguments() map[string][]string {
	return map[string][]string{
		"aws_ecs_service":           {"wait_for_steady_state"},
		"aws_iam_role":              {"force_detach_policies"},
		"aws_lb_target_group":       {"lambda_multi_value_headers_enabled", "proxy_protocol_v2"},
		"aws_secretsmanager_secret": {"force_overwrite_replica_secret", "recovery_window_in_days"},
	}
}

// Scope names the account and region this import covers, for the output
// layout: global for global services such as IAM.
func (p *AWSProvider) Scope(ctx context.Context) (account, region string, err error) {
	service := &AWSService{}
	service.SetArgs(p.serviceArgs())
	service.SetContext(ctx)
	config, err := service.generateConfig()
	if err != nil {
		return "", "", err
	}
	id, err := service.getAccountNumber(config)
	if err != nil {
		return "", "", fmt.Errorf("find the AWS account: %w", err)
	}
	region = p.region
	switch region {
	case GlobalRegion:
		region = "global"
	case NoRegion:
		region = config.Region
	}
	return aws.ToString(id), region, nil
}
