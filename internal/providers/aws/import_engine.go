// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// DefaultTags describes the provider's default_tags: tags it applies to
// every resource. Keys starting with aws: are AWS's own.
func (Provider) DefaultTags() (attribute, block, reservedPrefix string) {
	return "tags", "default_tags", "aws:"
}

// Scope names the account and region this import covers, for the output
// layout: global for global services such as IAM. It remembers the
// account, and the region the SDK resolved for ProfileRegion, for the
// provider block (see ProviderBlock).
func (p *Provider) Scope(ctx context.Context) (account, region string, err error) {
	service := &Service{}
	service.Configure(harvest.ListerConfig{Settings: p.serviceArgs()})
	service.SetContext(ctx)
	config, err := service.sdkConfig()
	if err != nil {
		return "", "", err
	}
	id, err := service.callerAccount(config)
	if err != nil {
		return "", "", fmt.Errorf("find the AWS account: %w", err)
	}
	region = p.region
	switch region {
	case GlobalPseudoRegion:
		region = "global"
	case ProfileRegion:
		region = config.Region
		p.resolvedRegion = config.Region
	}
	p.account = aws.ToString(id)
	return p.account, region, nil
}

// PartialTypes names the types a region's listing can leave out while
// they exist: an Aurora global cluster is listed in the region of its
// primary cluster only (see RDSLister.listGlobalClusters), which a
// failover moves; and with SQS_PREFIX set, the queues are listed by
// that prefix only (see SqsLister.List).
func (Provider) PartialTypes() []string {
	types := []string{"aws_rds_global_cluster"}
	if _, ok := os.LookupEnv("SQS_PREFIX"); ok {
		types = append(types, "aws_sqs_queue")
	}
	return types
}

// TerraformEnv gives Terraform the credentials infraharvest lists with, as
// resolved from --profile or the environment: an SSO session's, or a
// role's assumed with an MFA code, which Terraform couldn't resolve
// without asking. They are the base credentials: with --assume-role, the
// provider block assumes the role itself (see ProviderBlock). Each call
// returns them fresh, with at least credentialsExpiryWindow left, and the
// importer asks before each plan, so a long root never hands Terraform
// expired ones.
// Without --regions, it also gives the region the profile or environment
// resolves to, which the provider block names too once Scope has run.
func (p *Provider) TerraformEnv(ctx context.Context) (map[string]string, error) {
	service := &Service{}
	service.Configure(harvest.ListerConfig{Settings: p.serviceArgs()})
	service.SetContext(ctx)
	creds, err := service.baseCredentials()
	if err != nil {
		return nil, err
	}
	env := map[string]string{
		"AWS_ACCESS_KEY_ID":     creds.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY": creds.SecretAccessKey,
		// Empty unless temporary, so that a token from the environment
		// doesn't pair with other keys.
		"AWS_SESSION_TOKEN": creds.SessionToken,
		// The AWS provider prefers a profile to keys from the environment.
		"AWS_PROFILE":         "",
		"AWS_DEFAULT_PROFILE": "",
	}
	if p.region == ProfileRegion {
		config, err := service.sdkConfig()
		if err != nil {
			return nil, err
		}
		env["AWS_REGION"] = config.Region
	}
	return env, nil
}
