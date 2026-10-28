// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || google)

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/providers/google"
)

// TestGoogleQuery enables a service in the project of floci-gcp
// (testdata/google-query) and takes it through the query service, which
// lists it through Terraform's list resources (see checkQueryRoundTrip).
// The other services, and the stand-in for Cloud Asset Inventory, have no
// part in it.
func TestGoogleQuery(t *testing.T) {
	endpoint := os.Getenv(google.EndpointEnv)
	if endpoint == "" {
		skipUnconfigured(t, google.EndpointEnv+" is not set; start floci-gcp in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("%s=%s: the test creates resources, so it only runs against a local emulator", google.EndpointEnv, endpoint)
	}
	isolateGoogleConfig(t, endpoint)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cache := t.TempDir()
	_, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	seed(ctx, t, "google-query", execPath, plugins)

	checkQueryRoundTrip(ctx, t, queryRoundTrip{
		command:  "google",
		scope:    []string{"--projects=" + googleProject, "--regions=global"},
		typ:      "google_project_service",
		identity: map[string]string{"project": googleProject, "service": "pubsub.googleapis.com"},
		file:     "project.tf",
		plugins:  plugins,
	})
}
