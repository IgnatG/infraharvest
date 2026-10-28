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
)

// queryAccount is the account testdata/aws-query creates its resources in,
// which no other test uses, queryRegion the region and queryProfile the
// profile the test signs in to queryAccount as.
const (
	queryAccount = "131313131313"
	queryRegion  = "ap-southeast-2"
	queryProfile = "infraharvest-e2e-query"
)

// TestAWSQuery creates a VPC flow log in an account of the emulator of its
// own (testdata/aws-query) and takes it through the query service, which
// lists it through Terraform's list resources (see checkQueryRoundTrip).
func TestAWSQuery(t *testing.T) {
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cache := t.TempDir()
	_, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	created := seed(ctx, t, "aws-query", execPath, plugins)
	// Discover and the import sign in to the account of the estate, as a
	// profile of its own (see useAWSAccount).
	useAWSAccount(t, queryAccount, queryProfile)

	flowLog := ""
	for _, r := range created {
		if r.Type == "aws_flow_log" {
			flowLog, _ = r.AttributeValues["id"].(string)
		}
	}
	if flowLog == "" {
		t.Fatal("seed: no flow log")
	}
	checkQueryRoundTrip(ctx, t, queryRoundTrip{
		command:  "aws",
		scope:    []string{"--regions=" + queryRegion, "--profile=" + queryProfile},
		typ:      "aws_flow_log",
		identity: map[string]string{"account_id": queryAccount, "region": queryRegion, "id": flowLog},
		file:     "flow_log.tf",
		plugins:  plugins,
	})
}
