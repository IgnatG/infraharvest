// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/IgnatG/infraharvest/internal/cli"
)

// TestAWSBootstrap checks infraharvest bootstrap for an S3 backend: it
// writes the root that creates the state bucket, with Terraform (or
// OpenTofu with E2E_ENGINE=tofu) applies that root in the emulator, as
// people apply it once, and finds the bucket as the root makes it:
// versioned and not public. The root is applied again to a plan with no
// changes, as a root that is applied once and then planned along with the
// others must be.
func TestAWSBootstrap(t *testing.T) {
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)
	// As in TestAWSManagedStateS3: the S3 client addresses a bucket as
	// <bucket>.<host>, which "localhost" may not resolve.
	if u, err := url.Parse(endpoint); err == nil && u.Hostname() == "localhost" {
		u.Host = "127.0.0.1:" + u.Port()
		t.Setenv("AWS_ENDPOINT_URL_S3", u.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)
	cache := t.TempDir()
	_, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := pluginCacheDir(cache)

	const bucket = "e2e-bootstrap-state"
	configFile := filepath.Join(t.TempDir(), "infraharvest.yaml")
	if err := os.WriteFile(configFile, []byte("version: 1\nbackend:\n  s3:\n    bucket: "+bucket+"\n    region: us-east-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	root := cli.NewCmdRoot()
	root.SetArgs([]string{"bootstrap", "--config=" + configFile, "--out=" + out})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("infraharvest bootstrap: %v", err)
	}
	dir := filepath.Join(out, "bootstrap")
	for _, name := range []string{"main.tf", "versions.tf", "README.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("bootstrap wrote no %s: %v", name, err)
		}
	}
	if main := readFile(t, filepath.Join(dir, "main.tf")); !strings.Contains(main, `bucket = "`+bucket+`"`) {
		t.Errorf("bootstrap/main.tf doesn't create the bucket %s:\n%s", bucket, main)
	}

	applyRoot(ctx, t, dir, execPath, plugins, nil)
	t.Cleanup(func() {
		// The root protects the bucket from destroy, and the emulator keeps
		// everything in memory: empty the bucket's way out, not Terraform's.
		cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion("us-east-1"))
		if err == nil {
			_, _ = s3.NewFromConfig(cfg).DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: awssdk.String(bucket)})
		}
	})

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"))
	if err != nil {
		t.Fatal(err)
	}
	store := s3.NewFromConfig(cfg)
	if _, err := store.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: awssdk.String(bucket)}); err != nil {
		t.Fatalf("the bootstrap root didn't create the bucket %s: %v", bucket, err)
	}
	versioning, err := store.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: awssdk.String(bucket)})
	if err != nil {
		t.Fatalf("read the versioning of %s: %v", bucket, err)
	}
	if versioning.Status != s3types.BucketVersioningStatusEnabled {
		t.Errorf("bucket %s versioning is %q, want Enabled", bucket, versioning.Status)
	}
	block, err := store.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: awssdk.String(bucket)})
	if err != nil {
		t.Fatalf("read the public access block of %s: %v", bucket, err)
	}
	if c := block.PublicAccessBlockConfiguration; c == nil || !awssdk.ToBool(c.BlockPublicAcls) || !awssdk.ToBool(c.BlockPublicPolicy) || !awssdk.ToBool(c.IgnorePublicAcls) || !awssdk.ToBool(c.RestrictPublicBuckets) {
		t.Errorf("bucket %s public access block is %+v, want every block on", bucket, c)
	}

	// Nothing to change once it is applied.
	plan := planRoot(ctx, t, dir, execPath, plugins)
	for _, c := range plan.ResourceChanges {
		if !c.Change.Actions.NoOp() {
			t.Errorf("planning the bootstrap root again: %s %v, want no changes", c.Address, c.Change.Actions)
		}
	}
}
