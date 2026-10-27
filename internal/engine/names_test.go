// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"reflect"
	"testing"
)

// labelled is relabelled for a root generated for the first time.
func labelled(imports []Import, taken Names, tidy bool) []Import {
	return relabelled(imports, taken, tidy, nil)
}

func TestLabel(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"vpc-7fe7f292":               "vpc_7fe7f292",
		"/infraharvest-e2e/endpoint": "infraharvest_e2e_endpoint",
		"Orders Queue":               "orders_queue",
		"2024-backups":               "r_2024_backups",
		"--":                         "resource",
		// Words of camelCase names stay apart; before, these ran together
		// (allowhttpapi).
		"AllowHttpApi":   "allow_http_api",
		"HTTPApiGateway": "http_api_gateway",
		"Ipv4Address":    "ipv4_address",
		"Z1D633PJN98FT9": "z1d633pjn98ft9",
		// A parent's name before a child's that starts with it is said once.
		"shop_shop-web":                             "shop_web",
		"ih-sbx-terraform_ih-sbx-terraform-web":     "ih_sbx_terraform_web",
		"$default $default":                         "default",
		"web 10.0.0.10":                             "web_10_0_0_10",
		"web_10_0_0_0":                              "web_10_0_0_0",
		"api.ih-sbx.internal api.ih-sbx.internal A": "api_ih_sbx_internal_a",
	} {
		if got := Label(name); got != want {
			t.Errorf("Label(%q): got %q, want %q", name, got, want)
		}
	}
}

func TestLabelledIsUniqueAndStable(t *testing.T) {
	t.Parallel()
	imports := []Import{
		{Type: "aws_vpc", Name: "main", ID: "vpc-2"},
		{Type: "aws_vpc", Name: "main_2", ID: "vpc-9"},
		{Type: "aws_vpc", Name: "Main", ID: "vpc-1"},
		{Type: "aws_subnet", Name: "main", ID: "subnet-1"},
	}
	want := []Import{
		{Type: "aws_subnet", Name: "main", ID: "subnet-1"},
		{Type: "aws_vpc", Name: "main", ID: "vpc-1"},
		{Type: "aws_vpc", Name: "main_3", ID: "vpc-2"}, // main_2 is another resource's own label
		{Type: "aws_vpc", Name: "main_2", ID: "vpc-9"},
	}

	got := labelled(imports, nil, false)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// Discovery order must not change the labels.
	reversed := []Import{imports[3], imports[2], imports[1], imports[0]}
	if again := labelled(reversed, nil, false); !reflect.DeepEqual(again, want) {
		t.Errorf("reordered input: got %+v, want %+v", again, want)
	}
}

func TestWithoutTypeWords(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ resourceType, label, want string }{
		{"aws_sqs_queue", "orders_queue", "orders"},
		{"aws_sqs_queue", "sqs_queue_orders", "orders"},
		{"aws_ecs_task_definition", "task_definition_web", "web"},
		{"aws_kms_alias", "alias_shop_app", "shop_app"},
		{"azurerm_subnet_route_table_association", "app_route_table_association", "app"},
		{"aws_s3_bucket", "logs", "logs"},
		// Nothing else would be left, or the label would start with a digit.
		{"aws_sqs_queue", "queue", "queue"},
		{"aws_sqs_queue", "queue_2024", "queue_2024"},
		// Words of the type in the middle stay.
		{"aws_iam_role", "app_role_admin", "app_role_admin"},
	} {
		if got := withoutTypeWords(tc.resourceType, tc.label); got != tc.want {
			t.Errorf("withoutTypeWords(%q, %q): got %q, want %q", tc.resourceType, tc.label, got, tc.want)
		}
	}
}

func TestSharedPrefix(t *testing.T) {
	t.Parallel()
	names := func(labels ...string) []Import {
		imports := make([]Import, len(labels))
		for i, label := range labels {
			imports[i] = Import{Type: "aws_sqs_queue", Name: label}
		}
		return imports
	}
	for _, tc := range []struct {
		imports []Import
		want    string
	}{
		{names("shop_prod_web", "shop_prod_api", "logs"), "shop_prod_"},
		{names("shop_prod_web", "shop_dev_api", "shop_logs"), "shop_"},
		// Half isn't most.
		{names("shop_web", "shop_api", "logs", "jobs"), ""},
		// One resource shares nothing.
		{names("shop_web"), ""},
		// A label that is the prefix alone doesn't count.
		{names("shop", "shop", "shop_web"), ""},
	} {
		if got := sharedPrefix(tc.imports); got != tc.want {
			t.Errorf("sharedPrefix(%+v): got %q, want %q", tc.imports, got, tc.want)
		}
	}
}

// Tidied labels drop the words of their type and the prefix most of the
// root's names share, and a type with one resource is labelled main.
// Before, the sandbox's labels read like
// aws_ecs_task_definition.task_definition_ih_sbx_terraform_web.
func TestLabelledTidy(t *testing.T) {
	t.Parallel()
	imports := []Import{
		{Type: "aws_sqs_queue", Name: "ih-sbx-terraform-jobs", ID: "q1"},
		{Type: "aws_sqs_queue", Name: "ih-sbx-terraform-second-queue", ID: "q2"},
		{Type: "aws_ecs_task_definition", Name: "task-definition/ih-sbx-terraform-web", ID: "td"},
		{Type: "aws_ecs_service", Name: "ih-sbx-terraform_ih-sbx-terraform-web", ID: "svc1"},
		{Type: "aws_ecs_service", Name: "ih-sbx-terraform_ih-sbx-terraform-api", ID: "svc2"},
		{Type: "aws_ecs_cluster", Name: "ih-sbx-terraform", ID: "cluster"},
		// Stripped, the second log group would take the first one's label,
		// web, so it keeps the prefix.
		{Type: "aws_cloudwatch_log_group", Name: "web", ID: "/web"},
		{Type: "aws_cloudwatch_log_group", Name: "ih-sbx-terraform-web", ID: "/ih-sbx-terraform/web"},
		// A remainder starting with a digit keeps the prefix.
		{Type: "aws_cloudwatch_log_group", Name: "ih-sbx-terraform-2024", ID: "/2024"},
	}
	want := []Import{
		{Type: "aws_cloudwatch_log_group", Name: "ih_sbx_terraform_2024", ID: "/2024"},
		{Type: "aws_cloudwatch_log_group", Name: "ih_sbx_terraform_web", ID: "/ih-sbx-terraform/web"},
		{Type: "aws_cloudwatch_log_group", Name: "web", ID: "/web"},
		{Type: "aws_ecs_cluster", Name: "main", ID: "cluster"},
		{Type: "aws_ecs_service", Name: "api", ID: "svc2"},
		{Type: "aws_ecs_service", Name: "web", ID: "svc1"},
		{Type: "aws_ecs_task_definition", Name: "main", ID: "td"},
		{Type: "aws_sqs_queue", Name: "jobs", ID: "q1"},
		{Type: "aws_sqs_queue", Name: "second", ID: "q2"},
	}
	if got := labelled(imports, nil, true); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v,\nwant %+v", got, want)
	}
	reversed := make([]Import, len(imports))
	for i, imp := range imports {
		reversed[len(imports)-1-i] = imp
	}
	if got := labelled(reversed, nil, true); !reflect.DeepEqual(got, want) {
		t.Errorf("reordered input: got %+v,\nwant %+v", got, want)
	}
}

// What an incremental import adds is labelled main only if the root has no
// resource of its type, and never takes a label the root has.
func TestLabelledTidyKeepsTaken(t *testing.T) {
	t.Parallel()
	imports := []Import{
		{Type: "aws_sqs_queue", Name: "shop-jobs", ID: "q1"},
		{Type: "aws_sqs_queue", Name: "shop-orders", ID: "q2"},
		{Type: "aws_s3_bucket", Name: "shop-logs", ID: "b1"},
		{Type: "aws_sns_topic", Name: "shop-events", ID: "t1"},
	}
	taken := Names{"aws_sqs_queue.jobs": true, "aws_s3_bucket.main": true, "data.aws_sns_topic.x": true}
	want := []Import{
		{Type: "aws_s3_bucket", Name: "logs", ID: "b1"},
		{Type: "aws_sns_topic", Name: "main", ID: "t1"},
		{Type: "aws_sqs_queue", Name: "orders", ID: "q2"},
		{Type: "aws_sqs_queue", Name: "shop_jobs", ID: "q1"},
	}
	if got := labelled(imports, taken, true); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v,\nwant %+v", got, want)
	}
}

// A module call around the only resource of its type is named after the
// type: module "main" would say nothing, and a root with a VPC and a bucket
// module would call them main and main_2.
func TestCallName(t *testing.T) {
	t.Parallel()
	for address, want := range map[string]string{
		"aws_vpc.main":       "vpc",
		"aws_s3_bucket.main": "s3_bucket",
		"aws_s3_bucket.logs": "logs",
		"aws_vpc.main_2":     "main_2",
	} {
		if got := callName(address); got != want {
			t.Errorf("callName(%q): got %q, want %q", address, got, want)
		}
	}
}
