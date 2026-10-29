// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// s3Bucket is the bucket call is for, whether S3 addresses it by host or
// by path ("" for none).
func s3Bucket(call apiCall) string {
	if name, _, found := strings.Cut(call.Host, ".s3."); found && name != "" {
		return name
	}
	return strings.Trim(call.Path, "/")
}

const recordS3NS = `xmlns="http://s3.amazonaws.com/doc/2006-03-01/"`

const recordS3Policy = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:*","Resource":"arn:aws:s3:::logs-eu/*","Condition":{"Bool":{"aws:SecureTransport":"false"}}}]}`

// recordS3Buckets are the buckets of the S3 tests, in listing order, with
// their location constraint ("" for us-east-1) and policy answers.
var recordS3Buckets = []struct{ name, location, policy string }{
	{"logs-eu", `<LocationConstraint ` + recordS3NS + `>eu-west-1</LocationConstraint>`, recordS3Policy},
	{"assets-eu", `<LocationConstraint ` + recordS3NS + `>eu-west-1</LocationConstraint>`, `<Error><Code>NoSuchBucketPolicy</Code><Message>The bucket policy does not exist</Message></Error>`},
	{"legacy-eu", `<LocationConstraint ` + recordS3NS + `>EU</LocationConstraint>`, `<Error><Code>NoSuchBucketPolicy</Code></Error>`},
	{"us-bucket", `<LocationConstraint ` + recordS3NS + `/>`, `<Error><Code>NoSuchBucketPolicy</Code></Error>`},
	{"locked", `<Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`, `<Error><Code>AccessDenied</Code></Error>`},
	{"denied-policy", `<LocationConstraint ` + recordS3NS + `>eu-west-1</LocationConstraint>`, `<Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`},
}

// useS3API returns a context whose listers send their S3 requests to
// handle, in region (see useFakeAPI).
func useS3API(t *testing.T, region string, handle func(bucket string, call apiCall) string) context.Context {
	t.Helper()
	cfg := fakeConfig(func(call apiCall) string { return handle(s3Bucket(call), call) })
	cfg.Region = region
	return fakeRun(t, cfg)
}

// useRecordS3API returns a context whose listers are served
// recordS3Buckets, or listError for ListBuckets when it is set, in region.
func useRecordS3API(t *testing.T, region, listError string) context.Context {
	t.Helper()
	return useS3API(t, region, func(bucket string, call apiCall) string {
		if bucket == "" {
			if listError != "" {
				return listError
			}
			var b strings.Builder
			b.WriteString(`<ListAllMyBucketsResult ` + recordS3NS + `><Owner><ID>owner</ID></Owner><Buckets>`)
			for _, listed := range recordS3Buckets {
				b.WriteString(`<Bucket><Name>` + listed.name + `</Name><CreationDate>2026-01-02T03:04:05.000Z</CreationDate></Bucket>`)
			}
			b.WriteString(`</Buckets></ListAllMyBucketsResult>`)
			return b.String()
		}
		for _, b := range recordS3Buckets {
			if b.name != bucket {
				continue
			}
			switch {
			case call.Query.Has("location"):
				return b.location
			case call.Query.Has("policy"):
				return b.policy
			}
		}
		return recordUnexpected(t, call)
	})
}

// The lister lists the buckets in the region of its arguments, by name,
// with force_destroy false, acl private and the bucket policy when it has
// one, preceded by an aws_s3_bucket_policy for that policy. The legacy
// location constraint EU is eu-west-1. A bucket whose location it can't
// get is left out; one whose policy it can't get is listed without it.
// Both failures make the listing partial, naming the bucket.
func TestRecordS3ListsTheRegionsBuckets(t *testing.T) {
	t.Parallel()
	ctx := useRecordS3API(t, "us-east-1", "")
	g := &S3Lister{}
	g.SetSettings(serviceSettings{region: "eu-west-1"})

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if !harvest.IsPartial(err) || !strings.Contains(err.Error(), "location of bucket locked") || !strings.Contains(err.Error(), "policy of bucket denied-policy") {
		t.Errorf("want a partial error for the location of locked and the policy of denied-policy, got %v", err)
	}

	bucketAttrs := map[string]string{"force_destroy": "false", "acl": "private"}
	assertRecorded(t, resources,
		recordedResource{"aws_s3_bucket_policy", "logs-eu", "logs-eu", nil},
		recordedResource{"aws_s3_bucket", "logs-eu", "logs-eu", map[string]string{
			"force_destroy": "false", "acl": "private", "policy": recordS3Policy}},
		recordedResource{"aws_s3_bucket", "assets-eu", "assets-eu", bucketAttrs},
		recordedResource{"aws_s3_bucket", "legacy-eu", "legacy-eu", bucketAttrs},
		recordedResource{"aws_s3_bucket", "denied-policy", "denied-policy", bucketAttrs},
	)
}

// A bucket without a location constraint is in us-east-1. The bucket whose
// location can't be read might be in any region, so it makes every
// region's listing partial.
func TestRecordS3ListsUSEast1Buckets(t *testing.T) {
	t.Parallel()
	ctx := useRecordS3API(t, "us-east-1", "")
	g := &S3Lister{}
	g.SetSettings(serviceSettings{region: "us-east-1"})

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if !harvest.IsPartial(err) || !strings.Contains(err.Error(), "location of bucket locked") {
		t.Errorf("want a partial error for the location of locked, got %v", err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_s3_bucket", "us-bucket", "us-bucket", map[string]string{"force_destroy": "false", "acl": "private"}},
	)
}

// ListBuckets is asked for the region's buckets, and where its answer
// gives each bucket's region, no bucket's location is read.
func TestRecordS3UsesTheListedRegions(t *testing.T) {
	t.Parallel()
	ctx := useS3API(t, "us-east-1", func(bucket string, call apiCall) string {
		if bucket != "" {
			if call.Query.Has("policy") {
				return `<Error><Code>NoSuchBucketPolicy</Code></Error>`
			}
			return recordUnexpected(t, call)
		}
		if got := call.Query.Get("bucket-region"); got != "eu-west-1" {
			t.Errorf("ListBuckets bucket-region %q, want eu-west-1", got)
		}
		return `<ListAllMyBucketsResult ` + recordS3NS + `><Buckets>` +
			`<Bucket><Name>logs-eu</Name><BucketRegion>eu-west-1</BucketRegion></Bucket>` +
			`<Bucket><Name>other</Name><BucketRegion>us-east-1</BucketRegion></Bucket>` +
			`</Buckets></ListAllMyBucketsResult>`
	})
	g := &S3Lister{}
	g.SetSettings(serviceSettings{region: "eu-west-1"})

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertIDs(t, resources, "aws_s3_bucket", "logs-eu")
}

// Without a region (ProfileRegion), the lister lists the buckets of the region
// the SDK resolved.
func TestRecordS3WithoutRegion(t *testing.T) {
	t.Parallel()
	ctx := useRecordS3API(t, "eu-west-1", "")
	g := &S3Lister{}
	g.SetSettings(serviceSettings{region: ProfileRegion})

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if !harvest.IsPartial(err) {
		t.Errorf("want a partial error, got %v", err)
	}

	assertIDs(t, resources, "aws_s3_bucket", "logs-eu", "assets-eu", "legacy-eu", "denied-policy")
}

func TestRecordS3ReturnsListBucketsError(t *testing.T) {
	t.Parallel()
	ctx := useRecordS3API(t, "us-east-1", `<Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`)
	g := &S3Lister{}
	g.SetSettings(serviceSettings{region: "eu-west-1"})

	assertRecordedError(t, listError(ctx, g), "AccessDenied")
}

// The buckets' locations and policies are read several buckets at once,
// and recorded in the listed order.
func TestRecordS3ReadsBucketsInParallel(t *testing.T) {
	t.Parallel()
	locations, policies := newOverlap(3), newOverlap(3)
	ctx := useS3API(t, "us-east-1", func(bucket string, call apiCall) string {
		switch {
		case bucket == "":
			return `<ListAllMyBucketsResult ` + recordS3NS + `><Buckets>` +
				`<Bucket><Name>a</Name></Bucket><Bucket><Name>b</Name></Bucket><Bucket><Name>c</Name></Bucket>` +
				`</Buckets></ListAllMyBucketsResult>`
		case call.Query.Has("location"):
			locations.wait()
			return `<LocationConstraint ` + recordS3NS + `>eu-west-1</LocationConstraint>`
		case call.Query.Has("policy"):
			policies.wait()
			if bucket == "b" {
				return recordS3Policy
			}
			return `<Error><Code>NoSuchBucketPolicy</Code></Error>`
		}
		return recordUnexpected(t, call)
	})
	g := &S3Lister{}
	g.SetSettings(serviceSettings{region: "eu-west-1"})

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	locations.check(t)
	policies.check(t)
	bucketAttrs := map[string]string{"force_destroy": "false", "acl": "private"}
	assertRecorded(t, resources,
		recordedResource{"aws_s3_bucket", "a", "a", bucketAttrs},
		recordedResource{"aws_s3_bucket_policy", "b", "b", nil},
		recordedResource{"aws_s3_bucket", "b", "b", map[string]string{"force_destroy": "false", "acl": "private", "policy": recordS3Policy}},
		recordedResource{"aws_s3_bucket", "c", "c", bucketAttrs},
	)
}
