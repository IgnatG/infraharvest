// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"testing"

	"github.com/infraharvest/infraharvest-cli/internal/engine"
	"github.com/infraharvest/infraharvest-cli/internal/report"
	"github.com/infraharvest/infraharvest-cli/internal/selection"
)

// A preview decides the changes a detection found as the run with the
// decisions would, and counts only those the next apply would revert.
func TestPreviewDrift(t *testing.T) {
	t.Parallel()
	const root = "aws/1/eu-west-2"
	change := func(address, attribute, outcome string, after any) report.DriftChange {
		return report.DriftChange{Root: root, Address: address, Type: "aws_s3_bucket", Attribute: attribute, Before: "a", After: after, Kind: engine.DriftChanged, Outcome: outcome}
	}
	changes := []report.DriftChange{
		change("aws_s3_bucket.logs", "tags.Env", report.DriftKept, "b"),
		change("aws_s3_bucket.logs", "tags.Owner", report.DriftKept, "b"),
		change("aws_s3_bucket.logs", "versioning[0].enabled", report.DriftKept, true),
		change("aws_s3_bucket.logs", "arn", report.DriftAbsorbed, "b"),
		change("aws_s3_bucket.logs", "tags.Unknown", report.DriftNotAssessable, "b"),
	}
	f := &DriftDecisions{Version: 1, Rules: []DriftDecisionRule{{Accept: &DriftMatch{Attribute: selection.Patterns{"tags.*"}}}}, Changes: []DriftDecisionEntry{
		// Decided for another value of tags.Owner: not applied.
		{Root: root, Address: "aws_s3_bucket.logs", Attribute: "tags.Owner", Fingerprint: driftFingerprint(engine.DriftChanged, "a", "c", nil), Decision: engine.DriftAccept},
	}}
	f.index()

	for _, tc := range []struct {
		name      string
		decisions *DriftDecisions
		rules     []DriftRule
		want      DriftPreview
	}{
		{"nothing", nil, nil, DriftPreview{Keep: 3}},
		{"accept all", nil, []DriftRule{{Accept: true}}, DriftPreview{Accept: 3}},
		{"accept all, keep tags", nil, []DriftRule{{Accept: true}, {Field: "attribute", Pattern: "tags.*"}}, DriftPreview{Accept: 1, Keep: 2}},
		{"the decisions file", f, nil, DriftPreview{Accept: 1, Keep: 2, Stale: 1}},
		{"the file, then --accept", f, []DriftRule{{Accept: true, Field: "attribute", Pattern: "tags.Owner"}}, DriftPreview{Accept: 2, Keep: 1, Stale: 1}},
	} {
		if got := PreviewDrift(tc.decisions, tc.rules, changes); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
