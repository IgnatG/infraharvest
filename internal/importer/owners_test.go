// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"reflect"
	"testing"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
	"github.com/infraharvest/infraharvest-cli/internal/report"
)

// Resources another state or tool manages are left out by default, with
// the reason, which the report counts as managed.
func TestExcludeOwned(t *testing.T) {
	bucket := harvest.Record("state", "state", "aws_s3_bucket", "aws", nil)
	fn := harvest.Record("api", "api", "aws_lambda_function", "aws", nil)
	stack := harvest.Record("queue", "queue", "aws_sqs_queue", "aws", nil)
	pulumi := harvest.Record("web", "web", "google_storage_bucket", "google", map[string]string{"labels.managed-by": "pulumi"})
	plain := harvest.Record("logs", "logs", "aws_s3_bucket", "aws", nil)
	listed := listing{
		resources: map[string][]harvest.Resource{"s3": {bucket, plain}, "lambda": {fn}, "sqs": {stack}, "gcs": {pulumi}},
		tags: map[string]map[string]string{
			tagKey(bucket): {"ManagedBy": "Terraform"},
			tagKey(fn):     {"managed-by": "opentofu"},
			tagKey(stack):  {"aws:cloudformation:stack-name": "app"},
			tagKey(pulumi): harvest.AttributeTags(pulumi.Observed.Attributes),
			tagKey(plain):  {"team": "data"},
		},
	}
	// The stacks' own listing named the queue already: its reason stays.
	defaults := map[string]string{"aws_sqs_queue queue": "managed by CloudFormation stack app"}

	got := excludeOwned(listed, defaults)

	want := map[string]string{
		"aws_s3_bucket state":       "already managed by Terraform (Terraform elsewhere, per its tag ManagedBy=Terraform; --include-owned imports it)",
		"aws_lambda_function api":   "already managed by Terraform (OpenTofu elsewhere, per its tag managed-by=opentofu; --include-owned imports it)",
		"aws_sqs_queue queue":       "managed by CloudFormation stack app",
		"google_storage_bucket web": "managed by Pulumi (per its tag managed-by=pulumi; --include-owned imports it)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// The report counts them as managed by Terraform or by another tool.
	r := &report.Report{}
	for key, reason := range got {
		r.Excluded = append(r.Excluded, report.Excluded{Type: key, ID: key, Reason: reason})
	}
	r.Finish(nil, nil, false)
	if r.Totals.Managed != 2 || r.Totals.OtherTool != 2 {
		t.Errorf("totals: got %+v, want 2 managed and 2 by another tool", r.Totals)
	}
}
