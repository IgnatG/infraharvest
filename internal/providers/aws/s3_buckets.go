// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

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
// error (see partFailures). The buckets of a page are read resourceParallel
// at a time, and recorded in the page's order.
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
		reads := make([]s3BucketRead, len(page.Buckets))
		inParallel(len(page.Buckets), resourceParallel, func(i int) {
			g.readBucket(svc, page.Buckets[i], region, &reads[i])
		})
		for _, read := range reads {
			g.Resources = append(g.Resources, read.resources...)
			g.parts.errs = append(g.parts.errs, read.parts.errs...)
		}
	}
	return g.parts.err()
}

// s3BucketRead is what reading one bucket found: its resources, and the
// parts of it that failed.
type s3BucketRead struct {
	resources []harvest.Resource
	parts     partFailures
}

// readBucket reads into out the bucket if it is in region, asking for its
// location only when the listing didn't give it (see addBucket).
func (g *S3Lister) readBucket(svc *s3.Client, bucket s3types.Bucket, region string, out *s3BucketRead) {
	bucketRegion := aws.ToString(bucket.BucketRegion)
	if bucketRegion == "" {
		location, err := svc.GetBucketLocation(g.Context(), &s3.GetBucketLocationInput{Bucket: bucket.Name})
		if err != nil {
			out.parts.add("location of bucket "+aws.ToString(bucket.Name), err)
			return
		}
		bucketRegion = s3BucketRegion(string(location.LocationConstraint))
	}
	if bucketRegion != region {
		return
	}
	g.addBucket(svc, aws.ToString(bucket.Name), out)
}

// addBucket records in out the bucket name and, first, its policy when it
// has one that can be read. A bucket without a policy answers
// NoSuchBucketPolicy; any other error, AccessDenied included, is gathered
// and leaves the policy out.
func (g *S3Lister) addBucket(svc *s3.Client, name string, out *s3BucketRead) {
	attributes := map[string]string{"force_destroy": "false", "acl": "private"}
	policy, err := svc.GetBucketPolicy(g.Context(), &s3.GetBucketPolicyInput{Bucket: aws.String(name)})
	switch {
	case err == nil && policy.Policy != nil:
		attributes["policy"] = *policy.Policy
		out.resources = append(out.resources, harvest.RecordBare(name, name, "aws_s3_bucket_policy", "aws"))
	case err != nil && apiErrorCode(err) != "NoSuchBucketPolicy":
		out.parts.add("policy of bucket "+name, err)
	}
	out.resources = append(out.resources, harvest.Record(name, name, "aws_s3_bucket", "aws", attributes))
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
