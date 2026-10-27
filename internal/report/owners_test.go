// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestOwnerOf(t *testing.T) {
	t.Parallel()
	for reason, want := range map[string]string{
		"":                                  "",
		TerraformPrefix + " (prod.tfstate)": OwnerTerraformState,
		TerraformPrefix + " (backend)":      OwnerTerraformState,
		TerraformPrefix + " (Terraform elsewhere, per its tag ManagedBy=terraform; --include-owned imports it)": OwnerTerraformTag,
		"managed by CloudFormation stack app": OwnerCloudFormation,
		"managed by CloudFormation stack app (per its tag aws:cloudformation:stack-name; --include-owned imports it)": OwnerCloudFormation,
		"managed by Pulumi (per its tag ManagedBy=pulumi; --include-owned imports it)":                                OwnerOtherTool,
		"default VPC, which AWS creates in every region":                                                              OwnerCloud,
		"shared by account 111122223333 through AWS RAM, which manages it":                                            OwnerCloud,
		ReasonSelectionEntry:    OwnerSelection,
		ReasonSelectionRule:     OwnerSelection,
		ReasonSelectionDefaults: OwnerSelection,
	} {
		if got := OwnerOf(reason); got != want {
			t.Errorf("%q: got %q, want %q", reason, got, want)
		}
	}
}

func TestDiscovery(t *testing.T) {
	t.Parallel()
	const prod, dev = "aws/1/eu-west-2", "aws/2/eu-west-2"
	d := NewDiscovery("audit.yaml", []string{"team", "team"})
	d.Add(prod, "aws_s3_bucket", "", map[string]string{"team": "web"})
	d.Add(prod, "aws_s3_bucket", TerraformPrefix+" (prod.tfstate)", map[string]string{"team": "web"})
	d.Add(prod, "aws_s3_bucket", TerraformPrefix+" (Terraform elsewhere, per its tag ManagedBy=terraform; --include-owned imports it)", nil)
	d.Add(prod, "aws_s3_bucket", "managed by CloudFormation stack app", nil)
	d.Add(dev, "aws_s3_bucket", "managed by Pulumi (per its tag ManagedBy=pulumi; --include-owned imports it)", nil)
	d.Add(dev, "aws_vpc", "default VPC", map[string]string{"team": "net"})
	d.Failures = []string{"service iam: denied"}

	var out bytes.Buffer
	if err := d.WriteJSON(&out); err != nil {
		t.Fatal(err)
	}
	var got Discovery
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	want := Discovery{
		SchemaVersion:  SchemaVersion,
		Selection:      "audit.yaml",
		DiscoveryCount: DiscoveryCount{Listed: 6, Included: 1, Excluded: 5, TerraformState: 1, TerraformTag: 1, CloudFormation: 1, OtherTool: 1, Cloud: 1},
		Types: []DiscoveryType{
			{Type: "aws_s3_bucket", DiscoveryCount: DiscoveryCount{Listed: 5, Included: 1, Excluded: 4, TerraformState: 1, TerraformTag: 1, CloudFormation: 1, OtherTool: 1}},
			{Type: "aws_vpc", DiscoveryCount: DiscoveryCount{Listed: 1, Excluded: 1, Cloud: 1}},
		},
		Scopes: []DiscoveryScope{
			{Scope: prod, DiscoveryCount: DiscoveryCount{Listed: 4, Included: 1, Excluded: 3, TerraformState: 1, TerraformTag: 1, CloudFormation: 1}},
			{Scope: dev, DiscoveryCount: DiscoveryCount{Listed: 2, Excluded: 2, OtherTool: 1, Cloud: 1}},
		},
		Tags: []DiscoveryTag{
			{Key: "team", Value: "", DiscoveryCount: DiscoveryCount{Listed: 3, Excluded: 3, TerraformTag: 1, CloudFormation: 1, OtherTool: 1}},
			{Key: "team", Value: "net", DiscoveryCount: DiscoveryCount{Listed: 1, Excluded: 1, Cloud: 1}},
			{Key: "team", Value: "web", DiscoveryCount: DiscoveryCount{Listed: 2, Included: 1, Excluded: 1, TerraformState: 1}},
		},
		Failures: []string{"service iam: denied"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}
