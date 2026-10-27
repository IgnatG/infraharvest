// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package s3 reads Terraform state from Amazon S3, where the s3 backend
// keeps it. It links the AWS SDK, so only a build with AWS includes it.
package s3

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/IgnatG/infraharvest/internal/managed"
)

// Store reads state from Amazon S3.
type Store struct {
	client *awss3.Client
}

// New opens S3 in region with the credentials of profile, or the default
// region and credential chain for those that are empty.
func New(ctx context.Context, region, profile string) (managed.ObjectStore, error) {
	var opts []func(*config.LoadOptions) error
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &Store{client: awss3.NewFromConfig(cfg)}, nil
}

// List returns the keys under prefix.
func (s *Store) List(ctx context.Context, bucket, prefix string) ([]string, error) {
	var keys []string
	pages := awss3.NewListObjectsV2Paginator(s.client, &awss3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(prefix)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
	}
	return keys, nil
}

// Get returns an object's content.
func (s *Store) Get(ctx context.Context, bucket, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return managed.ReadState(out.Body)
}
