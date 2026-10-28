// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// TestAWSRegistryModuleMoves plans the roots of testdata/aws-registry-moves
// with the engine: one moves a bucket into a call of the curated s3-bucket
// module, from the registry, and the other moves one out of such a call
// the root still makes. The engine accepts both moved blocks: moved
// blocks stay within a module package, but the call is the root's own,
// and only the module's own calls are in its package.
func TestAWSRegistryModuleMoves(t *testing.T) {
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test plans against the emulator only", endpoint)
	}
	isolateAWSConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cache := t.TempDir()
	_, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	for _, name := range []string{"into", "out-of"} {
		dir := t.TempDir()
		fixture := filepath.Join("testdata", "aws-registry-moves", name, "main.tf")
		if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(readFile(t, fixture)), 0o644); err != nil {
			t.Fatal(err)
		}
		tf, err := tfrun.New(dir, execPath, plugins, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := tf.Init(ctx); err != nil {
			t.Fatalf("%s: terraform init: %v", fixture, err)
		}
		if _, err := tf.Plan(ctx); err != nil {
			t.Errorf("%s: terraform plan: %v, want the moved block accepted", fixture, err)
		}
	}
}
