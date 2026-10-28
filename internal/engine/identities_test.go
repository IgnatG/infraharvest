// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

const (
	keyID  = "1234abcd-12ab-34cd-56ef-1234567890ab"
	zoneID = "Z0123456789ABCDEFGHIJ"
)

// shopValues are the imported values of an API, its functions and what
// they use, by address.
var shopValues = map[string]map[string]any{
	"aws_api_gateway_rest_api.shop": {
		"id": "okoipb1lhk", "name": "shop", "root_resource_id": "s8kkpl3b2c",
		"arn":           "arn:aws:apigateway:eu-west-2::/restapis/okoipb1lhk",
		"execution_arn": "arn:aws:execute-api:eu-west-2:123456789012:okoipb1lhk",
	},
	"aws_api_gateway_resource.orders":       {"id": "a1b2c3", "parent_id": "s8kkpl3b2c", "path_part": "orders", "rest_api_id": "okoipb1lhk"},
	"aws_api_gateway_integration.orders":    {"id": "agi-okoipb1lhk-a1b2c3-GET", "cache_namespace": "a1b2c3", "resource_id": "a1b2c3"},
	"aws_api_gateway_method.root":           {"id": "agm-okoipb1lhk-s8kkpl3b2c-GET", "resource_id": "s8kkpl3b2c"},
	"aws_apigatewayv2_api.http":             {"id": "x7k2m9p4q1", "name": "http", "execution_arn": "arn:aws:execute-api:eu-west-2:123456789012:x7k2m9p4q1"},
	"aws_apigatewayv2_integration.orders":   {"id": "a9b8c7d", "api_id": "x7k2m9p4q1"},
	"aws_apigatewayv2_route.get_orders":     {"id": "r5t6y7u", "api_id": "x7k2m9p4q1", "target": "integrations/a9b8c7d"},
	"aws_lambda_permission.http":            {"id": "AllowHTTP", "function_name": "fn"},
	"aws_lambda_function.fn":                {"id": "fn", "function_name": "fn", "arn": "arn:aws:lambda:eu-west-2:123456789012:function:fn"},
	"aws_cloudwatch_log_group.fn":           {"id": "/aws/lambda/fn", "name": "/aws/lambda/fn", "arn": "arn:aws:logs:eu-west-2:123456789012:log-group:/aws/lambda/fn"},
	"aws_sqs_queue.orders":                  {"id": "https://sqs.eu-west-2.amazonaws.com/123456789012/orders", "name": "orders", "arn": "arn:aws:sqs:eu-west-2:123456789012:orders"},
	"aws_sns_topic.orders":                  {"id": "arn:aws:sns:eu-west-2:123456789012:orders", "name": "orders", "arn": "arn:aws:sns:eu-west-2:123456789012:orders"},
	"aws_cloudwatch_metric_alarm.depth":     {"id": "depth", "alarm_name": "depth"},
	"aws_kms_key.main":                      {"id": keyID, "key_id": keyID, "arn": "arn:aws:kms:eu-west-2:123456789012:key/" + keyID},
	"aws_kms_alias.main":                    {"id": "alias/main", "name": "alias/main", "target_key_id": keyID},
	"aws_route53_zone.shop":                 {"id": zoneID, "zone_id": zoneID, "name": "shop.example.com", "arn": "arn:aws:route53:::hostedzone/" + zoneID},
	"aws_route53_record.www":                {"id": zoneID + "_www.shop.example.com_A", "zone_id": zoneID, "name": "www.shop.example.com"},
	"aws_ecs_task_definition.web":           {"id": "web", "family": "web", "revision": float64(3), "arn": "arn:aws:ecs:eu-west-2:123456789012:task-definition/web:3"},
	"aws_ecs_service.web":                   {"id": "arn:aws:ecs:eu-west-2:123456789012:service/main/web", "name": "web", "task_definition": "web:3"},
	"aws_ebs_encryption_by_default.default": {"id": "eu-west-2", "region": "eu-west-2", "enabled": true},
}

const shopGenerated = `resource "aws_api_gateway_resource" "orders" {
  parent_id   = "s8kkpl3b2c"
  path_part   = "orders"
  rest_api_id = "okoipb1lhk"
}

resource "aws_api_gateway_integration" "orders" {
  cache_namespace = "a1b2c3"
  resource_id     = "a1b2c3"
  rest_api_id     = "okoipb1lhk"
}

resource "aws_api_gateway_method" "root" {
  resource_id = "s8kkpl3b2c"
  rest_api_id = "okoipb1lhk"
}

resource "aws_apigatewayv2_route" "get_orders" {
  api_id = "x7k2m9p4q1"
  target = "integrations/a9b8c7d"
}

resource "aws_lambda_permission" "http" {
  function_name = "fn"
  source_arn    = "arn:aws:execute-api:eu-west-2:123456789012:x7k2m9p4q1/*/*"
}

resource "aws_lambda_function" "fn" {
  function_name = "fn"
  logging_config {
    log_format = "Text"
    log_group  = "/aws/lambda/fn"
  }
}

resource "aws_cloudwatch_metric_alarm" "depth" {
  alarm_actions = ["arn:aws:sns:eu-west-2:123456789012:orders"]
  alarm_name    = "depth"
  description   = "Depth of orders, see arn:aws:sqs:eu-west-2:123456789012:orders"
  dimensions = {
    QueueName = "orders"
  }
  tags = {
    Queue = "orders"
  }
}

resource "aws_kms_key" "main" {
  description = "main"
}

resource "aws_kms_alias" "main" {
  name          = "alias/main"
  target_key_id = "` + keyID + `"
}

resource "aws_route53_record" "www" {
  name    = "www.shop.example.com"
  zone_id = "` + zoneID + `"
}

resource "aws_ecs_service" "web" {
  name            = "web"
  task_definition = "web:3"
}

resource "aws_sns_topic_subscription" "orders" {
  endpoint  = "arn:aws:sqs:eu-west-2:123456789012:orders"
  protocol  = "sqs"
  topic_arn = "arn:aws:sns:eu-west-2:123456789012:orders"
  region    = "eu-west-2"
  filter    = "arn:aws:sns:eu-west-2:123456789012:other"
}
`

// Short IDs, names and IDs inside longer strings become references where
// the argument and the value leave no doubt which resource they name.
func TestAddReferencesByIdentities(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, shopGenerated)

	changed, err := addReferences(dir, shopValues)
	if err != nil || !changed {
		t.Fatalf("want references, got changed=%v err=%v", changed, err)
	}

	got := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{
		// Short IDs, by the argument's name.
		"rest_api_id = aws_api_gateway_rest_api.shop.id",
		"resource_id = aws_api_gateway_resource.orders.id",
		"api_id = aws_apigatewayv2_api.http.id",
		// The root resource is the API's, by the ID alone.
		"parent_id = aws_api_gateway_rest_api.shop.root_resource_id",
		"resource_id = aws_api_gateway_rest_api.shop.root_resource_id",
		// The cache namespace is the resource ID another argument has.
		"cache_namespace = aws_api_gateway_resource.orders.id",
		// IDs inside longer strings.
		`target = "integrations/${aws_apigatewayv2_integration.orders.id}"`,
		`source_arn = "${aws_apigatewayv2_api.http.execution_arn}/*/*"`,
		// Names, where the argument names the type.
		"function_name = aws_lambda_function.fn.function_name",
		"log_group = aws_cloudwatch_log_group.fn.name",
		"QueueName = aws_sqs_queue.orders.name",
		// The attribute the argument names: a key's key_id, a zone's zone_id.
		"target_key_id = aws_kms_key.main.key_id",
		"zone_id = aws_route53_zone.shop.zone_id",
		// As Terraform reads a service's task definition back.
		`task_definition = "${aws_ecs_task_definition.web.family}:${aws_ecs_task_definition.web.revision}"`,
		"alarm_actions = [aws_sns_topic.orders.arn]",
		"topic_arn = aws_sns_topic.orders.arn",
		"endpoint = aws_sqs_queue.orders.arn",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, kept := range []string{
		// Names, where the argument doesn't name the type.
		`name = "web"`,
		`path_part = "orders"`,
		// Tags describe; descriptions are prose.
		`Queue = "orders"`,
		`description = "Depth of orders, see arn:aws:sqs:eu-west-2:123456789012:orders"`,
		// A region-wide setting's ID is the region: no ARN has it.
		`region = "eu-west-2"`,
		`filter = "arn:aws:sns:eu-west-2:123456789012:other"`,
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q changed:\n%s", kept, got)
		}
	}
}

// A value several resources have is no reference, unless the argument
// names one's type or another argument of the block refers to one.
func TestAddReferencesLeavesAmbiguousIdentities(t *testing.T) {
	t.Parallel()
	values := map[string]map[string]any{
		"aws_sqs_queue.jobs": {"id": "https://sqs.eu-west-2.amazonaws.com/1/jobs", "name": "jobs"},
		"aws_sns_topic.jobs": {"id": "arn:aws:sns:eu-west-2:1:jobs", "name": "jobs"},
		"aws_lb.a":           {"id": "arn:aws:elasticloadbalancing:eu-west-2:1:loadbalancer/app/a/1", "dns_name": "a-1.eu-west-2.elb.amazonaws.com", "zone_id": "Z35SXDOTRQ7X7K"},
		"aws_lb.b":           {"id": "arn:aws:elasticloadbalancing:eu-west-2:1:loadbalancer/app/b/2", "dns_name": "b-2.eu-west-2.elb.amazonaws.com", "zone_id": "Z35SXDOTRQ7X7K", "arn_suffix": "app/b/2"},
		"aws_subnet.abcdef":  {"id": "abcdef"},
	}
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_cloudwatch_metric_alarm" "jobs" {
  dimensions = {
    LoadBalancer = "app/b/2"
    TopicName    = "jobs"
    Other        = "jobs"
  }
}

resource "aws_route53_record" "b" {
  alias {
    name    = "b-2.eu-west-2.elb.amazonaws.com"
    zone_id = "Z35SXDOTRQ7X7K"
  }
}

resource "aws_route53_record" "both" {
  zone_id = "Z35SXDOTRQ7X7K"
}

resource "aws_network_interface" "x" {
  parent_id = "abcdef"
}
`)
	if changed, err := addReferences(dir, values); err != nil || !changed {
		t.Fatalf("want references, got changed=%v err=%v", changed, err)
	}
	got := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{
		"TopicName = aws_sns_topic.jobs.name",
		// An ARN suffix is the one balancer's.
		"LoadBalancer = aws_lb.b.arn_suffix",
		`Other = "jobs"`,
		// The DNS name says which load balancer; its zone follows.
		"name = aws_lb.b.dns_name",
		"zone_id = aws_lb.b.zone_id",
		// Two load balancers have the zone: no telling which.
		`resource "aws_route53_record" "both" { zone_id = "Z35SXDOTRQ7X7K" }`,
		// A short ID without digits could be a name.
		`parent_id = "abcdef"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// An Azure resource's location is its resource group's when it is in the
// group; elsewhere, or in a nested block, it stays.
func TestAddReferencesToResourceGroupLocations(t *testing.T) {
	t.Parallel()
	const sub = "/subscriptions/00000000-0000-0000-0000-000000000001"
	values := map[string]map[string]any{
		"azurerm_resource_group.app":   {"id": sub + "/resourceGroups/app", "name": "app", "location": "uksouth"},
		"azurerm_resource_group.other": {"id": sub + "/resourceGroups/other", "name": "other", "location": "uksouth"},
		"azurerm_virtual_network.main": {"id": sub + "/resourceGroups/app/providers/Microsoft.Network/virtualNetworks/main", "name": "main", "location": "uksouth"},
		"azurerm_cosmosdb_account.db":  {"id": sub + "/resourceGroups/app/providers/Microsoft.DocumentDB/databaseAccounts/db", "name": "db", "location": "uksouth"},
		"azurerm_public_ip.elsewhere":  {"id": "/subscriptions/00000000-0000-0000-0000-000000000002/resourceGroups/app/providers/Microsoft.Network/publicIPAddresses/ip", "name": "ip", "location": "uksouth"},
	}
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "azurerm_virtual_network" "main" {
  location            = "uksouth"
  name                = "main"
  resource_group_name = "app"
}

resource "azurerm_cosmosdb_account" "db" {
  location = "uksouth"
  geo_location {
    location = "uksouth"
  }
}

resource "azurerm_public_ip" "elsewhere" {
  location = "uksouth"
}

resource "azurerm_resource_group" "other" {
  location = "uksouth"
}
`)
	if changed, err := addReferences(dir, values); err != nil || !changed {
		t.Fatalf("want references, got changed=%v err=%v", changed, err)
	}
	got := squashed(readFile(t, dir, GeneratedFileName))
	if strings.Count(got, "location = azurerm_resource_group.app.location") != 2 {
		t.Errorf("want the network's and the account's locations to be the group's:\n%s", got)
	}
	for _, kept := range []string{
		`geo_location { location = "uksouth" }`,
		// Another subscription's group of the same name isn't the group.
		`resource "azurerm_public_ip" "elsewhere" { location = "uksouth" }`,
		`resource "azurerm_resource_group" "other" { location = "uksouth" }`,
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q changed:\n%s", kept, got)
		}
	}
}

// A resource whose new references change the plan keeps only the
// references of whole IDs; the others keep theirs.
func TestVerifyEachUndoesOnlyTheResourcesThatChange(t *testing.T) {
	t.Parallel()
	values := map[string]map[string]any{
		"aws_api_gateway_rest_api.shop":   {"id": "okoipb1lhk", "name": "shop"},
		"aws_api_gateway_resource.orders": {"id": "a1b2c3"},
		"aws_vpc.main":                    {"id": "vpc-0abc1234"},
		"aws_subnet.a":                    {"id": "subnet-0aaa1111"},
		"aws_security_group.web":          {"id": "sg-0aaa1111"},
	}
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_api_gateway_resource" "orders" {
  rest_api_id = "okoipb1lhk"
  tags = {
    vpc = "vpc-0abc1234"
  }
  vpc_id = "vpc-0abc1234"
}

resource "aws_subnet" "a" {
  vpc_id = "vpc-0abc1234"
}
`)
	clean := plannedImports(values)
	changes := map[string]string{}
	for _, rc := range clean.ResourceChanges {
		changes[rc.Address] = tfrun.ChangeSignature(rc)
	}
	changed := plannedImports(values)
	for _, rc := range changed.ResourceChanges {
		if rc.Address == "aws_api_gateway_resource.orders" {
			rc.Change = &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionUpdate}, Before: map[string]any{"rest_api_id": "okoipb1lhk"}, After: map[string]any{"rest_api_id": "other"}}
		}
	}
	tf := &fakeTerraform{dir: dir, plans: []fakePlan{{summary: tfrun.ChangeSummary{Change: 1}}}, showns: []*tfjson.Plan{changed}, shown: clean}

	kept, err := verifyEach(context.Background(), tf, dir, tfrun.ChangeSummary{}, changes, nil, func(skip map[string]int) (map[string]bool, error) {
		return addReferencesSkipping(dir, values, skip)
	}, GeneratedFileName)
	if err != nil || !kept {
		t.Fatalf("want the references kept, got %v, %v", kept, err)
	}
	got := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{
		`rest_api_id = "okoipb1lhk"`,
		"vpc_id = aws_vpc.main.id }",
		`resource "aws_subnet" "a" { vpc_id = aws_vpc.main.id }`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if want := "plan,show,plan,show"; strings.Join(tf.calls, ",") != want {
		t.Errorf("calls: got %v, want %s", tf.calls, want)
	}
}

// When the plan's errors aren't about a resource, nothing is kept.
func TestVerifyEachUndoesWhatNoResourceExplains(t *testing.T) {
	t.Parallel()
	values := map[string]map[string]any{"aws_vpc.main": {"id": "vpc-0abc1234"}}
	dir := t.TempDir()
	const config = "resource \"aws_subnet\" \"a\" {\n  vpc_id = \"vpc-0abc1234\"\n}\n"
	writeConfig(t, dir, GeneratedFileName, config)
	tf := &fakeTerraform{dir: dir, plans: []fakePlan{{diags: []tfjson.Diagnostic{{Severity: tfjson.DiagnosticSeverityError, Summary: "Cycle"}}}}}

	kept, err := verifyEach(context.Background(), tf, dir, tfrun.ChangeSummary{}, nil, nil, func(skip map[string]int) (map[string]bool, error) {
		return addReferencesSkipping(dir, values, skip)
	}, GeneratedFileName)
	if err != nil || kept {
		t.Fatalf("want nothing kept, got %v, %v", kept, err)
	}
	if got := readFile(t, dir, GeneratedFileName); got != config {
		t.Errorf("generated.tf changed:\n%s", got)
	}
}

// The tenant a Key Vault trusts is the one the provider signs in to.
func TestAddLookupsOfTheTenant(t *testing.T) {
	t.Parallel()
	const tenant = "11111111-2222-3333-4444-555555555555"
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "azurerm_key_vault" "main" {
  tenant_id = "`+tenant+`"
  access_policy {
    tenant_id = "`+tenant+`"
  }
}

resource "aws_instance" "other" {
  tenant_id = "`+tenant+`"
}
`)
	edited, err := addLookups(dir, Names{"data.azurerm_client_config.current": true}, nil)
	if err != nil || !edited["azurerm_key_vault.main"] || len(edited) != 1 {
		t.Fatalf("want the vault edited, got %v, %v", edited, err)
	}
	got := squashed(readFile(t, dir, GeneratedFileName))
	if strings.Count(got, "tenant_id = data.azurerm_client_config.current_2.tenant_id") != 2 || !strings.Contains(got, `tenant_id = "`+tenant+`"`) {
		t.Errorf("generated.tf:\n%s", got)
	}
	data := squashed(readFile(t, dir, DataFileName))
	if !strings.Contains(data, `data "azurerm_client_config" "current_2" {`) || strings.Count(data, "data \"") != 1 {
		t.Errorf("data.tf:\n%s", data)
	}
}

// Domain names identify a distribution and a bucket; the distribution's
// zone, which every distribution has, follows the alias's name.
func TestAddReferencesByDomainNames(t *testing.T) {
	t.Parallel()
	values := map[string]map[string]any{
		"aws_cloudfront_distribution.web": {"id": "E2QWRUHAPOMQZL", "arn": "arn:aws:cloudfront::1:distribution/E2QWRUHAPOMQZL", "domain_name": "d111111abcdef8.cloudfront.net", "hosted_zone_id": "Z2FDTNDATAQYW2"},
		"aws_cloudfront_distribution.api": {"id": "E3ABCDEF123456", "arn": "arn:aws:cloudfront::1:distribution/E3ABCDEF123456", "domain_name": "d222222abcdef8.cloudfront.net", "hosted_zone_id": "Z2FDTNDATAQYW2"},
		"aws_s3_bucket.assets":            {"id": "assets-1", "bucket": "assets-1", "bucket_regional_domain_name": "assets-1.s3.eu-west-2.amazonaws.com"},
	}
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_cloudfront_distribution" "web" {
  origin {
    domain_name = "assets-1.s3.eu-west-2.amazonaws.com"
    origin_id   = "assets"
  }
}

resource "aws_route53_record" "www" {
  alias {
    name    = "d111111abcdef8.cloudfront.net"
    zone_id = "Z2FDTNDATAQYW2"
  }
}
`)
	if changed, err := addReferences(dir, values); err != nil || !changed {
		t.Fatalf("want references, got changed=%v err=%v", changed, err)
	}
	got := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{
		"domain_name = aws_s3_bucket.assets.bucket_regional_domain_name",
		"name = aws_cloudfront_distribution.web.domain_name",
		"zone_id = aws_cloudfront_distribution.web.hosted_zone_id",
		`origin_id = "assets"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestSnakeCase(t *testing.T) {
	t.Parallel()
	for key, want := range map[string]string{
		"QueueName":            "queue_name",
		"DBInstanceIdentifier": "db_instance_identifier",
		"FunctionName":         "function_name",
		"QUEUE_URL":            "queue_url",
		"queue_name":           "queue_name",
		"LoadBalancer":         "load_balancer",
	} {
		if got := snakeCase(key); got != want {
			t.Errorf("snakeCase(%q): got %q, want %q", key, got, want)
		}
	}
}

func TestNamesType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		argument, address string
		want              bool
	}{
		{"rest_api_id", "aws_api_gateway_rest_api.x", true},
		{"log_group", "aws_cloudwatch_log_group.x", true},
		{"queue_name", "aws_sqs_queue.x", true},
		{"target_key_id", "aws_kms_key.x", true},
		{"security_groups", "aws_security_group.x", true},
		{"task_definition", "aws_ecs_task_definition.x", true},
		{"name", "aws_sqs_queue.x", false},
		{"parent_id", "aws_api_gateway_rest_api.x", false},
		{"dns_name", "azurerm_virtual_network.x", false},
		{"key", "aws_kms_key.x", true},
		{"keys", "aws_kms_alias.x", false},
	} {
		if got := namesType(tc.argument, tc.address); got != tc.want {
			t.Errorf("namesType(%q, %q): got %v, want %v", tc.argument, tc.address, got, tc.want)
		}
	}
}

// A private DNS zone names its network on the Compute API's own host.
func TestAddReferencesBySelfLinkOnTheComputeHost(t *testing.T) {
	t.Parallel()
	values := map[string]map[string]any{
		"google_compute_network.main": {"id": "projects/acme/global/networks/main", "name": "main", "self_link": "https://www.googleapis.com/compute/v1/projects/acme/global/networks/main"},
	}
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "google_dns_managed_zone" "internal" {
  private_visibility_config {
    networks {
      network_url = "https://compute.googleapis.com/compute/v1/projects/acme/global/networks/main"
    }
  }
}
`)
	if changed, err := addReferences(dir, values); err != nil || !changed {
		t.Fatalf("want references, got changed=%v err=%v", changed, err)
	}
	if got := squashed(readFile(t, dir, GeneratedFileName)); !strings.Contains(got, "network_url = google_compute_network.main.self_link") {
		t.Errorf("generated.tf:\n%s", got)
	}
}
