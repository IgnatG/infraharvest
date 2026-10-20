// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || google)

package e2e

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/report"
)

// TestGoogleDrift imports testdata/google, applies the roots into local
// states, and changes the bucket's team label out of band, through the
// Cloud Storage API. The bucket is a call of the curated storage bucket
// module, so drift traces the label to the call's labels
// (source.module_path). Reported, it is kept, exiting 4 with
// --detailed-exitcode, and the code stays as it was; accepted, it is
// written into the call's labels. The code then plans no change but to
// the labels the provider keeps only in state, and, once the state is
// refreshed, drift finds nothing. Then one of the key ring's keys, in a
// call of the curated kms module, gets another rotation period: accepted,
// the edit of the call's key_rotation_period would change the other key
// too, so the plan rejects it (rejected_by_plan), exiting 1 with
// --detailed-exitcode, and the code stays as it was.
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
	patch(t, e, e.endpoint+"/storage/v1/b/"+bucket, `{"labels": {"team": "ops", "env": "prod"}}`)

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

	// The rotation period of one of the key ring's keys, a call of the
	// curated kms module, which sets every key's from one input: accepted,
	// the edit of the call's key_rotation_period reaches the other key,
	// which didn't change, so the plan rejects it.
	t.Run("rejected by the plan", func(t *testing.T) {
		key := stateValue(t, e.state, "google_kms_crypto_key", "app", "id")
		patch(t, e, e.endpoint+"/v1/"+key+"?updateMask=rotationPeriod", `{"rotationPeriod": "8640000s"}`)
		code := codeFiles(t, out)
		d := drift.mustExit(t, report.ExitDriftRejected, "--accept", "type=google_kms_crypto_key", "--detailed-exitcode")
		checkDriftChange(t, "rejected by the plan", driftChange(t, "rejected by the plan", d, "google_kms_crypto_key", "rotation_period"), "7776000s", "8640000s", report.DriftAccept, report.DriftDecidedByFlag, report.DriftRejectedByPlan)
		checkCodeUnchanged(t, "rejected by the plan", code, out)
	})
}

// patch sends body to url, a resource of floci-gcp, as an update of it,
// skipping the test if floci-gcp doesn't update the resource.
func patch(t *testing.T, e googleEstate, url, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(e.ctx, http.MethodPatch, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer e2e-placeholder")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented:
		t.Skipf("floci-gcp doesn't update the resource (PATCH %s: %s): %s", url, resp.Status, content)
	case resp.StatusCode != http.StatusOK:
		t.Fatalf("PATCH %s: %s: %s", url, resp.Status, content)
	}
}
