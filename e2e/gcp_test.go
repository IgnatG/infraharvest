// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/cmd"
	"github.com/IgnatG/infraharvest/providers/gcp"
	"github.com/IgnatG/infraharvest/report"
)

// gcpProject is the project testdata/gcp creates its resources in.
const gcpProject = "infraharvest-e2e"

// gcpServices are the services testdata/gcp covers.
var gcpServices = []string{"firewall", "gcs", "networks", "pubsub", "subnetworks"}

// TestGCPRoundTrip creates resources in floci-gcp, a GCP emulator, imports
// them with --engine=terraform (or E2E_ENGINE=tofu) and checks the engine
// plans every one as an import with no changes.
func TestGCPRoundTrip(t *testing.T) {
	endpoint := os.Getenv(gcp.EndpointEnv)
	if endpoint == "" {
		t.Skip(gcp.EndpointEnv + " is not set; start floci-gcp in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("%s=%s: the test creates resources, so it only runs against a local emulator", gcp.EndpointEnv, endpoint)
	}
	isolateGCPConfig(t, endpoint)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")

	state := seed(ctx, t, "gcp", execPath, plugins)

	out := t.TempDir()
	root := cmd.NewCmdRoot()
	root.SetArgs([]string{
		"import", "google",
		"--engine=" + engineName,
		"--terraform-path=" + execPath,
		"--projects=" + gcpProject,
		"--regions=us-central1",
		"--resources=" + strings.Join(gcpServices, ","),
		"--path-output=" + out,
		"--all",
		"--allow-partial",
	})
	if err := root.ExecuteContext(ctx); cmd.ExitCode(err) != report.ExitOK && cmd.ExitCode(err) != report.ExitPartial {
		t.Fatalf("infraharvest import google: %v", err)
	}

	var coverage report.Report
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(out, report.Dir, "coverage.json"))), &coverage); err != nil {
		t.Fatal(err)
	}
	if coverage.Totals.LeftOut != 0 || coverage.Totals.Failed != 0 || len(coverage.Failures) != 0 {
		t.Errorf("coverage.json: %+v, failures %v", coverage.Totals, coverage.Failures)
	}
	for _, d := range coverage.Directories {
		for _, c := range d.Checks {
			if !c.Passed {
				t.Errorf("%s: %s failed: %v", d.Path, c.Name, c.Details)
			}
		}
	}

	imported := map[string]int{}
	for _, dir := range generatedDirs(t, out) {
		for typ, n := range checkNoChanges(ctx, t, dir, execPath, plugins, nil) {
			imported[typ] += n
		}
	}
	seeded := map[string]int{}
	for _, r := range state {
		seeded[r.Type]++
	}
	for _, typ := range sortedKeys(seeded) {
		if imported[typ] < seeded[typ] {
			t.Errorf("%s: created %d, imported %d", typ, seeded[typ], imported[typ])
		}
	}
}

// isolateGCPConfig points the listers (gcp.EndpointEnv) and the Terraform
// provider (its custom endpoints) at the emulator, with a placeholder token:
// the test never uses a real project or real credentials.
func isolateGCPConfig(t *testing.T, endpoint string) {
	t.Helper()
	for k, v := range map[string]string{
		"GOOGLE_APPLICATION_CREDENTIALS":          "",
		"GOOGLE_CREDENTIALS":                      "",
		"GOOGLE_OAUTH_ACCESS_TOKEN":               "e2e-placeholder",
		"GOOGLE_CLOUD_PROJECT":                    gcpProject,
		"GOOGLE_COMPUTE_CUSTOM_ENDPOINT":          endpoint + "/compute/v1/",
		"GOOGLE_STORAGE_CUSTOM_ENDPOINT":          endpoint + "/storage/v1/",
		"GOOGLE_PUBSUB_CUSTOM_ENDPOINT":           endpoint + "/v1/",
		"GOOGLE_RESOURCE_MANAGER_CUSTOM_ENDPOINT": endpoint + "/v1/",
		"GOOGLE_SERVICE_USAGE_CUSTOM_ENDPOINT":    endpoint + "/v1/",
	} {
		t.Setenv(k, v)
	}
}
