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

	"github.com/IgnatG/infraharvest/internal/harvest"
)

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

// s3Probe reports whether a bucket has a part of its configuration that
// the AWS provider manages as a separate resource, imported by bucket name.
type s3Probe func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error)

// s3Probes are the probes of the children the catalogue names for
// aws_s3_bucket, by type.
var s3Probes = map[string]s3Probe{
	"aws_s3_bucket_versioning": func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: bucket})
		return err == nil && out.Status != "", err
	},
	"aws_s3_bucket_server_side_encryption_configuration": func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: bucket})
		return err == nil && out.ServerSideEncryptionConfiguration != nil && len(out.ServerSideEncryptionConfiguration.Rules) > 0, err
	},
	"aws_s3_bucket_lifecycle_configuration": func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: bucket})
		return err == nil && len(out.Rules) > 0, err
	},
	"aws_s3_bucket_cors_configuration": func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketCors(ctx, &s3.GetBucketCorsInput{Bucket: bucket})
		return err == nil && len(out.CORSRules) > 0, err
	},
	"aws_s3_bucket_website_configuration": func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		_, err := svc.GetBucketWebsite(ctx, &s3.GetBucketWebsiteInput{Bucket: bucket})
		return err == nil, err
	},
	"aws_s3_bucket_logging": func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketLogging(ctx, &s3.GetBucketLoggingInput{Bucket: bucket})
		return err == nil && out.LoggingEnabled != nil, err
	},
	"aws_s3_bucket_public_access_block": func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: bucket})
		return err == nil && out.PublicAccessBlockConfiguration != nil, err
	},
	"aws_s3_bucket_ownership_controls": func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketOwnershipControls(ctx, &s3.GetBucketOwnershipControlsInput{Bucket: bucket})
		return err == nil && out.OwnershipControls != nil && len(out.OwnershipControls.Rules) > 0, err
	},
	"aws_s3_bucket_accelerate_configuration": func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketAccelerateConfiguration(ctx, &s3.GetBucketAccelerateConfigurationInput{Bucket: bucket})
		return err == nil && out.Status != "", err
	},
	"aws_s3_bucket_request_payment_configuration": func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketRequestPayment(ctx, &s3.GetBucketRequestPaymentInput{Bucket: bucket})
		// The bucket owner pays unless configured otherwise.
		return err == nil && out.Payer == s3types.PayerRequester, err
	},
	"aws_s3_bucket_object_lock_configuration": func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{Bucket: bucket})
		return err == nil && out.ObjectLockConfiguration != nil && out.ObjectLockConfiguration.ObjectLockEnabled == s3types.ObjectLockEnabledEnabled, err
	},
	"aws_s3_bucket_replication_configuration": func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketReplication(ctx, &s3.GetBucketReplicationInput{Bucket: bucket})
		return err == nil && out.ReplicationConfiguration != nil && len(out.ReplicationConfiguration.Rules) > 0, err
	},
}

// ChildImports lists, for an S3 bucket, the separate resources that hold
// the parts of its configuration it has: of the types the catalogue names
// as its children, which carry the settings of the arguments it omits from
// aws_s3_bucket. Other resources have none.
func (p *Provider) ChildImports(ctx context.Context, r harvest.Resource) ([]harvest.Resource, error) {
	entry, ok := p.catalogue().Entry(r.Address.Type)
	if r.Address.Type != "aws_s3_bucket" || !ok || len(entry.Children) == 0 {
		return nil, nil
	}
	service := &Service{}
	service.Configure(harvest.ListerConfig{Settings: p.serviceArgs()})
	service.SetContext(ctx)
	config, err := service.sdkConfig()
	if err != nil {
		return nil, err
	}
	svc := s3.NewFromConfig(config)
	bucket := r.Observed.ID
	var children []harvest.Resource
	var errs []error
	for _, child := range entry.Children {
		probe, ok := s3Probes[child]
		if !ok {
			errs = append(errs, fmt.Errorf("%s: no way to tell whether a bucket has one", child))
			continue
		}
		configured, err := probe(ctx, svc, &bucket)
		var apiErr smithy.APIError
		switch {
		case errors.As(err, &apiErr) && s3NotConfigured[apiErr.ErrorCode()]:
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", child, err))
		case configured:
			children = append(children, harvest.RecordBare(bucket, bucket, child, "aws"))
		}
	}
	return children, errors.Join(errs...)
}
