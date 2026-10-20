// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/report"
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
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
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
	root := cli.NewCmdRoot()
	root.SetArgs([]string{
		"import", "aws",
		"--engine=" + engineName,
		"--terraform-path=" + execPath,
		"--accounts=111111111111,222222222222",
		"--parallel=2",
		"--regions=us-east-1",
		"--services=sqs",
		"--out=" + out,
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
		generated := configuration(t, dir)
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

	checkAccountEnvironments(ctx, t, out, engineName, execPath, plugins)
}

// accountEnvironments maps the accounts of testdata/aws-accounts to
// environments, as a configuration file's environments do.
var accountEnvironments = map[string]string{
	"111111111111": "dev",
	"222222222222": "prod",
}

// checkAccountEnvironments imports the accounts again, with environments
// in the configuration file that map each account to one: each gets its
// roots under envs/<env>/, which plan as imports with no changes. Into
// out, where an import without environments generated the roots, the
// roots stay where they are, and the report names them.
func checkAccountEnvironments(ctx context.Context, t *testing.T, out, engineName, execPath, plugins string) {
	t.Helper()
	config := filepath.Join(t.TempDir(), "infraharvest.yaml")
	content := "version: 1\nenvironments:\n"
	for _, account := range []string{"111111111111", "222222222222"} {
		content += "  " + accountEnvironments[account] + ":\n    aws: { accounts: [\"" + account + "\"] }\n"
	}
	if err := os.WriteFile(config, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	importInto := func(out string, more ...string) report.Report {
		t.Helper()
		root := cli.NewCmdRoot()
		root.SetArgs(append([]string{
			"import", "aws",
			"--config=" + config,
			// The roots per environment, as they are laid out: merging them
			// is TestAWSEnvironments's.
			"--merge-environments=false",
			"--engine=" + engineName,
			"--terraform-path=" + execPath,
			"--accounts=111111111111,222222222222",
			"--parallel=2",
			"--regions=us-east-1",
			"--services=sqs",
			"--out=" + out,
			"--all",
		}, more...))
		if err := root.ExecuteContext(ctx); err != nil {
			t.Fatalf("infraharvest import with environments %v: %v", more, err)
		}
		var coverage report.Report
		if err := json.Unmarshal([]byte(readFile(t, filepath.Join(out, report.Dir, "coverage.json"))), &coverage); err != nil {
			t.Fatal(err)
		}
		if len(coverage.Failures) != 0 || coverage.Totals.LeftOut != 0 {
			t.Errorf("coverage.json: %+v, failures %v", coverage.Totals, coverage.Failures)
		}
		return coverage
	}

	split := t.TempDir()
	if coverage := importInto(split); len(coverage.KeptRoots) != 0 {
		t.Errorf("a new --out kept roots: %+v", coverage.KeptRoots)
	}
	for account, queue := range parallelAccounts {
		dir := filepath.Join(split, "envs", accountEnvironments[account], "aws", account, "us-east-1")
		if generated := configuration(t, dir); !strings.Contains(generated, queue) {
			t.Errorf("%s: %s is missing", dir, queue)
		}
		if imported := checkNoChanges(ctx, t, dir, execPath, plugins, nil); imported["aws_sqs_queue"] != 1 {
			t.Errorf("%s: imported %v, want its queue", dir, imported)
		}
	}
	if _, err := os.Stat(filepath.Join(split, "aws")); err == nil {
		t.Errorf("%s: roots outside envs/", split)
	}

	// The roots out has stay, unchanged.
	coverage := importInto(out, "--resume")
	if len(coverage.KeptRoots) != len(parallelAccounts) {
		t.Errorf("kept roots: %+v, want one per account", coverage.KeptRoots)
	}
	for _, kept := range coverage.KeptRoots {
		account := strings.Split(kept.Path, "/")[1]
		if want := "aws/" + account + "/us-east-1"; kept.Path != want || len(kept.Environments) != 1 || kept.Environments[0] != accountEnvironments[account] {
			t.Errorf("kept root %+v, want %s of %s", kept, want, accountEnvironments[account])
		}
	}
	if _, err := os.Stat(filepath.Join(out, "envs")); err == nil {
		t.Errorf("%s: roots under envs/ beside the earlier import's", out)
	}
}
