// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// selectionRules is a selection file of rules alone, as a person writes
// one before the first discover: nothing is included by default; the
// project's queues are, by tag, but not a queue without the tag; buckets
// are by a name glob, with their configuration, which has the bucket's
// name, but not the logs bucket, whose exclude rule comes last and wins;
// and topics without an owner tag are.
const selectionRules = `version: 1
defaults:
  include: false
rules:
  - include: { type: aws_sqs_queue, tags: { Project: infraharvest-e2e } }
  - exclude: { type: aws_sqs_queue, tags: { Project: "" } }
  - include: { type: "aws_s3_bucket*", name: "infraharvest-e2e-*" }
  - exclude: { type: "aws_s3_bucket*", name: "*-logs" }
  - include: { type: aws_sns_topic, tags: { owner: "" } }
resources: []
`

// TestAWSSelectionRules imports testdata/aws through selectionRules: the
// import imports what the rules include, by tag, by a name glob and by
// the absence of a tag, and leaves out, each with its reason, what an
// exclude rule matches, what no rule includes, and the bucket a
// CloudFormation stack owns, which a rule can't include.
func TestAWSSelectionRules(t *testing.T) {
	e := seedAWS(t)
	path := filepath.Join(t.TempDir(), "selection.yaml")
	if err := os.WriteFile(path, []byte(selectionRules), 0o644); err != nil {
		t.Fatal(err)
	}
	out := importAWS(e.ctx, t, e.engineName, e.execPath, "--selection="+path)
	coverage := coverageOf(t, out)
	checkCoverage(t, "the rules", coverage, nil)

	imported := map[string][]string{}
	for _, d := range coverage.Directories {
		for _, r := range d.Imported {
			typ := engine.AddressType(r.Address)
			imported[typ] = append(imported[typ], r.ID)
		}
	}
	importedID := func(typ, suffix string) bool {
		return slices.ContainsFunc(imported[typ], func(id string) bool { return strings.HasSuffix(id, suffix) })
	}
	for _, want := range []struct{ typ, suffix string }{
		{"aws_sqs_queue", "/infraharvest-e2e-jobs"},
		{"aws_sns_topic", ":infraharvest-e2e-events"},
		{"aws_s3_bucket", "infraharvest-e2e-state"},
		{"aws_s3_bucket", "infraharvest-e2e-artifacts"},
		{"aws_s3_bucket_versioning", "infraharvest-e2e-artifacts"},
	} {
		if !importedID(want.typ, want.suffix) {
			t.Errorf("the rules include %s %s, which isn't imported: %v", want.typ, want.suffix, imported[want.typ])
		}
	}
	for typ := range imported {
		if typ != "aws_sqs_queue" && typ != "aws_sns_topic" && !strings.HasPrefix(typ, "aws_s3_bucket") {
			t.Errorf("no rule includes %s, yet %v are imported", typ, imported[typ])
		}
	}

	excluded := func(typ, id, reason string) {
		t.Helper()
		for _, x := range coverage.Excluded {
			if x.Type == typ && x.ID == id {
				if !strings.Contains(x.Reason, reason) {
					t.Errorf("%s %s is left out as %q, want %q", typ, id, x.Reason, reason)
				}
				return
			}
		}
		t.Errorf("%s %s isn't reported left out, as %q", typ, id, reason)
	}
	excluded("aws_s3_bucket", "infraharvest-e2e-logs", "excluded by a rule in the selection file")
	excluded("aws_s3_bucket", "infraharvest-e2e-cfn-assets", "CloudFormation stack infraharvest-e2e-app")
	for _, id := range []string{"infraharvest-e2e-logs", "infraharvest-e2e-cfn-assets"} {
		if importedID("aws_s3_bucket", id) {
			t.Errorf("the bucket %s is imported", id)
		}
	}
	// A bucket's configuration follows it, by its name too.
	if importedID("aws_s3_bucket_versioning", "infraharvest-e2e-logs") {
		t.Error("the logs bucket's versioning is imported")
	}
	vpcs := 0
	for _, x := range coverage.Excluded {
		if x.Type == "aws_vpc" {
			vpcs++
			if x.Reason != "excluded by the selection file's defaults" && !strings.Contains(x.Reason, "default VPC") {
				t.Errorf("aws_vpc %s is left out as %q, want by the selection file's defaults", x.ID, x.Reason)
			}
		}
	}
	if vpcs == 0 {
		t.Error("no VPC is reported left out, though no rule includes one")
	}
}
