// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// driftBackendAccount is the account TestAWSDriftBackend creates
// testdata/aws-drift-queues in, which no other test uses.
const driftBackendAccount = "737373737373"

// driftBackendProfile is the profile the test imports driftBackendAccount
// as.
const driftBackendProfile = "infraharvest-e2e-drift-backend"

// TestAWSDriftBackend imports testdata/aws-drift-queues with an S3 backend
// in the emulator, from the configuration file, and applies the root, so
// that its state is in the fixture's bucket, not in the root. It then
// changes a queue's tag out of band: drift initialises the root with its
// backend and reads the state from the bucket, reports the change, kept,
// exiting 4 with --detailed-exitcode, and, with --accept, writes it into
// the code, which then plans no change; once the state in the bucket is
// refreshed, drift finds nothing.
func TestAWSDriftBackend(t *testing.T) {
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
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	account := tfexec.Var("account=" + driftBackendAccount)
	fixture := seedApplied(ctx, t, "aws-drift-queues", execPath, plugins, account)
	bucket := stateValue(t, fixture.resources(ctx, t), "aws_s3_bucket", "state", "bucket")
	useAWSAccount(t, driftBackendAccount, driftBackendProfile)
	config := filepath.Join(t.TempDir(), "infraharvest.yaml")
	if err := os.WriteFile(config, []byte("version: 1\nbackend:\n  s3:\n    bucket: "+bucket+"\n    region: us-east-1\n    key_prefix: e2e\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	runMovesImport(ctx, t,
		"import", "aws",
		"--engine="+engineName,
		"--terraform-path="+execPath,
		"--profile="+driftBackendProfile,
		"--regions=us-east-1",
		"--services=sqs",
		"--config="+config,
		"--out="+out,
		"--all",
	)
	checkCoverage(t, "the import", coverageOf(t, out), nil)
	dirs := applyGenerated(ctx, t, out, execPath, plugins)
	for _, dir := range dirs {
		if backend := readFile(t, filepath.Join(dir, importer.BackendFileName)); !strings.Contains(backend, bucket) {
			t.Errorf("%s: the backend isn't the bucket %s:\n%s", dir, bucket, backend)
		}
		if _, err := os.Stat(filepath.Join(dir, "terraform.tfstate")); err == nil {
			t.Errorf("%s keeps a local state, not one in the bucket", dir)
		}
	}
	drift := driftRun{ctx: ctx, engineName: engineName, execPath: execPath, out: out}

	fixture.apply(ctx, t, account, tfexec.Var("team=ops"))
	code := codeFiles(t, out)
	d := drift.mustExit(t, report.ExitDrift, "--detailed-exitcode")
	checkCodeUnchanged(t, "report only", code, out)
	if len(d.Roots) != len(dirs) {
		t.Errorf("report only: roots %+v, want %d", d.Roots, len(dirs))
	}
	for _, r := range d.Roots {
		if r.Backend != "s3" {
			t.Errorf("report only: %s has backend %q, want s3", r.Path, r.Backend)
		}
	}
	team := driftChange(t, "report only", d, "aws_sqs_queue", "tags.team")
	checkDriftChange(t, "report only", team, "web", "ops", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)

	d = drift.mustExit(t, report.ExitOK, "--accept", "attribute=tags.team", "--detailed-exitcode")
	checkDriftChange(t, "accepted", driftChange(t, "accepted", d, "aws_sqs_queue", "tags.team"), "web", "ops", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	if !codeMatches(t, out, `team\s*=\s*"ops"`) || codeMatches(t, out, `team\s*=\s*"web"`) {
		t.Errorf("accepted: the code doesn't have the queue's tag team = \"ops\" in place of \"web\"")
	}

	checkAccepted(ctx, t, dirs, execPath, plugins)
	if d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode"); len(d.Changes) != 0 {
		t.Errorf("with the state refreshed: changes\n%s", driftChanges(d))
	}
}
