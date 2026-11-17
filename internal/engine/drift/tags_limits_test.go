// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

// A resource that gets a default tag, and changes it in the cloud to a
// value of its own, reports the provider's value as what it was: the
// resource's own tags had no such key, tags_all had it.
func TestDriftDefaultTagChangeKeepsTheValueItHad(t *testing.T) {
	t.Parallel()
	dir, staging := awsRoot(t)
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{tagUpdate("aws_s3_bucket.logs", awsLogs("prod"), awsLogs("dev"))}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		tagUpdate("aws_s3_bucket.logs", awsLogs("dev"), awsLogs("prod")),
		tagNoop("aws_sqs_queue.jobs", awsJobs("prod")),
	}}
	edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{tagNoop("aws_s3_bucket.logs", awsLogs("dev")), tagNoop("aws_sqs_queue.jobs", awsJobs("prod"))}}
	result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := outcomes(result)["aws_s3_bucket.logs tags.Env"]
	if c.Before != "prod" || c.After != "dev" {
		t.Errorf("tags.Env went from %v to %v, want prod to dev", c.Before, c.After)
	}
}
