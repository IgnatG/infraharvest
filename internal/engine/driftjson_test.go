// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

func TestEquivalentJSON(t *testing.T) {
	t.Parallel()
	policy := `{"Version":"2012-10-17","Statement":[{"Sid":"Read","Effect":"Allow","Action":["s3:GetObject","s3:ListBucket"],"Resource":["arn:aws:s3:::b","arn:aws:s3:::b/*"]},{"Effect":"Deny","Action":"sqs:ReceiveMessage","Resource":"*"}]}`
	tests := []struct {
		name          string
		before, after any
		want          bool
	}{
		{"reindented, keys reordered", `{"a": 1, "b": [1, 2]}`, "{\n  \"b\": [1, 2],\n  \"a\": 1.0\n}", true},
		{"a list keeps its order", `{"a": [1, 2]}`, `{"a": [2, 1]}`, false},
		{"a value changed", `{"a": 1}`, `{"a": 2}`, false},
		{"not JSON", `{"a": 1`, `{"a": 1}`, false},
		{"scalars stay strings", `1`, `1.0`, false},
		{"not strings", map[string]any{"a": 1.0}, map[string]any{"a": 1.0}, false},
		{"the policy reformatted", policy, `{
  "Statement": [
    {"Resource": "*", "Action": ["sqs:ReceiveMessage"], "Effect": "Deny", "Sid": ""},
    {"Resource": ["arn:aws:s3:::b/*", "arn:aws:s3:::b"], "Action": ["s3:ListBucket", "s3:GetObject", "s3:GetObject"], "Effect": "Allow", "Sid": "Read"}
  ],
  "Version": "2012-10-17"
}`, true},
		{"a single statement as an object", `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}}`, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["*"]}]}`, true},
		{"principals and conditions", `{"Statement":[{"Effect":"Allow","Principal":"*","Action":"sts:AssumeRole","Condition":{"StringEquals":{"aws:PrincipalOrgID":"o-1"},"Bool":{"aws:SecureTransport":true}}}]}`, `{"Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["sts:AssumeRole"],"Condition":{"Bool":{"aws:SecureTransport":["true"]},"StringEquals":{"aws:PrincipalOrgID":["o-1"]}}}]}`, true},
		{"the principals in another order", `{"Statement":[{"Effect":"Allow","Principal":{"AWS":["arn:aws:iam::111111111111:root","arn:aws:iam::222222222222:root"]},"Action":"sts:AssumeRole"}]}`, `{"Statement":[{"Effect":"Allow","Principal":{"AWS":["arn:aws:iam::222222222222:root","arn:aws:iam::111111111111:root"]},"Action":"sts:AssumeRole"}]}`, true},
		{"an action added", policy, `{"Version":"2012-10-17","Statement":[{"Sid":"Read","Effect":"Allow","Action":["s3:GetObject","s3:ListBucket","s3:PutObject"],"Resource":["arn:aws:s3:::b","arn:aws:s3:::b/*"]},{"Effect":"Deny","Action":"sqs:ReceiveMessage","Resource":"*"}]}`, false},
		{"an effect changed", `{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`, `{"Statement":[{"Effect":"Deny","Action":"s3:GetObject","Resource":"*"}]}`, false},
		{"a statement removed", policy, `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"sqs:ReceiveMessage","Resource":"*"}]}`, false},
		{"a Sid changed", `{"Statement":[{"Sid":"A","Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`, `{"Statement":[{"Sid":"B","Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := equivalentJSON(tt.before, tt.after); got != tt.want {
				t.Errorf("equivalentJSON(%v, %v) = %v, want %v", tt.before, tt.after, got, tt.want)
			}
			if got := equivalentJSON(tt.after, tt.before); got != tt.want {
				t.Errorf("equivalentJSON(%v, %v) = %v, want %v", tt.after, tt.before, got, tt.want)
			}
		})
	}
}

// A policy the cloud gives back reformatted is not drift; one it changed
// is, as the whole attribute.
func TestDriftComparesPoliciesAsDocuments(t *testing.T) {
	t.Parallel()
	code := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:SendMessage"],"Resource":"*"}]}`
	reformatted := "{\n  \"Statement\": [\n    {\n      \"Resource\": \"*\",\n      \"Action\": \"sqs:SendMessage\",\n      \"Effect\": \"Allow\"\n    }\n  ],\n  \"Version\": \"2012-10-17\"\n}"
	changed := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:SendMessage","sqs:DeleteMessage"],"Resource":"*"}]}`
	refresh := func(policy string) *tfjson.Plan {
		return &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{{
			Address: "aws_sqs_queue_policy.jobs",
			Mode:    tfjson.ManagedResourceMode,
			Type:    "aws_sqs_queue_policy",
			Change: &tfjson.Change{
				Actions: tfjson.Actions{tfjson.ActionUpdate},
				Before:  map[string]any{"queue_url": "https://sqs/jobs", "policy": code},
				After:   map[string]any{"queue_url": "https://sqs/jobs", "policy": policy},
			},
		}}}
	}

	if got := classifyDrift(refresh(reformatted), nil, nil, false); len(got) != 0 {
		t.Errorf("a reformatted policy: changes %+v, want none", got)
	}
	got := classifyDrift(refresh(changed), nil, nil, false)
	if len(got) != 1 || got[0].Attribute != "policy" || got[0].Kind != DriftChanged {
		t.Errorf("a changed policy: changes %+v, want one of policy", got)
	}
}
