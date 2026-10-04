// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/IgnatG/infraharvest/terraformutils"
)

// s3BucketArguments are the aws_s3_bucket arguments that the AWS provider
// replaced with separate resources, and marks as deprecated. They are
// optional and computed, so configuration without them plans no change;
// the separate resources s3Children lists carry their settings.
var s3BucketArguments = []string{
	"acceleration_status", "acl", "cors_rule", "grant", "lifecycle_rule", "logging",
	"object_lock_configuration", "policy", "replication_configuration", "request_payer",
	"server_side_encryption_configuration", "versioning", "website",
}

// s3NotConfigured are the error codes S3 answers with when a bucket has no
// configuration of the kind asked for. NotImplemented means the endpoint
// doesn't support the feature, so there is nothing to import either.
var s3NotConfigured = map[string]bool{
	"NoSuchLifecycleConfiguration":                   true,
	"NoSuchCORSConfiguration":                        true,
	"NoSuchWebsiteConfiguration":                     true,
	"ServerSideEncryptionConfigurationNotFoundError": true,
	"NoSuchPublicAccessBlockConfiguration":           true,
	"OwnershipControlsNotFoundError":                 true,
	"ObjectLockConfigurationNotFoundError":           true,
	"ReplicationConfigurationNotFoundError":          true,
	"NotImplemented":                                 true,
}

// s3Child is a part of a bucket's configuration that the AWS provider
// manages as a separate resource, imported by bucket name.
type s3Child struct {
	resourceType string
	// configured reports whether the bucket has this configuration.
	configured func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error)
}

var s3Children = []s3Child{
	{"aws_s3_bucket_versioning", func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: bucket})
		return err == nil && out.Status != "", err
	}},
	{"aws_s3_bucket_server_side_encryption_configuration", func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: bucket})
		return err == nil && out.ServerSideEncryptionConfiguration != nil && len(out.ServerSideEncryptionConfiguration.Rules) > 0, err
	}},
	{"aws_s3_bucket_lifecycle_configuration", func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: bucket})
		return err == nil && len(out.Rules) > 0, err
	}},
	{"aws_s3_bucket_cors_configuration", func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketCors(ctx, &s3.GetBucketCorsInput{Bucket: bucket})
		return err == nil && len(out.CORSRules) > 0, err
	}},
	{"aws_s3_bucket_website_configuration", func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		_, err := svc.GetBucketWebsite(ctx, &s3.GetBucketWebsiteInput{Bucket: bucket})
		return err == nil, err
	}},
	{"aws_s3_bucket_logging", func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketLogging(ctx, &s3.GetBucketLoggingInput{Bucket: bucket})
		return err == nil && out.LoggingEnabled != nil, err
	}},
	{"aws_s3_bucket_public_access_block", func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: bucket})
		return err == nil && out.PublicAccessBlockConfiguration != nil, err
	}},
	{"aws_s3_bucket_ownership_controls", func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketOwnershipControls(ctx, &s3.GetBucketOwnershipControlsInput{Bucket: bucket})
		return err == nil && out.OwnershipControls != nil && len(out.OwnershipControls.Rules) > 0, err
	}},
	{"aws_s3_bucket_accelerate_configuration", func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketAccelerateConfiguration(ctx, &s3.GetBucketAccelerateConfigurationInput{Bucket: bucket})
		return err == nil && out.Status != "", err
	}},
	{"aws_s3_bucket_request_payment_configuration", func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketRequestPayment(ctx, &s3.GetBucketRequestPaymentInput{Bucket: bucket})
		// The bucket owner pays unless configured otherwise.
		return err == nil && out.Payer == s3types.PayerRequester, err
	}},
	{"aws_s3_bucket_object_lock_configuration", func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{Bucket: bucket})
		return err == nil && out.ObjectLockConfiguration != nil && out.ObjectLockConfiguration.ObjectLockEnabled == s3types.ObjectLockEnabledEnabled, err
	}},
	{"aws_s3_bucket_replication_configuration", func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketReplication(ctx, &s3.GetBucketReplicationInput{Bucket: bucket})
		return err == nil && out.ReplicationConfiguration != nil && len(out.ReplicationConfiguration.Rules) > 0, err
	}},
}

// ChildImports lists, for an S3 bucket, the separate resources that hold
// the parts of its configuration it has (see s3BucketArguments). Other
// resources have none.
func (p *AWSProvider) ChildImports(ctx context.Context, r terraformutils.Resource) ([]terraformutils.Resource, error) {
	if r.InstanceInfo.Type != "aws_s3_bucket" {
		return nil, nil
	}
	service := &AWSService{}
	service.SetArgs(p.serviceArgs())
	service.SetContext(ctx)
	config, err := service.generateConfig()
	if err != nil {
		return nil, err
	}
	svc := s3.NewFromConfig(config)
	bucket := r.InstanceState.ID
	var children []terraformutils.Resource
	var errs []error
	for _, child := range s3Children {
		configured, err := child.configured(ctx, svc, &bucket)
		var apiErr smithy.APIError
		switch {
		case errors.As(err, &apiErr) && s3NotConfigured[apiErr.ErrorCode()]:
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", child.resourceType, err))
		case configured:
			children = append(children, terraformutils.NewSimpleResource(bucket, bucket, child.resourceType, "aws", S3AllowEmptyValues))
		}
	}
	return children, errors.Join(errs...)
}
