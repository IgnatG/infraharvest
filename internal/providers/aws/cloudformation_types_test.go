// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// Every CloudFormation type of the table, or custom resource type of the
// CDK, names its Terraform types.
func TestCloudFormationTypesAreWellFormed(t *testing.T) {
	cfn := regexp.MustCompile(`^(AWS::[A-Za-z0-9]+::[A-Za-z0-9]+|Custom::AWSCDK-[A-Za-z0-9-]+)$`)
	for typ, terraform := range cloudFormationTypes {
		if !cfn.MatchString(typ) {
			t.Errorf("%q is not a CloudFormation type", typ)
		}
		if len(terraform) == 0 {
			t.Errorf("%s names no Terraform type", typ)
		}
		for _, tf := range terraform {
			if !strings.HasPrefix(tf, "aws_") {
				t.Errorf("%s: %q is not an AWS Terraform type", typ, tf)
			}
		}
	}
}

// The catalogue's cloud_control entries add their CloudFormation types.
func TestStackTypesAddCloudControlTypes(t *testing.T) {
	types := stackTypes(awsCatalogue())
	if got := types["AWS::Athena::WorkGroup"]; !reflect.DeepEqual(got, []string{"aws_athena_workgroup"}) {
		t.Errorf("AWS::Athena::WorkGroup: %v, want aws_athena_workgroup", got)
	}
	if got := types["AWS::IAM::Role"]; !reflect.DeepEqual(got, []string{"aws_iam_role"}) {
		t.Errorf("AWS::IAM::Role: %v, want aws_iam_role", got)
	}
}

// A stack's resource leaves out what has its physical ID only when the
// type of what is listed is one its CloudFormation type can be listed as,
// or a part of one under the same ID, such as a bucket's policy or
// versioning; a CloudFormation type the table doesn't know, such as a
// custom resource's, leaves out whatever has its physical ID. Where a
// stack has two resources with one physical ID, each leaves out its own.
func TestExcludedByDefaultCloudFormationByType(t *testing.T) {
	const ns = `xmlns="http://cloudformation.amazonaws.com/doc/2010-05-15/"`
	member := func(logical, physical, typ string) string {
		return `<member><LogicalResourceId>` + logical + `</LogicalResourceId><PhysicalResourceId>` + physical +
			`</PhysicalResourceId><ResourceType>` + typ + `</ResourceType><ResourceStatus>CREATE_COMPLETE</ResourceStatus>` +
			`<LastUpdatedTimestamp>2026-01-01T00:00:00Z</LastUpdatedTimestamp></member>`
	}
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListStacks":
			return `<ListStacksResponse ` + ns + `><ListStacksResult><StackSummaries>` +
				`<member><StackId>arn:aws:cloudformation:us-east-1:123456789012:stack/app/1</StackId><StackName>app</StackName><StackStatus>CREATE_COMPLETE</StackStatus><CreationTime>2026-01-01T00:00:00Z</CreationTime></member>` +
				`</StackSummaries></ListStacksResult></ListStacksResponse>`
		case "ListStackResources":
			return `<ListStackResourcesResponse ` + ns + `><ListStackResourcesResult><StackResourceSummaries>` +
				member("OrdersTable", "orders", "AWS::DynamoDB::Table") +
				member("Assets", "assets", "AWS::S3::Bucket") +
				member("Widget", "widget", "Custom::Widget") +
				member("Analytics", "analytics", "AWS::Athena::WorkGroup") +
				member("WebRole", "web", "AWS::IAM::Role") +
				member("WebProfile", "web", "AWS::IAM::InstanceProfile") +
				`</StackResourceSummaries></ListStackResourcesResult></ListStackResourcesResponse>`
		}
		return recordUnexpected(t, call)
	})
	p := &Provider{region: "us-east-1"}

	excluded, err := p.ExcludedByDefault(t.Context(), []harvest.Resource{
		harvest.RecordBare("orders", "orders", "aws_dynamodb_table", "aws"),
		// A hand-made role and instance profile named as the stack's table.
		harvest.RecordBare("orders", "orders", "aws_iam_role", "aws"),
		harvest.RecordBare("orders", "orders", "aws_iam_instance_profile", "aws"),
		harvest.RecordBare("assets", "assets", "aws_s3_bucket", "aws"),
		harvest.RecordBare("assets", "assets", "aws_s3_bucket_policy", "aws"),
		harvest.RecordBare("assets", "assets", "aws_s3_bucket_versioning", "aws"),
		harvest.RecordBare("assets", "assets", "aws_glue_job", "aws"),
		harvest.RecordBare("widget", "widget", "aws_lambda_function", "aws"),
		harvest.RecordBare("analytics", "analytics", "aws_athena_workgroup", "aws"),
		harvest.RecordBare("analytics", "analytics", "aws_glue_crawler", "aws"),
		harvest.RecordBare("web", "web", "aws_iam_role", "aws"),
		harvest.RecordBare("web", "web", "aws_iam_instance_profile", "aws"),
		harvest.RecordBare("web", "web", "aws_iam_user", "aws"),
	})
	if err != nil {
		t.Fatal(err)
	}

	stack := reasonCloudFormation("app")
	want := map[string]string{
		"aws_dynamodb_table orders":       stack,
		"aws_s3_bucket assets":            stack,
		"aws_s3_bucket_policy assets":     stack,
		"aws_s3_bucket_versioning assets": stack,
		"aws_lambda_function widget":      stack,
		"aws_athena_workgroup analytics":  stack,
		"aws_iam_role web":                stack,
		"aws_iam_instance_profile web":    stack,
	}
	if !reflect.DeepEqual(excluded, want) {
		t.Errorf("got %v, want %v", excluded, want)
	}
}
