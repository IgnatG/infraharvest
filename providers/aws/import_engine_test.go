// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"

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
		{terraformutils.NewSimpleResource("arn:aws:ecs:us-east-1:1:cluster/apps", "apps", "aws_ecs_cluster", "aws", nil), "apps", true},
		{terraformutils.NewResource("arn:aws:ecs:us-east-1:1:service/apps/web", "apps_web", "aws_ecs_service", "aws",
			map[string]string{"cluster": "apps", "name": "web"}, nil, nil), "apps/web", true},
		{terraformutils.NewResource("AllowSNS", "AllowSNS", "aws_lambda_permission", "aws",
			map[string]string{"function_name": "arn:aws:lambda:us-east-1:1:function:f", "statement_id": "AllowSNS"}, nil, nil), "arn:aws:lambda:us-east-1:1:function:f/AllowSNS", true},
		{terraformutils.NewResource("vai-1", "i-1:/dev/sdh", "aws_volume_attachment", "aws",
			map[string]string{"device_name": "/dev/sdh", "volume_id": "vol-1", "instance_id": "i-1"}, nil, nil), "/dev/sdh:vol-1:i-1", true},
	}
	for _, tt := range tests {
		id, ok := AWSProvider{}.ImportID(tt.resource)
		if id != tt.wantID || ok != tt.wantOK {
			t.Errorf("%s: got %q, %v; want %q, %v", tt.resource.InstanceInfo.Type, id, ok, tt.wantID, tt.wantOK)
		}
	}
}
