// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// keptOwned keeps, by type and import ID, only the selected resources
// whose listed tags name another tool and that have an import ID: an
// untagged or unowned resource needs no keeping, and one without an import
// ID can't be imported anyway.
func TestKeptOwned(t *testing.T) {
	t.Parallel()
	queue := harvest.RecordBare("q-1", "orders", "aws_sqs_queue", "aws")
	bucket := harvest.RecordBare("b-1", "logs", "aws_s3_bucket", "aws")
	topic := harvest.RecordBare("t-1", "alerts", "aws_sns_topic", "aws")
	noID := harvest.RecordBare("n-1", "", "aws_sqs_queue", "aws")
	// importID gives every resource but noID its listed ID, prefixed so
	// the result shows it is the import ID rather than the listed one.
	importID := func(r harvest.Resource) (string, bool) {
		if r.Observed.ID == "n-1" {
			return "", false
		}
		return "id:" + r.Observed.ID, true
	}
	// listed is a listing of the tags of each resource.
	listed := func(tags map[harvest.Resource]map[string]string) listing {
		l := listing{tags: map[string]map[string]string{}}
		for r, t := range tags {
			l.tags[tagKey(r)] = t
		}
		return l
	}
	for _, tc := range []struct {
		name     string
		selected map[string][]harvest.Resource
		listed   listing
		want     map[string]bool
	}{
		{name: "nothing selected", listed: listed(map[harvest.Resource]map[string]string{queue: {"ManagedBy": "terraform"}})},
		{
			name:     "no tags listed",
			selected: map[string][]harvest.Resource{"sqs": {queue}},
			listed:   listing{},
		},
		{
			name:     "tags name no tool",
			selected: map[string][]harvest.Resource{"sqs": {queue}},
			listed:   listed(map[harvest.Resource]map[string]string{queue: {"env": "prod", "ManagedBy": "the platform team"}}),
		},
		{
			name:     "owned but no import ID",
			selected: map[string][]harvest.Resource{"sqs": {noID}},
			listed:   listed(map[harvest.Resource]map[string]string{noID: {"ManagedBy": "pulumi"}}),
		},
		{
			name: "owned across services",
			selected: map[string][]harvest.Resource{
				"sqs": {queue},
				"s3":  {bucket},
				"sns": {topic},
			},
			listed: listed(map[harvest.Resource]map[string]string{
				queue:  {"managed-by": "Terraform"},
				bucket: {"aws:cloudformation:stack-name": "logs-stack"},
				topic:  {"team": "alerts"},
			}),
			want: map[string]bool{"aws_sqs_queue id:q-1": true, "aws_s3_bucket id:b-1": true},
		},
		{
			// Tags are keyed by type and listed ID: a resource of another
			// type with the same ID doesn't borrow them.
			name:     "tags of another type",
			selected: map[string][]harvest.Resource{"s3": {harvest.RecordBare("q-1", "", "aws_s3_bucket", "aws")}},
			listed:   listed(map[harvest.Resource]map[string]string{queue: {"ManagedBy": "terraform"}}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := keptOwned(tc.selected, tc.listed, importID)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("keptOwned = %v, want %v", got, tc.want)
			}
		})
	}
}

// ownedByTags gives the reason and owner the selection gives an owned
// resource, so the report of an import tells Terraform states, stacks and
// other tools apart, and says how to import it anyway.
func TestOwnedByTags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		owner      engine.Owner
		wantPrefix string
		wantOwner  string
		wantIn     string
	}{
		{"terraform", engine.Owner{Tool: "Terraform", Tag: "ManagedBy=terraform"}, report.TerraformPrefix, report.OwnerTerraformTag, "ManagedBy=terraform"},
		{"opentofu", engine.Owner{Tool: "OpenTofu", Tag: "managed_by=opentofu"}, report.TerraformPrefix, report.OwnerTerraformTag, "OpenTofu elsewhere"},
		{"cloudformation", engine.Owner{Tool: "CloudFormation", Stack: "net", Tag: "aws:cloudformation:stack-name=net"}, report.OtherToolPrefix + "CloudFormation stack net", report.OwnerCloudFormation, "aws:cloudformation:stack-name=net"},
		{"other tool", engine.Owner{Tool: "Pulumi", Tag: "ManagedBy=pulumi"}, report.OtherToolPrefix + "Pulumi", report.OwnerOtherTool, "ManagedBy=pulumi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var r report.Report
			r.Exclude("aws/123/eu-west-2", "aws_sqs_queue", "q-1", ownedByTags(tc.owner))
			if len(r.Excluded) != 1 {
				t.Fatalf("excluded %v, want one", r.Excluded)
			}
			got := r.Excluded[0]
			if !strings.HasPrefix(got.Reason, tc.wantPrefix) || !strings.Contains(got.Reason, tc.wantIn) || !strings.Contains(got.Reason, "--include-owned") {
				t.Errorf("reason %q, want it to start with %q and name %q and --include-owned", got.Reason, tc.wantPrefix, tc.wantIn)
			}
			if got.Owner != tc.wantOwner {
				t.Errorf("owner %q, want %q", got.Owner, tc.wantOwner)
			}
		})
	}
}
