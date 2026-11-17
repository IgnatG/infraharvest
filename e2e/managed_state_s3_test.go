// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// TestAWSManagedStateS3 checks --managed-state with an s3:// URL, which
// infraharvest reads (as the S3 backend keeps it) but never writes: the
// test creates a bucket in the emulator and puts the state of one of two
// queues in it, as the s3 backend would. Discover must find the object in
// the bucket and leave that queue out as already managed, and the other in.
// Then the state of the other queue, put in the bucket as a workspace's
// (env:/<workspace>/<key>), must be read by a second run, which leaves
// both out.
func TestAWSManagedStateS3(t *testing.T) {
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)
	// The S3 client addresses a bucket as <bucket>.<host> for a host that
	// isn't an IP address, which "localhost" may not resolve on every host: reach the
	// emulator by its address, for S3 alone.
	if u, err := url.Parse(endpoint); err == nil && u.Hostname() == "localhost" {
		u.Host = "127.0.0.1:" + u.Port()
		t.Setenv("AWS_ENDPOINT_URL_S3", u.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"))
	if err != nil {
		t.Fatal(err)
	}
	queues := sqs.NewFromConfig(cfg)
	managedURL := createQueue(ctx, t, queues, "e2e-managed-s3-first")
	workspaceURL := createQueue(ctx, t, queues, "e2e-managed-s3-second")
	unmanagedURL := createQueue(ctx, t, queues, "e2e-managed-s3-unmanaged")

	const bucket = "e2e-managed-state"
	store := s3.NewFromConfig(cfg)
	if _, err := store.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: awssdk.String(bucket)}); err != nil {
		t.Fatalf("create bucket %s: %v", bucket, err)
	}
	t.Cleanup(func() {
		// Not the test's ctx, which the cleanup may outlive.
		ctx := context.Background()
		listed, err := store.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: awssdk.String(bucket)})
		if err == nil {
			for _, o := range listed.Contents {
				_, _ = store.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: awssdk.String(bucket), Key: o.Key})
			}
		}
		_, _ = store.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: awssdk.String(bucket)})
	})

	put := func(key, queueURL string) {
		t.Helper()
		if _, err := store.PutObject(ctx, &s3.PutObjectInput{Bucket: awssdk.String(bucket), Key: awssdk.String(key), Body: bytes.NewReader(queueState(t, queueURL))}); err != nil {
			t.Fatalf("put s3://%s/%s: %v", bucket, key, err)
		}
		if _, err := store.HeadObject(ctx, &s3.HeadObjectInput{Bucket: awssdk.String(bucket), Key: awssdk.String(key)}); err != nil {
			t.Fatalf("the state isn't in the bucket at %s: %v", key, err)
		}
	}
	source := "s3://" + bucket + "/prod?region=us-east-1"

	put("prod/terraform.tfstate", managedURL)
	first := discoverQueues(ctx, t, source)
	assertManaged(t, first, managedURL, true)
	assertManaged(t, first, workspaceURL, false)
	assertManaged(t, first, unmanagedURL, false)

	put("env:/dev/prod/terraform.tfstate", workspaceURL)
	second := discoverQueues(ctx, t, source)
	assertManaged(t, second, managedURL, true)
	assertManaged(t, second, workspaceURL, true)
	assertManaged(t, second, unmanagedURL, false)
}

// queueState is the state of one SQS queue, as the s3 backend keeps it.
func queueState(t *testing.T, queueURL string) []byte {
	t.Helper()
	content, err := json.Marshal(map[string]any{
		"version": 4,
		"resources": []map[string]any{{
			"mode": "managed", "type": "aws_sqs_queue", "name": "queue",
			"instances": []map[string]any{{"attributes": map[string]any{"id": queueURL}}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return content
}

// discoverQueues runs discover for the SQS queues with --managed-state
// source and returns its selection file.
func discoverQueues(ctx context.Context, t *testing.T, source string) *selection.File {
	t.Helper()
	path := t.TempDir() + "/selection.yaml"
	root := cli.NewCmdRoot()
	root.SetArgs([]string{"discover", "aws", "--regions=us-east-1", "--services=sqs", "--selection=" + path, "--managed-state=" + source})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("infraharvest discover --managed-state=%s: %v", source, err)
	}
	f, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// assertManaged checks the queue at queueURL is in f, and excluded as
// managed by Terraform when managed, included when not.
func assertManaged(t *testing.T, f *selection.File, queueURL string, managed bool) {
	t.Helper()
	for _, r := range f.Resources {
		if r.Type != "aws_sqs_queue" || r.ID != queueURL {
			continue
		}
		if got := !r.Include && strings.HasPrefix(r.Reason, report.TerraformPrefix); got != managed {
			t.Errorf("%s: excluded as managed is %v (include=%v, %q), want %v", queueURL, got, r.Include, r.Reason, managed)
		}
		if !managed && !r.Include {
			t.Errorf("%s isn't in the state but is left out: %q", queueURL, r.Reason)
		}
		return
	}
	t.Errorf("discover lists no queue %s", queueURL)
}
