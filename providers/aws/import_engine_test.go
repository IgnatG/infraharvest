// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/IgnatG/infraharvest/terraformutils"
)

func TestImportID(t *testing.T) {
	tests := []struct {
		resource terraformutils.Resource
		wantID   string
		wantOK   bool
	}{
		{terraformutils.NewSimpleResource("https://sqs/1/jobs", "jobs", "aws_sqs_queue", "aws", nil), "https://sqs/1/jobs", true},
		{terraformutils.NewResource("rtbassoc-1", "subnet-1", "aws_route_table_association", "aws",
			map[string]string{"subnet_id": "subnet-1", "route_table_id": "rtb-1"}, nil, nil), "subnet-1/rtb-1", true},
		{terraformutils.NewResource("rtbassoc-2", "vpc-1", "aws_main_route_table_association", "aws",
			map[string]string{"vpc_id": "vpc-1", "route_table_id": "rtb-1"}, nil, nil), "", false},
	}
	for _, tt := range tests {
		id, ok := AWSProvider{}.ImportID(tt.resource)
		if id != tt.wantID || ok != tt.wantOK {
			t.Errorf("%s: got %q, %v; want %q, %v", tt.resource.InstanceInfo.Type, id, ok, tt.wantID, tt.wantOK)
		}
	}
}

// Trimmed from what Terraform 1.16 and the AWS provider 6.67 generate.
const generatedConfig = `resource "aws_route_table" "public" {
  route = [{
    cidr_block      = "0.0.0.0/0"
    gateway_id      = "igw-1"
    ipv6_cidr_block = ""
  }]
  vpc_id = "vpc-1"
}

resource "aws_sqs_queue" "jobs" {
  name   = "jobs"
  policy = ""
}
`

const fixedConfig = `resource "aws_route_table" "public" {
  route = [{
    cidr_block      = "0.0.0.0/0"
    gateway_id      = "igw-1"
    ipv6_cidr_block = null
  }]
  vpc_id = "vpc-1"
}

resource "aws_sqs_queue" "jobs" {
  name   = "jobs"
  policy = ""
}
`

func TestFixGeneratedConfig(t *testing.T) {
	f, diags := hclwrite.ParseConfig([]byte(generatedConfig), "generated.tf", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	var fixed []string
	for _, block := range f.Body().Blocks() {
		if (AWSProvider{}).FixGeneratedConfig(block.Labels()[0], block.Body()) {
			fixed = append(fixed, block.Labels()[1])
		}
	}

	if got := string(hclwrite.Format(f.Bytes())); got != fixedConfig {
		t.Errorf("got:\n%s\nwant:\n%s", got, fixedConfig)
	}
	if len(fixed) != 1 || fixed[0] != "public" {
		t.Errorf("reported fixed blocks %v, want [public]", fixed)
	}
}
