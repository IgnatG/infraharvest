// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// S3Lister lists the S3 buckets of the region it runs for, with their
// bucket policies (the `s3` service).
type S3Lister struct {
	Service
	// parts gathers the parts of the listing that failed.
	parts partFailures
}

// List records every bucket of the region as an aws_s3_bucket,
// imported by its name, with force_destroy "false", acl "private" and its
// policy when it has one. A bucket with a policy is preceded by an
// aws_s3_bucket_policy for it. ListBuckets is asked for the region's
// buckets only, and gives each one's region, so no bucket needs a call of
// its own; GetBucketLocation is only for an answer without regions, as an
// emulator's may be, that has every region's buckets. Without a region
// (ProfileRegion), the region is the one the SDK resolved from the profile
// or the environment. A listing error fails the service; a bucket whose
// location can't be read is left out, and one whose policy can't be read is
// listed without it, and those failures are returned together as a partial
// error (see partFailures).
func (g *S3Lister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	region := regionSetting(g.Settings())
	if region == ProfileRegion {
		region = config.Region
	}
	svc := s3.NewFromConfig(config)
	input := &s3.ListBucketsInput{}
	if region != "" {
		input.BucketRegion = aws.String(region)
	}
	pages := s3.NewListBucketsPaginator(svc, input, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return errors.Join(err, g.parts.err())
		}
		for _, bucket := range page.Buckets {
			bucketRegion := aws.ToString(bucket.BucketRegion)
			if bucketRegion == "" {
				location, err := svc.GetBucketLocation(g.Context(), &s3.GetBucketLocationInput{Bucket: bucket.Name})
				if err != nil {
					g.parts.add("location of bucket "+aws.ToString(bucket.Name), err)
					continue
				}
				bucketRegion = s3BucketRegion(string(location.LocationConstraint))
			}
			if bucketRegion != region {
				continue
			}
			g.addBucket(svc, aws.ToString(bucket.Name))
		}
	}
	return g.parts.err()
}

// addBucket records the bucket name and, first, its policy when it has one
// that can be read. A bucket without a policy answers NoSuchBucketPolicy;
// any other error, AccessDenied included, is gathered and leaves the policy
// out.
func (g *S3Lister) addBucket(svc *s3.Client, name string) {
	attributes := map[string]string{"force_destroy": "false", "acl": "private"}
	policy, err := svc.GetBucketPolicy(g.Context(), &s3.GetBucketPolicyInput{Bucket: aws.String(name)})
	switch {
	case err == nil && policy.Policy != nil:
		attributes["policy"] = *policy.Policy
		g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_s3_bucket_policy", "aws"))
	case err != nil && apiErrorCode(err) != "NoSuchBucketPolicy":
		g.parts.add("policy of bucket "+name, err)
	}
	g.Resources = append(g.Resources, harvest.Record(name, name, "aws_s3_bucket", "aws", attributes))
}

// s3BucketRegion returns the region of a bucket whose location constraint
// is constraint. Buckets in us-east-1 have no constraint, and those made
// in eu-west-1 long ago have the legacy constraint "EU".
func s3BucketRegion(constraint string) string {
	switch constraint {
	case "":
		return "us-east-1"
	case "EU":
		return "eu-west-1"
	}
	return constraint
}
