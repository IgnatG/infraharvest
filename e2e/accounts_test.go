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
	"github.com/IgnatG/infraharvest/engine"
	"github.com/IgnatG/infraharvest/report"
)

// parallelAccounts are the accounts testdata/aws-accounts creates a queue
// in, each named after its account's place.
var parallelAccounts = map[string]string{
	"111111111111": "infraharvest-e2e-first-account",
	"222222222222": "infraharvest-e2e-second-account",
}

// TestAWSParallelAccounts imports two accounts of the emulator at once
// (--accounts, --parallel), each through a role: each account gets its own
// root with its own resources, which plans as imports with no changes.
func TestAWSParallelAccounts(t *testing.T) {
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		t.Skip("AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	seed(ctx, t, "aws-accounts", execPath, plugins)

	out := t.TempDir()
	root := cmd.NewCmdRoot()
	root.SetArgs([]string{
		"import", "aws",
		"--engine=" + engineName,
		"--terraform-path=" + execPath,
		"--accounts=111111111111,222222222222",
		"--parallel=2",
		"--regions=us-east-1",
		"--resources=sqs",
		"--path-output=" + out,
		"--all",
	})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("infraharvest import --accounts --parallel: %v", err)
	}

	var coverage report.Report
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(out, report.Dir, "coverage.json"))), &coverage); err != nil {
		t.Fatal(err)
	}
	if len(coverage.Failures) != 0 || coverage.Totals.LeftOut != 0 || len(coverage.Scopes) != len(parallelAccounts) {
		t.Errorf("coverage.json: %+v, scopes %+v, failures %v", coverage.Totals, coverage.Scopes, coverage.Failures)
	}
	for account, queue := range parallelAccounts {
		dir := filepath.Join(out, "aws", account, "us-east-1")
		generated := readFile(t, filepath.Join(dir, engine.GeneratedFileName))
		if !strings.Contains(generated, queue) {
			t.Errorf("%s: %s is missing", dir, queue)
		}
		for other, otherQueue := range parallelAccounts {
			if other != account && strings.Contains(generated, otherQueue) {
				t.Errorf("%s: has %s of account %s", dir, otherQueue, other)
			}
		}
		if imported := checkNoChanges(ctx, t, dir, execPath, plugins, nil); imported["aws_sqs_queue"] != 1 {
			t.Errorf("%s: imported %v, want its queue", dir, imported)
		}
	}
}
