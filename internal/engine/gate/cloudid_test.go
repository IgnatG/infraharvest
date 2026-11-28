// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gate

import (
	"maps"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

func importing(address, typ string, after map[string]any) *tfjson.ResourceChange {
	return &tfjson.ResourceChange{
		Address: address, Type: typ, Mode: tfjson.ManagedResourceMode,
		Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionNoop}, Importing: &tfjson.Importing{ID: "x"}, After: after},
	}
}

// A resource whose arn argument names another resource has no canonical
// cloud ID of its own: taking it would give two resources one ID.
func TestCloudIDsLeavesOutAnARNOfAnotherResource(t *testing.T) {
	lambdaARN := "arn:aws:lambda:eu-west-1:123456789012:function:worker"
	topicARN := "arn:aws:sns:eu-west-1:123456789012:alerts"
	archiveARN := "arn:aws:events:eu-west-1:123456789012:archive/old"
	p := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		importing("aws_lambda_function.worker", "aws_lambda_function", map[string]any{"arn": lambdaARN}),
		importing("aws_cloudwatch_event_target.worker", "aws_cloudwatch_event_target", map[string]any{"arn": lambdaARN}),
		// The target's type is one the catalogue doesn't know.
		importing("aws_cloudwatch_event_target.stream", "aws_cloudwatch_event_target", map[string]any{"arn": "arn:aws:firehose:eu-west-1:123456789012:deliverystream/audit"}),
		// The catalogue knows the ARN is a function's, so no other type has it.
		importing("aws_lambda_alias.live", "aws_lambda_alias", map[string]any{"arn": lambdaARN}),
		importing("aws_sns_topic.alerts", "aws_sns_topic", map[string]any{"arn": topicARN}),
		// A type the catalogue has, without an ARN shape to compare with.
		importing("aws_cloudwatch_event_archive.old", "aws_cloudwatch_event_archive", map[string]any{"arn": archiveARN}),
		importing("aws_sns_topic_policy.alerts", "aws_sns_topic_policy", map[string]any{"arn": topicARN}),
	}}
	want := map[string]string{
		"aws_lambda_function.worker":       lambdaARN,
		"aws_sns_topic.alerts":             topicARN,
		"aws_cloudwatch_event_archive.old": archiveARN,
	}
	if got := CloudIDs(p); !maps.Equal(got, want) {
		t.Errorf("CloudIDs:\n got %v\nwant %v", got, want)
	}
}

func TestCloudIDs(t *testing.T) {
	bucketARN := "arn:aws:s3:::logs-prod"
	govARN := "arn:aws-us-gov:iam::123456789012:role/deploy"
	armID := "/subscriptions/0000/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/logs"
	tenantID := "/providers/Microsoft.Management/managementGroups/root"
	notImported := importing("aws_s3_bucket.kept", "aws_s3_bucket", map[string]any{"arn": "arn:aws:s3:::kept"})
	notImported.Change.Importing = nil
	data := importing("data.aws_s3_bucket.read", "aws_s3_bucket", map[string]any{"arn": "arn:aws:s3:::read"})
	data.Mode = tfjson.DataResourceMode

	p := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		importing("aws_s3_bucket.logs", "aws_s3_bucket", map[string]any{"id": "logs-prod", "arn": bucketARN}),
		importing(`module.iam.aws_iam_role.this["deploy"]`, "aws_iam_role", map[string]any{"arn": govARN}),
		importing("azurerm_storage_account.logs", "azurerm_storage_account", map[string]any{"id": armID}),
		importing("azurerm_management_group.root", "azurerm_management_group", map[string]any{"id": tenantID}),
		// Not canonical IDs, so left out.
		importing("aws_security_group.web", "aws_security_group", map[string]any{"id": "sg-123", "arn": "sg-123"}),
		importing("azurerm_key_vault_secret.db", "azurerm_key_vault_secret", map[string]any{"id": "https://kv.vault.azure.net/secrets/db/1"}),
		importing("google_storage_bucket.logs", "google_storage_bucket", map[string]any{"id": "logs"}),
		// Unknown until apply, not imported, or not managed: left out.
		importing("aws_s3_bucket.unknown", "aws_s3_bucket", map[string]any{"id": "unknown"}),
		notImported,
		data,
	}}

	want := map[string]string{
		"aws_s3_bucket.logs":                     bucketARN,
		`module.iam.aws_iam_role.this["deploy"]`: govARN,
		"azurerm_storage_account.logs":           armID,
		"azurerm_management_group.root":          tenantID,
	}
	if got := CloudIDs(p); !maps.Equal(got, want) {
		t.Errorf("CloudIDs:\n got %v\nwant %v", got, want)
	}
	if got := CloudIDs(nil); len(got) != 0 {
		t.Errorf("CloudIDs(nil) = %v, want none", got)
	}
}
