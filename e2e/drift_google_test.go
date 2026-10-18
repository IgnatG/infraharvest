// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || google)

package e2e

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/infraharvest/infraharvest-cli/internal/report"
)

// TestGoogleDrift imports testdata/google, applies the roots into local
// states, and changes the bucket's team label out of band, through the
// Cloud Storage API. The bucket is a call of the curated storage bucket
// module, so drift traces the label to the call's labels
// (source.module_path). Reported, it is kept, exiting 4 with
// --detailed-exitcode, and the code stays as it was; accepted, it is
// written into the call's labels. The code then plans no change but to
// the labels the provider keeps only in state, and, once the state is
// refreshed, drift finds nothing.
//
// Only the bucket has labels in testdata/google, so labels several
// resources share aren't tested here; the AWS test covers shared tags.
// The test changes the estate the other Google Cloud tests import, so CI
// runs it on a runner of its own.
func TestGoogleDrift(t *testing.T) {
	e := seedGoogle(t)
	useAssetSearch(t, e, e.state)
	out := importGoogle(t, e, "", "--all")
	checkCoverage(t, "the import", coverageOf(t, out), nil)
	dirs := applyGenerated(e.ctx, t, out, e.execPath, e.plugins)
	drift := driftRun{ctx: e.ctx, engineName: e.engineName, execPath: e.execPath, out: out}

	bucket := stateValue(t, e.state, "google_storage_bucket", "assets", "name")
	req, err := http.NewRequestWithContext(e.ctx, http.MethodPatch, e.endpoint+"/storage/v1/b/"+bucket, strings.NewReader(`{"labels": {"team": "ops", "env": "prod"}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer e2e-placeholder")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented:
		t.Skipf("floci-gcp doesn't update a bucket's labels (PATCH %s: %s): %s", bucket, resp.Status, body)
	case resp.StatusCode != http.StatusOK:
		t.Fatalf("PATCH %s: %s: %s", bucket, resp.Status, body)
	}

	code := codeFiles(t, out)
	d := drift.mustExit(t, report.ExitDrift, "--detailed-exitcode")
	checkCodeUnchanged(t, "report only", code, out)
	label := driftChange(t, "report only", d, "google_storage_bucket", "labels.team")
	checkDriftChange(t, "report only", label, "web", "ops", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	if label.ModuleCall == "" || label.Source == nil || label.Source.ModulePath == "" {
		t.Errorf("report only: %s isn't traced through its module call %q: %+v", label.Address, label.ModuleCall, label.Source)
	}

	d = drift.mustExit(t, report.ExitOK, "--accept", "type=google_storage_bucket", "--detailed-exitcode")
	checkDriftChange(t, "accepted", driftChange(t, "accepted", d, "google_storage_bucket", "labels.team"), "web", "ops", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	if !codeMatches(t, out, `team\s*=\s*"ops"`) || codeMatches(t, out, `team\s*=\s*"web"`) {
		t.Errorf("accepted: the code doesn't have the bucket's label team = \"ops\" in place of \"web\"")
	}

	checkAccepted(e.ctx, t, dirs, e.execPath, e.plugins)
	if d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode"); len(d.Changes) != 0 {
		t.Errorf("with the state refreshed: changes\n%s", driftChanges(d))
	}
}
