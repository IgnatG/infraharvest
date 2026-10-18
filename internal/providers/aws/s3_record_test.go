// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"net/http"
	"strings"
	"testing"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// recordS3API passes each request to handle with the bucket it is for,
// whether S3 addresses the bucket by host or by path ("" for none).
type recordS3API struct {
	handle func(bucket string, call apiCall) string
}

func (a *recordS3API) Do(r *http.Request) (*http.Response, error) {
	bucket := strings.Trim(r.URL.Path, "/")
	host := r.URL.Host
	if host == "" {
		host = r.Host
	}
	if name, _, found := strings.Cut(host, ".s3."); found && name != "" {
		bucket = name
	}
	fake := &fakeAPI{handle: func(call apiCall) string { return a.handle(bucket, call) }}
	return fake.Do(r)
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

// useRecordS3API serves recordS3Buckets, or listError for ListBuckets when
// it is set.
func useRecordS3API(t *testing.T, listError string) {
	t.Helper()
	useFakeAPI(t, func(apiCall) string { return "" })
	testConfig.HTTPClient = &recordS3API{handle: func(bucket string, call apiCall) string {
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
	}}
}

// The lister lists the buckets in the region of its arguments, by name,
// with force_destroy false, acl private and the bucket policy when it has
// one, preceded by an aws_s3_bucket_policy for that policy. The legacy
// location constraint EU is eu-west-1. A bucket whose location it can't
// get is left out; one whose policy it can't get is listed without it.
// Both failures make the listing partial, naming the bucket.
func TestRecordS3ListsTheRegionsBuckets(t *testing.T) {
	useRecordS3API(t, "")
	g := &S3Lister{}
	g.Configure(harvest.ListerConfig{Settings: map[string]interface{}{"region": "eu-west-1"}})

	err := g.List()
	if !harvest.IsPartial(err) || !strings.Contains(err.Error(), "location of bucket locked") || !strings.Contains(err.Error(), "policy of bucket denied-policy") {
		t.Errorf("want a partial error for the location of locked and the policy of denied-policy, got %v", err)
	}

	bucketAttrs := map[string]string{"force_destroy": "false", "acl": "private"}
	assertRecorded(t, g.Resources,
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
	useRecordS3API(t, "")
	g := &S3Lister{}
	g.Configure(harvest.ListerConfig{Settings: map[string]interface{}{"region": "us-east-1"}})

	if err := g.List(); !harvest.IsPartial(err) || !strings.Contains(err.Error(), "location of bucket locked") {
		t.Errorf("want a partial error for the location of locked, got %v", err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_s3_bucket", "us-bucket", "us-bucket", map[string]string{"force_destroy": "false", "acl": "private"}},
	)
}

// ListBuckets is asked for the region's buckets, and where its answer
// gives each bucket's region, no bucket's location is read.
func TestRecordS3UsesTheListedRegions(t *testing.T) {
	useFakeAPI(t, func(apiCall) string { return "" })
	testConfig.HTTPClient = &recordS3API{handle: func(bucket string, call apiCall) string {
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
	}}
	g := &S3Lister{}
	g.Configure(harvest.ListerConfig{Settings: map[string]interface{}{"region": "eu-west-1"}})

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertIDs(t, g.Resources, "aws_s3_bucket", "logs-eu")
}

// Without a region (ProfileRegion), the lister lists the buckets of the region
// the SDK resolved.
func TestRecordS3WithoutRegion(t *testing.T) {
	useRecordS3API(t, "")
	testConfig.Region = "eu-west-1"
	g := &S3Lister{}
	g.Configure(harvest.ListerConfig{Settings: map[string]interface{}{"region": ProfileRegion}})

	if err := g.List(); !harvest.IsPartial(err) {
		t.Errorf("want a partial error, got %v", err)
	}

	assertIDs(t, g.Resources, "aws_s3_bucket", "logs-eu", "assets-eu", "legacy-eu", "denied-policy")
}

func TestRecordS3ReturnsListBucketsError(t *testing.T) {
	useRecordS3API(t, `<Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`)
	g := &S3Lister{}
	g.Configure(harvest.ListerConfig{Settings: map[string]interface{}{"region": "eu-west-1"}})

	assertRecordedError(t, g.List(), "AccessDenied")
}
