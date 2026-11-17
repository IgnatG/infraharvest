// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/providers/workers"
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
	// A bucket deleted since it was listed has nothing left to import.
	"NoSuchBucket": true,
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
	"aws_s3_bucket_notification": func(ctx context.Context, svc *s3.Client, bucket *string) (bool, error) {
		out, err := svc.GetBucketNotificationConfiguration(ctx, &s3.GetBucketNotificationConfigurationInput{Bucket: bucket})
		return err == nil && (out.EventBridgeConfiguration != nil || len(out.LambdaFunctionConfigurations) > 0 ||
			len(out.QueueConfigurations) > 0 || len(out.TopicConfigurations) > 0), err
	},
}

// s3ListProbe lists the names of the configurations of one kind a bucket
// has, of which a bucket may have many: each is a resource of its own,
// imported as "bucket:name".
type s3ListProbe func(ctx context.Context, svc *s3.Client, bucket *string) ([]string, error)

// s3ListProbes are the probes of the children the catalogue names for
// aws_s3_bucket that a bucket has any number of, by type. Each is listed
// by the continuation token S3 answers truncated listings with.
var s3ListProbes = map[string]s3ListProbe{
	"aws_s3_bucket_inventory": func(ctx context.Context, svc *s3.Client, bucket *string) ([]string, error) {
		var names []string
		in := &s3.ListBucketInventoryConfigurationsInput{Bucket: bucket}
		for {
			out, err := svc.ListBucketInventoryConfigurations(ctx, in)
			if err != nil {
				return nil, err
			}
			for _, c := range out.InventoryConfigurationList {
				names = append(names, aws.ToString(c.Id))
			}
			if !aws.ToBool(out.IsTruncated) || aws.ToString(out.NextContinuationToken) == "" {
				return names, nil
			}
			in.ContinuationToken = out.NextContinuationToken
		}
	},
	"aws_s3_bucket_metric": func(ctx context.Context, svc *s3.Client, bucket *string) ([]string, error) {
		var names []string
		in := &s3.ListBucketMetricsConfigurationsInput{Bucket: bucket}
		for {
			out, err := svc.ListBucketMetricsConfigurations(ctx, in)
			if err != nil {
				return nil, err
			}
			for _, c := range out.MetricsConfigurationList {
				names = append(names, aws.ToString(c.Id))
			}
			if !aws.ToBool(out.IsTruncated) || aws.ToString(out.NextContinuationToken) == "" {
				return names, nil
			}
			in.ContinuationToken = out.NextContinuationToken
		}
	},
	"aws_s3_bucket_intelligent_tiering_configuration": func(ctx context.Context, svc *s3.Client, bucket *string) ([]string, error) {
		var names []string
		in := &s3.ListBucketIntelligentTieringConfigurationsInput{Bucket: bucket}
		for {
			out, err := svc.ListBucketIntelligentTieringConfigurations(ctx, in)
			if err != nil {
				return nil, err
			}
			for _, c := range out.IntelligentTieringConfigurationList {
				names = append(names, aws.ToString(c.Id))
			}
			if !aws.ToBool(out.IsTruncated) || aws.ToString(out.NextContinuationToken) == "" {
				return names, nil
			}
			in.ContinuationToken = out.NextContinuationToken
		}
	},
}

// s3ProbeParallel is how many probes of one bucket run at once.
const s3ProbeParallel = 4

// s3ChildrenClient returns the S3 client ChildImports asks with for
// service's configuration: one per configuration of the run, kept with it
// (see runConfigs), rather than one per bucket, so that the client's
// adaptive retry, which slows down once S3 throttles, remembers it from
// one bucket to the next.
func s3ChildrenClient(service *Service) (*s3.Client, error) {
	config, err := service.sdkConfig()
	if err != nil {
		return nil, err
	}
	run, key := service.configs(), service.cacheKey()
	run.mu.Lock()
	defer run.mu.Unlock()
	if c, ok := run.s3Children[key]; ok {
		return c, nil
	}
	c := s3.NewFromConfig(config)
	run.s3Children[key] = c
	return c, nil
}

// ChildImports lists, for an S3 bucket, the separate resources that hold
// the parts of its configuration it has: of the types the catalogue names
// as its children, which carry the settings of the arguments it omits from
// aws_s3_bucket. Other resources have none.
func (p *Provider) ChildImports(ctx context.Context, r harvest.Resource) ([]harvest.Resource, error) {
	entry, ok := p.catalogue().View(r.Address.Type)
	if r.Address.Type != "aws_s3_bucket" || !ok || len(entry.Children) == 0 {
		return nil, nil
	}
	svc, err := s3ChildrenClient(p.service(ctx))
	if err != nil {
		return nil, err
	}
	bucket := r.Observed.ID
	// The probes ask about one bucket each, so they run at once, a few at
	// a time, as the importer asks about several buckets at once too, and
	// their answers are read in the catalogue's order, which the output
	// keeps.
	configured := make([]bool, len(entry.Children))
	named := make([][]string, len(entry.Children))
	probeErrs := make([]error, len(entry.Children))
	wg := workers.New(s3ProbeParallel)
	for i, child := range entry.Children {
		probe, isProbe := s3Probes[child]
		list, isList := s3ListProbes[child]
		switch {
		case isProbe:
			wg.Go(func() {
				name := bucket
				configured[i], probeErrs[i] = probe(ctx, svc, &name)
			})
		case isList:
			wg.Go(func() {
				name := bucket
				named[i], probeErrs[i] = list(ctx, svc, &name)
			})
		default:
			probeErrs[i] = errors.New("no way to tell whether a bucket has one")
		}
	}
	wg.Wait()
	var children []harvest.Resource
	var errs []error
	for i, child := range entry.Children {
		var apiErr smithy.APIError
		switch err := probeErrs[i]; {
		case errors.As(err, &apiErr) && s3NotConfigured[apiErr.ErrorCode()]:
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", child, err))
		case configured[i]:
			children = append(children, harvest.RecordBare(bucket, bucket, child, "aws"))
		default:
			for _, name := range named[i] {
				children = append(children, harvest.RecordBare(bucket+":"+name, bucket+"_"+name, child, "aws"))
			}
		}
	}
	return children, errors.Join(errs...)
}
