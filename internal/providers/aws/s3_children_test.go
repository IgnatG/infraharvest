// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// s3Answers are fake S3 answers by query parameter (?versioning, ...):
// configured, not configured, or failing.
var s3Answers = map[string]string{
	"versioning":          `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`,
	"encryption":          `<ServerSideEncryptionConfiguration><Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>AES256</SSEAlgorithm></ApplyServerSideEncryptionByDefault></Rule></ServerSideEncryptionConfiguration>`,
	"lifecycle":           `<Error><Code>NoSuchLifecycleConfiguration</Code></Error>`,
	"cors":                `<Error><Code>NoSuchCORSConfiguration</Code></Error>`,
	"website":             `<Error><Code>NoSuchWebsiteConfiguration</Code></Error>`,
	"logging":             `<BucketLoggingStatus></BucketLoggingStatus>`,
	"publicAccessBlock":   `<PublicAccessBlockConfiguration><BlockPublicAcls>true</BlockPublicAcls></PublicAccessBlockConfiguration>`,
	"ownershipControls":   `<OwnershipControls><Rule><ObjectOwnership>BucketOwnerEnforced</ObjectOwnership></Rule></OwnershipControls>`,
	"accelerate":          `<AccelerateConfiguration></AccelerateConfiguration>`,
	"requestPayment":      `<RequestPaymentConfiguration><Payer>BucketOwner</Payer></RequestPaymentConfiguration>`,
	"object-lock":         `<Error><Code>NotImplemented</Code></Error>`,
	"replication":         `<Error><Code>ReplicationConfigurationNotFoundError</Code></Error>`,
	"notification":        `<NotificationConfiguration></NotificationConfiguration>`,
	"inventory":           `<ListInventoryConfigurationsResult><InventoryConfiguration><Id>daily</Id></InventoryConfiguration><InventoryConfiguration><Id>weekly</Id></InventoryConfiguration><IsTruncated>false</IsTruncated></ListInventoryConfigurationsResult>`,
	"metrics":             `<ListMetricsConfigurationsResult><MetricsConfiguration><Id>EntireBucket</Id></MetricsConfiguration><IsTruncated>false</IsTruncated></ListMetricsConfigurationsResult>`,
	"intelligent-tiering": `<ListBucketIntelligentTieringConfigurationsOutput><IsTruncated>false</IsTruncated></ListBucketIntelligentTieringConfigurationsOutput>`,
}

func s3Answer(call apiCall) string {
	for param, answer := range s3Answers {
		if call.Query.Has(param) {
			return answer
		}
	}
	return `<Error><Code>Unexpected</Code></Error>`
}

func TestChildImports(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, s3Answer)
	p := &Provider{region: "us-east-1"}
	bucket := harvest.RecordBare("artifacts", "artifacts", "aws_s3_bucket", "aws")

	children, err := p.ChildImports(ctx, bucket)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, c := range children {
		if _, listed := s3ListProbes[c.Address.Type]; !listed && c.Observed.ID != "artifacts" {
			t.Errorf("%s imports %q, want the bucket name", c.Address.Type, c.Observed.ID)
		}
		got = append(got, c.Address.Type+" "+c.Observed.ID)
	}
	want := []string{
		"aws_s3_bucket_versioning artifacts", "aws_s3_bucket_server_side_encryption_configuration artifacts",
		"aws_s3_bucket_public_access_block artifacts", "aws_s3_bucket_ownership_controls artifacts",
		"aws_s3_bucket_inventory artifacts:daily", "aws_s3_bucket_inventory artifacts:weekly", "aws_s3_bucket_metric artifacts:EntireBucket",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestChildImportsReportsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Query.Has("versioning") {
			return `<Error><Code>AccessDenied</Code></Error>`
		}
		return s3Answer(call)
	})
	p := &Provider{region: "us-east-1"}

	children, err := p.ChildImports(ctx, harvest.RecordBare("artifacts", "artifacts", "aws_s3_bucket", "aws"))

	if err == nil || !strings.Contains(err.Error(), "aws_s3_bucket_versioning") {
		t.Errorf("want the versioning error, got %v", err)
	}
	if len(children) != 6 {
		t.Errorf("want the other children still listed, got %d", len(children))
	}
}

// The buckets of a configuration share one S3 client, and no more than
// s3ProbeParallel probes of a bucket run at once.
func TestChildImportsShareAClientAndBoundTheProbes(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	running, most := 0, 0
	ctx := useFakeAPI(t, func(call apiCall) string {
		mu.Lock()
		running++
		most = max(most, running)
		mu.Unlock()
		defer func() {
			mu.Lock()
			running--
			mu.Unlock()
		}()
		time.Sleep(5 * time.Millisecond)
		return s3Answer(call)
	})
	p := &Provider{region: "us-east-1"}
	for _, name := range []string{"a", "b"} {
		if _, err := p.ChildImports(ctx, harvest.RecordBare(name, name, "aws_s3_bucket", "aws")); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	if most > s3ProbeParallel {
		t.Errorf("%d probes ran at once, want at most %d", most, s3ProbeParallel)
	}
	mu.Unlock()
	run := p.service(ctx).configs()
	run.mu.Lock()
	defer run.mu.Unlock()
	if len(run.s3Children) != 1 {
		t.Errorf("%d S3 clients for one configuration, want 1", len(run.s3Children))
	}
}

func TestChildImportsOnlyForBuckets(t *testing.T) {
	t.Parallel()
	p := &Provider{region: "us-east-1"}
	children, err := p.ChildImports(t.Context(), harvest.RecordBare("q", "q", "aws_sqs_queue", "aws"))
	if err != nil || children != nil {
		t.Errorf("got %v, %v; want none", children, err)
	}
}

// Every child the catalogue names for a bucket has a probe, and every
// probe is for one of them; the bucket leaves out the arguments the
// children carry.
func TestS3ProbesAreTheCataloguedChildren(t *testing.T) {
	t.Parallel()
	entry, ok := awsCatalogue().Entry("aws_s3_bucket")
	if !ok {
		t.Fatal("aws_s3_bucket isn't in the catalogue")
	}
	for _, child := range entry.Children {
		_, isProbe := s3Probes[child]
		_, isList := s3ListProbes[child]
		if !isProbe && !isList {
			t.Errorf("%s has no probe", child)
		}
	}
	for child := range s3ListProbes {
		if !slices.Contains(entry.Children, child) {
			t.Errorf("%s isn't a catalogued child of aws_s3_bucket", child)
		}
	}
	for child := range s3Probes {
		if !slices.Contains(entry.Children, child) {
			t.Errorf("%s isn't a catalogued child of aws_s3_bucket", child)
		}
	}
	for _, name := range []string{"versioning", "server_side_encryption_configuration", "policy", "acl"} {
		if !slices.Contains(entry.Omit, name) {
			t.Errorf("%s not omitted from aws_s3_bucket", name)
		}
	}
}

// A bucket with more configurations of a kind than one answer holds has
// them all listed, and one with a notification configuration, or deleted
// since it was listed, is told so.
func TestChildImportsListEveryPageAndHandleGoneBuckets(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch {
		case call.Query.Has("metrics") && call.Query.Get("continuation-token") == "":
			return `<ListMetricsConfigurationsResult><MetricsConfiguration><Id>a</Id></MetricsConfiguration><IsTruncated>true</IsTruncated><NextContinuationToken>page2</NextContinuationToken></ListMetricsConfigurationsResult>`
		case call.Query.Has("metrics"):
			return `<ListMetricsConfigurationsResult><MetricsConfiguration><Id>b</Id></MetricsConfiguration><IsTruncated>false</IsTruncated></ListMetricsConfigurationsResult>`
		case call.Query.Has("notification"):
			return `<NotificationConfiguration><EventBridgeConfiguration></EventBridgeConfiguration></NotificationConfiguration>`
		case call.Query.Has("inventory"):
			return `<Error><Code>NoSuchBucket</Code></Error>`
		}
		return s3Answer(call)
	})
	p := &Provider{region: "us-east-1"}

	children, err := p.ChildImports(ctx, harvest.RecordBare("artifacts", "artifacts", "aws_s3_bucket", "aws"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range children {
		if c.Address.Type == "aws_s3_bucket_metric" || c.Address.Type == "aws_s3_bucket_notification" || c.Address.Type == "aws_s3_bucket_inventory" {
			got = append(got, c.Address.Type+" "+c.Observed.ID)
		}
	}
	want := []string{"aws_s3_bucket_notification artifacts", "aws_s3_bucket_metric artifacts:a", "aws_s3_bucket_metric artifacts:b"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
