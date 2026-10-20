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

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/providers/google"
	"github.com/IgnatG/infraharvest/internal/report"
)

// googleEnvironmentsConfig tells the environments of one project apart by
// their env label, as configuration files do for environments that share
// a project.
const googleEnvironmentsConfig = `version: 1
environments:
  dev:
    tags: { env: dev }
  prod:
    tags: { env: prd }
    tokens: [prd]
`

// TestGoogleEnvironments imports testdata/google-environments, a bucket
// of dev and one of prod in one project, which the configuration file's
// environments tell apart by label: the roots of the two environments
// merge into one root they share, google/global, which plans as each
// environment with no changes.
func TestGoogleEnvironments(t *testing.T) {
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
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	state := seed(ctx, t, "google-environments", execPath, plugins)
	t.Setenv(google.EndpointEnv, assetSearch(t, endpoint, state).URL)

	out := t.TempDir()
	root := cli.NewCmdRoot()
	root.SetArgs([]string{
		"import", "google",
		"--config=" + writeConfig(t, googleEnvironmentsConfig),
		"--engine=" + engineName,
		"--terraform-path=" + execPath,
		"--projects=" + googleProject,
		"--services=cloud_asset",
		"--regions=global",
		"--out=" + out,
		"--all",
	})
	if err := root.ExecuteContext(ctx); importer.ExitCode(err) != report.ExitOK {
		t.Fatalf("infraharvest import google: %v", err)
	}
	coverage := coverageOf(t, out)
	checkCoverage(t, "the import", coverage, nil)
	checkMerged(t, out, coverage, "google/global", "dev", "prod")
	imported := checkMergedPlans(ctx, t, filepath.Join(out, "google", "global"), execPath, plugins, nil, "dev", "prod")
	for _, env := range []string{"dev", "prod"} {
		if imported[env]["google_storage_bucket"] != 1 {
			t.Errorf("%s imports %v, want its bucket", env, imported[env])
		}
	}
}
