// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"log"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// S3Lister lists the S3 buckets of the region it runs for, with their
// bucket policies (the `s3` service).
type S3Lister struct {
	Service
}

// List records every bucket of the region as an aws_s3_bucket,
// imported by its name, with force_destroy "false", acl "private" and its
// policy when it has one. A bucket with a policy is preceded by an
// aws_s3_bucket_policy for it. ListBuckets answers with the buckets of
// every region, so each bucket's location decides whether it belongs here;
// without a region (ProfileRegion), the region is the one the SDK resolved from
// the profile or the environment. A listing error fails the service; a
// bucket whose location can't be read is logged and left out, and one
// whose policy can't be read is logged and listed without it.
func (g *S3Lister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	region, _ := g.Settings()["region"].(string)
	if region == ProfileRegion {
		region = config.Region
	}
	svc := s3.NewFromConfig(config)
	pages := s3.NewListBucketsPaginator(svc, &s3.ListBucketsInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, bucket := range page.Buckets {
			location, err := svc.GetBucketLocation(g.Context(), &s3.GetBucketLocationInput{Bucket: bucket.Name})
			if err != nil {
				log.Printf("aws: s3: can't read the location of bucket %s; leaving it out: %v", aws.ToString(bucket.Name), err)
				continue
			}
			if s3BucketRegion(string(location.LocationConstraint)) != region {
				continue
			}
			g.addBucket(svc, aws.ToString(bucket.Name))
		}
	}
	return nil
}

// addBucket records the bucket name and, first, its policy when it has one
// that can be read. A bucket without a policy answers NoSuchBucketPolicy;
// any other error, AccessDenied included, is logged and leaves the policy
// out.
func (g *S3Lister) addBucket(svc *s3.Client, name string) {
	attributes := map[string]string{"force_destroy": "false", "acl": "private"}
	policy, err := svc.GetBucketPolicy(g.Context(), &s3.GetBucketPolicyInput{Bucket: aws.String(name)})
	switch {
	case err == nil && policy.Policy != nil:
		attributes["policy"] = *policy.Policy
		g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_s3_bucket_policy", "aws"))
	case err != nil && apiErrorCode(err) != "NoSuchBucketPolicy":
		log.Printf("aws: s3: can't read the policy of bucket %s; listing it without one: %v", name, err)
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
