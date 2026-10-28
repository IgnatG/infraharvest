// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// mergeStack is mergeStackWith without controls, as a stack first merges.
func mergeStack(provider string, profile MergeProfile, roots []envRoot, schemas *tfjson.ProviderSchemas) *stackMerge {
	return mergeStackWith(provider, profile, roots, schemas, nil)
}

// mergeProfiles are the merge profiles of the providers whose roots the
// tests merge, as the importer fills them from the providers' profiles.
var mergeProfiles = map[string]MergeProfile{
	"aws": {
		ScopeTitle:     "Account",
		ScopeData:      "aws_caller_identity.current",
		ScopeLocal:     "aws_account_id",
		ScopeExpr:      "data.aws_caller_identity.current.account_id",
		RegionArgument: "region",
		RegionTitle:    "AWS region",
		RegionData:     "aws_region.current",
		RegionLocal:    "aws_region",
		RegionExpr:     "data.aws_region.current.region",
	},
	"azurerm": {
		ScopeTitle: "Subscription",
		ScopeData:  "azurerm_client_config.current",
		ScopeLocal: "subscription_id",
		ScopeExpr:  "data.azurerm_client_config.current.subscription_id",
	},
	"google": {
		ScopeTitle: "Project",
		ScopeData:  "google_client_config.current",
		ScopeLocal: "project_id",
		ScopeExpr:  "data.google_client_config.current.project",
	},
}

func stringAttributes(names ...string) map[string]*tfjson.SchemaAttribute {
	attrs := map[string]*tfjson.SchemaAttribute{}
	for _, name := range names {
		attrs[name] = &tfjson.SchemaAttribute{AttributeType: cty.String}
	}
	return attrs
}

var envSchemas = &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
	"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
		"aws_sqs_queue": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
			"name":                       {AttributeType: cty.String},
			"kms_master_key_id":          {AttributeType: cty.String},
			"visibility_timeout_seconds": {AttributeType: cty.Number},
			"delay_seconds":              {AttributeType: cty.Number},
			"max_message_size":           {AttributeType: cty.Number},
		}}},
		"aws_db_instance": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
			"identifier":        {AttributeType: cty.String},
			"engine":            {AttributeType: cty.String},
			"instance_class":    {AttributeType: cty.String},
			"allocated_storage": {AttributeType: cty.Number},
			"password":          {AttributeType: cty.String, Sensitive: true},
		}}},
		"aws_ssm_parameter": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
			"name":  {AttributeType: cty.String},
			"type":  {AttributeType: cty.String},
			"value": {AttributeType: cty.String, Sensitive: true},
		}}},
		"aws_dynamodb_table": {Block: &tfjson.SchemaBlock{
			Attributes:   stringAttributes("name", "billing_mode", "hash_key"),
			NestedBlocks: map[string]*tfjson.SchemaBlockType{"attribute": {Block: &tfjson.SchemaBlock{Attributes: stringAttributes("name", "type")}}},
		}},
		"aws_lambda_function": {Block: &tfjson.SchemaBlock{
			Attributes:   stringAttributes("function_name", "handler", "runtime"),
			NestedBlocks: map[string]*tfjson.SchemaBlockType{"tracing_config": {Block: &tfjson.SchemaBlock{Attributes: stringAttributes("mode")}}},
		}},
	}},
}}

// flatExpr is tokens formatted, on one line.
func flatExpr(tokens hclwrite.Tokens) string {
	formatted := hclwrite.Format(append([]byte("x = "), tokens.Bytes()...))
	return strings.TrimPrefix(strings.Join(strings.Fields(string(formatted)), " "), "x = ")
}

func resourceAt(t *testing.T, m *stackMerge, address string) *stackResource {
	t.Helper()
	for _, r := range m.resources {
		if r.resourceType+"."+r.label == address {
			return r
		}
	}
	var have []string
	for _, r := range m.resources {
		have = append(have, r.resourceType+"."+r.label)
	}
	t.Fatalf("no resource %s; have %s", address, strings.Join(have, ", "))
	return nil
}

func attributeOf(t *testing.T, r *stackResource, name string) *mergedAttribute {
	t.Helper()
	if r.body == nil {
		t.Fatalf("%s isn't merged: %s", r.address(), r.residue)
	}
	for _, a := range r.body.attributes {
		if a.name == name {
			return a
		}
	}
	t.Fatalf("%s has no %s", r.address(), name)
	return nil
}

func variableNamed(t *testing.T, m *stackMerge, name string) *envVariable {
	t.Helper()
	for _, v := range m.variables {
		if v.name == name {
			return v
		}
	}
	t.Fatalf("no variable %s", name)
	return nil
}

// tfvarsOf is environment env's tfvars, formatted.
func tfvarsOf(m *stackMerge, env int) map[string]string {
	values := map[string]string{}
	for name, tokens := range m.tfvars(env) {
		values[name] = flatExpr(tokens)
	}
	return values
}

// mergeDevProd merges dev and prod, roots of generated configuration with
// their import IDs, of AWS accounts 111111111111 and 222222222222, prod
// named with the token prd.
func mergeDevProd(t *testing.T, dev, prod string, devIDs, prodIDs map[string]string) *stackMerge {
	t.Helper()
	roots := []envRoot{
		testEnvRoot(t, "dev", nil, "111111111111", dev, devIDs),
		testEnvRoot(t, "prod", []string{"prd"}, "222222222222", prod, prodIDs),
	}
	for i := range roots {
		roots[i].region = "eu-west-2"
	}
	return mergeStack("aws", mergeProfiles["aws"], roots, envSchemas)
}

// localsOf is m's locals, formatted, by name, and their names in order.
func localsOf(m *stackMerge) (map[string]string, []string) {
	values := map[string]string{}
	names := make([]string, 0, len(m.locals))
	for _, l := range m.locals {
		values[l.name] = flatExpr(l.value)
		names = append(names, l.name)
	}
	return values, names
}

func TestMergeStackTemplates(t *testing.T) {
	m := mergeDevProd(t, `resource "aws_iam_role" "ci" {
  description          = "reads arn:aws:sqs:eu-west-2:111111111111:shop-dev-orders"
  name                 = "shop-dev-ci"
  permissions_boundary = "arn:aws:iam::111111111111:policy/boundary"
}
`, `resource "aws_iam_role" "ci" {
  description          = "reads arn:aws:sqs:eu-west-2:222222222222:shop-prd-orders"
  name                 = "shop-prd-ci"
  permissions_boundary = "arn:aws:iam::222222222222:policy/boundary"
}
`, map[string]string{"aws_iam_role.ci": "shop-dev-ci"}, map[string]string{"aws_iam_role.ci": "shop-prd-ci"})
	role := resourceAt(t, m, "aws_iam_role.ci")
	if role.deploy != nil || role.forEach != nil {
		t.Error("a resource every environment has is counted")
	}
	for name, want := range map[string]string{
		"description":          `"reads arn:aws:sqs:${local.aws_region}:${local.aws_account_id}:${local.naming_prefix}-orders"`,
		"name":                 "local.ci_name",
		"permissions_boundary": `"arn:aws:iam::${local.aws_account_id}:policy/boundary"`,
	} {
		a := attributeOf(t, role, name)
		if a.kind != templateValue || flatExpr(a.value) != want {
			t.Errorf("%s = %s (kind %d), want the template %s", name, flatExpr(a.value), a.kind, want)
		}
	}
	if a := attributeOf(t, role, "name"); a.local != "ci_name" {
		t.Errorf("the name's local is %q", a.local)
	}
	if m.workload != "shop" {
		t.Errorf("workload = %q, want shop", m.workload)
	}
	if !slices.Equal(m.data, []string{"aws_caller_identity.current", "aws_region.current"}) {
		t.Errorf("data = %v", m.data)
	}
	locals, order := localsOf(m)
	if want := []string{"naming_prefix", "aws_account_id", "aws_region", "ci_name"}; !slices.Equal(order, want) {
		t.Errorf("locals %v, want %v", order, want)
	}
	for name, want := range map[string]string{
		"naming_prefix":  `"shop-${var.env}"`,
		"aws_account_id": "data.aws_caller_identity.current.account_id",
		"aws_region":     "data.aws_region.current.region",
		"ci_name":        `"${local.naming_prefix}-ci"`,
	} {
		if locals[name] != want {
			t.Errorf("local.%s = %s, want %s", name, locals[name], want)
		}
	}
	// var.env is the token the names use, not the environment's name.
	for e, want := range []string{`"dev"`, `"prd"`} {
		if got := tfvarsOf(m, e)["env"]; got != want {
			t.Errorf("%s: env = %s, want %s", m.envs[e], got, want)
		}
	}
	if ids := m.imports(1); ids["aws_iam_role.ci"] != "shop-prd-ci" {
		t.Errorf("prod imports = %v", ids)
	}
}

func TestMergeStackTokenCase(t *testing.T) {
	m := mergeDevProd(t, `resource "aws_sqs_queue" "orders" {
  name = "shop-DEV-orders"
}
`, `resource "aws_sqs_queue" "orders" {
  name = "shop-PRD-orders"
}
`, map[string]string{"aws_sqs_queue.orders": "shop-DEV-orders"}, map[string]string{"aws_sqs_queue.orders": "shop-PRD-orders"})
	queue := resourceAt(t, m, "aws_sqs_queue.orders")
	if a := attributeOf(t, queue, "name"); a.kind != templateValue {
		t.Errorf("name = %s (kind %d), want a template of var.env", flatExpr(a.value), a.kind)
	}
	// var.env keeps the case the names write the token in.
	for e, want := range []string{`"DEV"`, `"PRD"`} {
		if got := tfvarsOf(m, e)["env"]; got != want {
			t.Errorf("%s: env = %s, want %s", m.envs[e], got, want)
		}
	}
}

func TestMergeStackConditions(t *testing.T) {
	bucket := func(env, forceDestroy, billing string) string {
		return `resource "aws_s3_bucket" "logs" {
  bucket        = "shop-` + env + `-logs"
  force_destroy = ` + forceDestroy + `
  tags          = { Owner = "platform" }
}

resource "aws_dynamodb_table" "orders" {
  billing_mode = "` + billing + `"
  hash_key     = "id"
  name         = "shop-` + env + `-orders"
}
`
	}
	ids := func(env string) map[string]string {
		return map[string]string{"aws_s3_bucket.logs": "shop-" + env + "-logs", "aws_dynamodb_table.orders": "shop-" + env + "-orders"}
	}
	m := mergeDevProd(t, bucket("dev", "true", "PAY_PER_REQUEST"), bucket("prd", "false", "PROVISIONED"), ids("dev"), ids("prd"))
	for _, tc := range []struct{ address, attr, want string }{
		// Two environments: true where it is.
		{"aws_s3_bucket.logs", "force_destroy", `var.env == "dev"`},
		// Two environments: the last one's value.
		{"aws_dynamodb_table.orders", "billing_mode", `var.env == "prd" ? "PROVISIONED" : "PAY_PER_REQUEST"`},
	} {
		a := attributeOf(t, resourceAt(t, m, tc.address), tc.attr)
		if a.kind != conditionValue || flatExpr(a.value) != tc.want {
			t.Errorf("%s.%s = %s (kind %d), want %s", tc.address, tc.attr, flatExpr(a.value), a.kind, tc.want)
		}
	}
	for _, v := range m.variables {
		if v.name != "env" {
			t.Errorf("variable %s for a value a condition gives", v.name)
		}
	}

	// Three environments, one of them false.
	roots := make([]envRoot, 0, 3)
	for _, env := range []string{"dev", "stage", "prod"} {
		destroy := "true"
		if env == "prod" {
			destroy = "false"
		}
		roots = append(roots, testEnvRoot(t, env, nil, "", bucket(env, destroy, "PAY_PER_REQUEST"), ids(env)))
	}
	m = mergeStack("google", mergeProfiles["google"], roots, nil)
	if a := attributeOf(t, resourceAt(t, m, "aws_s3_bucket.logs"), "force_destroy"); flatExpr(a.value) != `var.env != "prod"` {
		t.Errorf("force_destroy = %s", flatExpr(a.value))
	}
}

func TestMergeStackFeatures(t *testing.T) {
	m := mergeDevProd(t, `resource "aws_s3_bucket" "logs" {
  bucket = "shop-dev-logs"
}
`, `resource "aws_cloudwatch_metric_alarm" "errors" {
  alarm_name = "shop-prd-errors"
}

resource "aws_customer_gateway" "vpn_office" {
  ip_address = "203.0.113.10"
}

resource "aws_s3_bucket" "logs" {
  bucket = "shop-prd-logs"
}

resource "aws_vpn_connection" "office" {
  customer_gateway_id = aws_customer_gateway.vpn_office.id
  vpn_gateway_id      = aws_vpn_gateway.vpn.id
}

resource "aws_vpn_gateway" "vpn" {
  amazon_side_asn = 64512
}
`, map[string]string{"aws_s3_bucket.logs": "shop-dev-logs"}, map[string]string{
		"aws_s3_bucket.logs":                 "shop-prd-logs",
		"aws_cloudwatch_metric_alarm.errors": "shop-prd-errors",
		"aws_customer_gateway.vpn_office":    "cgw-1",
		"aws_vpn_connection.office":          "vpn-1",
		"aws_vpn_gateway.vpn":                "vgw-1",
	})
	vpn := resourceAt(t, m, "aws_vpn_gateway.vpn").deploy
	if vpn == nil || vpn.name != "deploy_vpn" {
		t.Fatalf("the VPN gateway is counted by %v, want deploy_vpn", vpn)
	}
	for _, address := range []string{"aws_customer_gateway.vpn_office", "aws_vpn_connection.office"} {
		if resourceAt(t, m, address).deploy != vpn {
			t.Errorf("%s isn't counted by deploy_vpn", address)
		}
	}
	if alarm := resourceAt(t, m, "aws_cloudwatch_metric_alarm.errors").deploy; alarm == nil || alarm.name != "deploy_errors" {
		t.Error("the alarm, a feature of its own, isn't counted by deploy_errors")
	}
	var deploys []string
	for _, v := range m.variables {
		if strings.HasPrefix(v.name, "deploy_") {
			deploys = append(deploys, v.name)
		}
	}
	if !slices.Equal(deploys, []string{"deploy_errors", "deploy_vpn"}) {
		t.Errorf("deploy variables %v", deploys)
	}
	if got := tfvarsOf(m, 1); got["deploy_vpn"] != "true" || got["deploy_errors"] != "true" {
		t.Errorf("prod tfvars = %v", got)
	}
	connection := resourceAt(t, m, "aws_vpn_connection.office")
	if a := attributeOf(t, connection, "vpn_gateway_id"); flatExpr(a.value) != "aws_vpn_gateway.vpn[0].id" {
		t.Errorf("vpn_gateway_id = %s", flatExpr(a.value))
	}
	if ids := m.imports(1); ids["aws_vpn_connection.office[0]"] != "vpn-1" {
		t.Errorf("prod imports = %v", ids)
	}
}

func TestMergeStackVariables(t *testing.T) {
	m := mergeDevProd(t, `resource "aws_sqs_queue" "orders" {
  delay_seconds              = 0
  name                       = "shop-dev-orders"
  visibility_timeout_seconds = 30
}
`, `resource "aws_sqs_queue" "orders" {
  delay_seconds              = 0
  kms_master_key_id          = "alias/aws/sqs"
  name                       = "shop-prd-orders"
  visibility_timeout_seconds = 60
}
`, map[string]string{"aws_sqs_queue.orders": "shop-dev-orders"}, map[string]string{"aws_sqs_queue.orders": "shop-prd-orders"})
	queue := resourceAt(t, m, "aws_sqs_queue.orders")
	if a := attributeOf(t, queue, "delay_seconds"); a.kind != sameValue || flatExpr(a.value) != "0" {
		t.Errorf("delay_seconds = %s", flatExpr(a.value))
	}
	timeout := attributeOf(t, queue, "visibility_timeout_seconds")
	if timeout.kind != variableValue || flatExpr(timeout.value) != "var.orders_visibility_timeout_seconds" {
		t.Errorf("visibility_timeout_seconds = %s", flatExpr(timeout.value))
	}
	v := variableNamed(t, m, "orders_visibility_timeout_seconds")
	if !v.ty.Equals(cty.Number) || v.what != "visibility_timeout_seconds of aws_sqs_queue.orders" {
		t.Errorf("variable: type %s, what %q", v.ty.FriendlyName(), v.what)
	}
	// Two environments: both values stay explicit.
	if v.hasDefault {
		t.Error("a variable of two environments has a default")
	}
	// An argument one environment sets: null by default.
	key := variableNamed(t, m, "orders_kms_master_key_id")
	if !key.hasDefault || key.defaultValue != nil {
		t.Error("an argument one environment sets has no default null")
	}
	for e, want := range []map[string]string{
		{"env": `"dev"`, "orders_visibility_timeout_seconds": "30"},
		{"env": `"prd"`, "orders_visibility_timeout_seconds": "60", "orders_kms_master_key_id": `"alias/aws/sqs"`},
	} {
		if got := tfvarsOf(m, e); !maps.Equal(got, want) {
			t.Errorf("%s tfvars = %v, want %v", m.envs[e], got, want)
		}
	}
}

func TestEnvVariableDefaults(t *testing.T) {
	tokens := func(s string) hclwrite.Tokens {
		if s == "" {
			return nil
		}
		return hclwrite.TokensForIdentifier(s)
	}
	for _, tc := range []struct {
		name      string
		envs      int
		values    map[int]string // "" for null
		sensitive bool
		// want is the default, "-" for none and "null" for null, and
		// tfvars what each environment sets, "-" for nothing.
		want   string
		tfvars []string
	}{
		{"two environments keep both values", 2, map[int]string{0: "30", 1: "60"}, false, "-", []string{"30", "60"}},
		{"a strict majority of three is the default", 3, map[int]string{0: "30", 1: "30", 2: "60"}, false, "30", []string{"-", "-", "60"}},
		{"no majority of three", 3, map[int]string{0: "30", 1: "45", 2: "60"}, false, "-", []string{"30", "45", "60"}},
		{"half of four isn't a majority", 4, map[int]string{0: "30", 1: "30", 2: "60", 3: "90"}, false, "-", []string{"30", "30", "60", "90"}},
		{"null in one of two", 2, map[int]string{0: "30", 1: ""}, false, "null", []string{"30", "-"}},
		{"a majority of nulls", 3, map[int]string{0: "", 1: "", 2: "5"}, false, "null", []string{"-", "-", "5"}},
		{"a majority over a null", 3, map[int]string{0: "5", 1: "5", 2: ""}, false, "5", []string{"-", "-", "null"}},
		{"an environment without the resource", 3, map[int]string{0: "30", 1: "60"}, false, "null", []string{"30", "60", "-"}},
		{"a secret every environment sets", 3, map[int]string{0: "", 1: "", 2: ""}, true, "-", []string{"-", "-", "-"}},
		{"a secret one environment sets", 2, map[int]string{1: ""}, true, "null", []string{"-", "-"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &envVariable{name: "x", values: map[int]hclwrite.Tokens{}, sensitive: tc.sensitive}
			for e, s := range tc.values {
				v.values[e] = tokens(s)
			}
			v.setDefault(tc.envs)
			got := "-"
			if v.hasDefault {
				got = "null"
				if v.defaultValue != nil {
					got = flatExpr(v.defaultValue)
				}
			}
			if got != tc.want {
				t.Errorf("default = %s, want %s", got, tc.want)
			}
			for e, want := range tc.tfvars {
				value, ok := v.tfvars(e)
				got := "-"
				if ok {
					got = flatExpr(value)
				}
				if got != want {
					t.Errorf("environment %d sets %s, want %s", e, got, want)
				}
			}
		})
	}
}

func TestMergeStackSecrets(t *testing.T) {
	m := mergeDevProd(t, `resource "aws_db_instance" "main" {
  allocated_storage = 20
  engine            = "postgres"
  identifier        = "shop-dev-db"
  instance_class    = "db.t3.micro"
  password          = var.shop_dev_password
}
`, `resource "aws_db_instance" "main" {
  allocated_storage = 20
  engine            = "postgres"
  identifier        = "shop-prd-db"
  instance_class    = "db.r6g.large"
  password          = var.shop_prd_password
}
`, map[string]string{"aws_db_instance.main": "shop-dev-db"}, map[string]string{"aws_db_instance.main": "shop-prd-db"})
	db := resourceAt(t, m, "aws_db_instance.main")
	if a := attributeOf(t, db, "password"); flatExpr(a.value) != "var.db_instance_password" {
		t.Errorf("password = %s", flatExpr(a.value))
	}
	if v := variableNamed(t, m, "db_instance_password"); !v.sensitive || v.hasDefault {
		t.Errorf("the password's variable: sensitive %t, default %t; want sensitive, without a default", v.sensitive, v.hasDefault)
	}
	for e := range m.envs {
		vars := m.tfvars(e)
		if _, ok := vars["db_instance_password"]; ok {
			t.Errorf("%s tfvars set the password", m.envs[e])
		}
		if _, ok := vars["db_instance_instance_class"]; !ok {
			t.Errorf("%s tfvars don't set the instance class", m.envs[e])
		}
	}
}

func TestMergeStackSensitiveLiteral(t *testing.T) {
	m := mergeDevProd(t, `resource "aws_ssm_parameter" "url" {
  name  = "/shop/dev/url"
  type  = "String"
  value = "https://one.example.com"
}
`, `resource "aws_ssm_parameter" "url" {
  name  = "/shop/prd/url"
  type  = "String"
  value = "https://two.example.com"
}
`, map[string]string{"aws_ssm_parameter.url": "/shop/dev/url"}, map[string]string{"aws_ssm_parameter.url": "/shop/prd/url"})
	for _, address := range []string{"aws_ssm_parameter.url_dev", "aws_ssm_parameter.url_prod"} {
		if r := resourceAt(t, m, address); r.residue != "its sensitive value differs between the environments" || r.deploy == nil {
			t.Errorf("%s: residue %q, deploy %v", address, r.residue, r.deploy)
		}
	}
	for _, v := range m.variables {
		if v.sensitive {
			t.Errorf("variable %s for a literal", v.name)
		}
	}
}

func TestMergeStackResidue(t *testing.T) {
	m := mergeDevProd(t, `resource "aws_sqs_queue" "orders" {
  delay_seconds              = 0
  max_message_size           = 1024
  name                       = "shop-dev-orders"
  visibility_timeout_seconds = 30
}
`, `resource "aws_sqs_queue" "orders" {
  delay_seconds              = 5
  max_message_size           = 2048
  name                       = "shop-prd-orders"
  visibility_timeout_seconds = 60
}
`, map[string]string{"aws_sqs_queue.orders": "shop-dev-orders"}, map[string]string{"aws_sqs_queue.orders": "shop-prd-orders"})
	for e, address := range []string{"aws_sqs_queue.orders_dev", "aws_sqs_queue.orders_prod"} {
		r := resourceAt(t, m, address)
		if r.residue != "3 of its 4 arguments differ between the environments" {
			t.Errorf("%s: residue %q", address, r.residue)
		}
		if r.deploy == nil || r.deploy.name != "deploy_orders_"+m.envs[e] {
			t.Errorf("%s isn't counted by its own variable", address)
		}
		// One environment's resource, written as it is, but for its name.
		if a := attributeOf(t, r, "name"); a.kind != sameValue || a.local != "orders_"+m.envs[e]+"_name" || flatExpr(a.value) != "local."+a.local {
			t.Errorf("%s: name = %s (kind %d)", address, flatExpr(a.value), a.kind)
		}
	}
	locals, order := localsOf(m)
	if want := []string{"naming_prefix", "orders_dev_name", "orders_prod_name"}; !slices.Equal(order, want) {
		t.Errorf("locals %v, want %v", order, want)
	}
	for name, want := range map[string]string{
		"naming_prefix":    `"shop-${var.env}"`,
		"orders_dev_name":  `"${local.naming_prefix}-orders"`,
		"orders_prod_name": `"${local.naming_prefix}-orders"`,
	} {
		if locals[name] != want {
			t.Errorf("local.%s = %s, want %s", name, locals[name], want)
		}
	}
	if ids := m.imports(0); ids["aws_sqs_queue.orders_dev[0]"] != "shop-dev-orders" {
		t.Errorf("dev imports = %v", ids)
	}
}

// TestMergeStackResidueNames checks that a residue resource's name that
// holds no token of its environment, or not the one var.env stands for,
// is a local of its name as it is.
func TestMergeStackResidueNames(t *testing.T) {
	roots := []envRoot{
		testEnvRoot(t, "dev", nil, "111111111111", `resource "aws_sqs_queue" "orders" {
  delay_seconds              = 0
  max_message_size           = 1024
  name                       = "shop-dev-orders"
  visibility_timeout_seconds = 30
}
`, map[string]string{"aws_sqs_queue.orders": "shop-dev-orders"}),
		testEnvRoot(t, "prod", []string{"prd", "prod"}, "222222222222", `resource "aws_sqs_queue" "orders" {
  delay_seconds              = 5
  max_message_size           = 2048
  name                       = "legacy-orders"
  visibility_timeout_seconds = 60
}

resource "aws_sqs_queue" "refunds" {
  delay_seconds = 5
  name          = "shop-prd-refunds"
}

resource "aws_sqs_queue" "returns" {
  delay_seconds = 5
  name          = "shop-prod-returns"
}
`, map[string]string{"aws_sqs_queue.orders": "legacy-orders", "aws_sqs_queue.refunds": "shop-prd-refunds", "aws_sqs_queue.returns": "shop-prod-returns"}),
	}
	isolated := map[string]bool{mergeKey(1, "aws_sqs_queue.refunds"): true, mergeKey(1, "aws_sqs_queue.returns"): true}
	m := mergeStackWith("aws", mergeProfiles["aws"], roots, envSchemas, &mergeControls{isolated: isolated})
	locals, _ := localsOf(m)
	for address, want := range map[string]string{
		"aws_sqs_queue.orders_dev":  `"${local.naming_prefix}-orders"`,
		"aws_sqs_queue.orders_prod": `"legacy-orders"`,
		"aws_sqs_queue.refunds":     `"${local.naming_prefix}-refunds"`,
		// prd is the token var.env stands for in prod.
		"aws_sqs_queue.returns": `"shop-prod-returns"`,
	} {
		r := resourceAt(t, m, address)
		if r.residue == "" {
			t.Errorf("%s isn't residue", address)
		}
		a := attributeOf(t, r, "name")
		if a.local == "" || flatExpr(a.value) != "local."+a.local || locals[a.local] != want {
			t.Errorf("%s: name = %s, local.%s = %s, want %s", address, flatExpr(a.value), a.local, locals[a.local], want)
		}
	}
	if got := tfvarsOf(m, 1)["env"]; got != `"prd"` {
		t.Errorf("prod: env = %s", got)
	}
}

func TestMergeStackEnvOnly(t *testing.T) {
	const dev = `resource "aws_sqs_queue" "orders" {
  delay_seconds              = 0
  name                       = "shop-dev-orders"
  visibility_timeout_seconds = 30
}
`
	devIDs := map[string]string{"aws_sqs_queue.orders": "https://sqs.eu-west-2.amazonaws.com/111111111111/shop-dev-orders"}

	t.Run("counted", func(t *testing.T) {
		m := mergeDevProd(t, dev, `resource "aws_cloudwatch_metric_alarm" "errors" {
  alarm_name = "shop-prd-errors"
  threshold  = 1
}

resource "aws_sqs_queue" "orders" {
  delay_seconds              = 0
  name                       = "shop-prd-orders"
  visibility_timeout_seconds = 30
}
`, devIDs, map[string]string{
			"aws_cloudwatch_metric_alarm.errors": "shop-prd-errors",
			"aws_sqs_queue.orders":               "https://sqs.eu-west-2.amazonaws.com/222222222222/shop-prd-orders",
		})
		alarm := resourceAt(t, m, "aws_cloudwatch_metric_alarm.errors")
		if alarm.deploy == nil || alarm.deploy.name != "deploy_errors" || alarm.forEach != nil {
			t.Fatal("the alarm prod only has isn't counted by deploy_errors")
		}
		if v := alarm.deploy; !v.hasDefault || flatExpr(v.defaultValue) != "false" {
			t.Error("deploy_errors isn't false by default")
		}
		if got := tfvarsOf(m, 1)["deploy_errors"]; got != "true" {
			t.Errorf("prod sets deploy_errors = %s, want true", got)
		}
		if _, ok := tfvarsOf(m, 0)["deploy_errors"]; ok {
			t.Error("dev sets deploy_errors")
		}
		if ids := m.imports(1); ids["aws_cloudwatch_metric_alarm.errors[0]"] != "shop-prd-errors" {
			t.Errorf("prod imports = %v", ids)
		}
		if queue := resourceAt(t, m, "aws_sqs_queue.orders"); queue.rule != byImportID || queue.deploy != nil {
			t.Error("the queue isn't matched by its URL")
		}
	})

	t.Run("a key of a compatible resource", func(t *testing.T) {
		m := mergeDevProd(t, dev, `resource "aws_sqs_queue" "orders" {
  delay_seconds              = 0
  name                       = "shop-prd-orders"
  visibility_timeout_seconds = 30
}

resource "aws_sqs_queue" "refunds" {
  delay_seconds              = 0
  name                       = "shop-prd-refunds"
  visibility_timeout_seconds = 30
}
`, devIDs, map[string]string{
			"aws_sqs_queue.orders":  "https://sqs.eu-west-2.amazonaws.com/222222222222/shop-prd-orders",
			"aws_sqs_queue.refunds": "https://sqs.eu-west-2.amazonaws.com/222222222222/shop-prd-refunds",
		})
		if len(m.forEach) != 1 {
			t.Fatalf("%d for_each groups, want 1", len(m.forEach))
		}
		g := m.forEach[0]
		var keys []string
		for _, k := range g.keys {
			keys = append(keys, k.label)
			if k.deploy != nil || k.address() != "aws_sqs_queue.this" {
				t.Errorf("key %s: address %s", k.label, k.address())
			}
		}
		if g.address() != "aws_sqs_queue.this" || !slices.Equal(keys, []string{"orders", "refunds"}) || !slices.Equal(g.fields, []string{"name"}) {
			t.Errorf("group %s, keys %v, fields %v", g.address(), keys, g.fields)
		}
		for _, a := range g.body.attributes {
			want := sameValue
			if a.name == "name" {
				want = eachValue
			}
			if a.kind != want {
				t.Errorf("%s is of kind %d, want %d (%s)", a.name, a.kind, want, flatExpr(a.value))
			}
		}
		if got := tfvarsOf(m, 0)["sqs_queues"]; got != `{ "orders" = { name = "shop-dev-orders" } }` {
			t.Errorf("dev sqs_queues = %s", got)
		}
		if got := tfvarsOf(m, 1)["sqs_queues"]; got != `{ "orders" = { name = "shop-prd-orders" } "refunds" = { name = "shop-prd-refunds" } }` {
			t.Errorf("prod sqs_queues = %s", got)
		}
		if ids := m.imports(1); ids[`aws_sqs_queue.this["refunds"]`] == "" || ids[`aws_sqs_queue.this["orders"]`] == "" {
			t.Errorf("prod imports = %v", ids)
		}

		// Keys that no longer merge, as one has a block the other
		// lacks, dissolve the group, rather than leave it without a body.
		g.keys[1].members[1].block.Write.Body().AppendNewBlock("redrive_allow_policy", nil)
		m.forEachBodies()
		if len(m.forEach) != 0 {
			t.Fatalf("%d for_each groups, want the group dissolved", len(m.forEach))
		}
		for _, k := range g.keys {
			if k.forEach != nil || k.address() != "aws_sqs_queue."+k.label {
				t.Errorf("key %s: still in the group, at %s", k.label, k.address())
			}
		}
	})
}

// A data source only some environments read is counted with the feature
// that alone refers to it, and the references to it are to its instance.
func TestMergeStackFeatureData(t *testing.T) {
	const dev = `resource "aws_sqs_queue" "orders" {
  name = "shop-dev-orders"
}
`
	merge := func(t *testing.T, prod, data string) *stackMerge {
		t.Helper()
		roots := []envRoot{
			awsEnvRoot(t, "dev", nil, "111111111111", dev, map[string]string{"aws_sqs_queue.orders": "shop-dev-orders"}, nil, nil, ""),
			awsEnvRoot(t, "prod", []string{"prd"}, "222222222222", prod, map[string]string{"aws_sqs_queue.orders": "shop-prd-orders", "aws_vpn_gateway.vpn": "vgw-1"}, nil, nil, ""),
		}
		f, err := hclfile.Parse(rootfile.DataFileName, []byte(data))
		if err != nil {
			t.Fatal(err)
		}
		roots[1].data = f
		return mergeStack("aws", mergeProfiles["aws"], roots, envSchemas)
	}
	const policy = `data "aws_vpc" "vpn" {
  id = "vpc-0abc1234"
}

data "aws_iam_policy_document" "vpn" {
  statement {
    resources = [data.aws_vpc.vpn.arn]
  }
}
`

	t.Run("counted with its feature", func(t *testing.T) {
		m := merge(t, dev+`
resource "aws_vpn_gateway" "vpn" {
  vpc_id = data.aws_vpc.vpn.id
  tags = {
    Policy = data.aws_iam_policy_document.vpn.json
  }
}
`, policy)
		if m.reason != "" {
			t.Fatal(m.reason)
		}
		deploy := resourceAt(t, m, "aws_vpn_gateway.vpn").deploy
		if deploy == nil {
			t.Fatal("the VPN gateway isn't counted")
		}
		for _, d := range m.rootData {
			if d.deploy != deploy {
				t.Errorf("data.%s is counted by %v, want %s", d.address, d.deploy, deploy.name)
			}
		}
		gateway := resourceAt(t, m, "aws_vpn_gateway.vpn")
		if a := attributeOf(t, gateway, "vpc_id"); flatExpr(a.value) != "data.aws_vpc.vpn[0].id" {
			t.Errorf("vpc_id = %s", flatExpr(a.value))
		}
		if a := attributeOf(t, gateway, "tags"); !strings.Contains(flatExpr(a.value), "data.aws_iam_policy_document.vpn[0].json") {
			t.Errorf("tags = %s", flatExpr(a.value))
		}
		files, err := mergedRootFiles(m)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"data \"aws_iam_policy_document\" \"vpn\" {\n  count = var." + deploy.name + " ? 1 : 0\n\n  statement {\n    resources = [data.aws_vpc.vpn[0].arn]\n",
			"data \"aws_vpc\" \"vpn\" {\n  count = var." + deploy.name + " ? 1 : 0\n\n  id = \"vpc-0abc1234\"\n}\n",
		} {
			if got := string(files[rootfile.DataFileName]); !strings.Contains(got, want) {
				t.Errorf("%s:\n%s\nwant it to have:\n%s", rootfile.DataFileName, got, want)
			}
		}
	})

	t.Run("refused where two features refer to it", func(t *testing.T) {
		m := merge(t, dev+`
resource "aws_vpn_gateway" "vpn" {
  vpc_id = data.aws_vpc.vpn.id
}

resource "aws_sns_topic" "alerts" {
  policy = data.aws_iam_policy_document.vpn.json
}

resource "aws_sns_topic" "audit" {
  policy = data.aws_iam_policy_document.vpn.json
}
`, policy)
		if !strings.Contains(m.reason, "data.aws_iam_policy_document.vpn is read in some environments only") {
			t.Errorf("reason %q", m.reason)
		}
	})
}

func TestMergeStackReferencesToEnvOnly(t *testing.T) {
	const prod = `resource "aws_kms_key" "queue" {
  description = "shop-prd-queue"
}

resource "aws_sqs_queue" "orders" {
  delay_seconds              = 0
  kms_master_key_id          = aws_kms_key.queue.arn
  name                       = "shop-prd-orders"
  visibility_timeout_seconds = 30
}
`
	ids := func(env string) map[string]string {
		return map[string]string{"aws_sqs_queue.orders": "shop-" + env + "-orders", "aws_kms_key.queue": "key-" + env}
	}

	t.Run("where other environments leave the argument null", func(t *testing.T) {
		m := mergeDevProd(t, `resource "aws_sqs_queue" "orders" {
  delay_seconds              = 0
  name                       = "shop-dev-orders"
  visibility_timeout_seconds = 30
}
`, prod, ids("dev"), ids("prd"))
		queue := resourceAt(t, m, "aws_sqs_queue.orders")
		a := attributeOf(t, queue, "kms_master_key_id")
		if a.kind != referenceValue || flatExpr(a.value) != "var.deploy_queue ? aws_kms_key.queue[0].arn : null" {
			t.Errorf("kms_master_key_id = %s (kind %d)", flatExpr(a.value), a.kind)
		}
	})

	t.Run("refused where another environment sets it", func(t *testing.T) {
		m := mergeDevProd(t, `resource "aws_sqs_queue" "orders" {
  delay_seconds              = 0
  kms_master_key_id          = "alias/aws/sqs"
  name                       = "shop-dev-orders"
  visibility_timeout_seconds = 30
}
`, prod, ids("dev"), ids("prd"))
		for _, address := range []string{"aws_sqs_queue.orders_dev", "aws_sqs_queue.orders_prod"} {
			if r := resourceAt(t, m, address); !strings.Contains(r.residue, "kms_master_key_id") {
				t.Errorf("%s: residue %q", address, r.residue)
			}
		}
		if key := resourceAt(t, m, "aws_kms_key.queue"); key.deploy == nil || key.residue != "" {
			t.Error("the key prod only has isn't counted")
		}
	})
}

func TestMergeStackDynamicBlocks(t *testing.T) {
	m := mergeDevProd(t, `resource "aws_dynamodb_table" "orders" {
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"
  name         = "shop-dev-orders"

  attribute {
    name = "id"
    type = "S"
  }
}

resource "aws_lambda_function" "api" {
  function_name = "shop-dev-api"
  handler       = "index.handler"
  runtime       = "nodejs22.x"
}
`, `resource "aws_dynamodb_table" "orders" {
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"
  name         = "shop-prd-orders"

  attribute {
    name = "id"
    type = "S"
  }

  attribute {
    name = "sk"
    type = "S"
  }
}

resource "aws_lambda_function" "api" {
  function_name = "shop-prd-api"
  handler       = "index.handler"
  runtime       = "nodejs22.x"

  tracing_config {
    mode = "Active"
  }
}
`, map[string]string{"aws_dynamodb_table.orders": "shop-dev-orders", "aws_lambda_function.api": "shop-dev-api"},
		map[string]string{"aws_dynamodb_table.orders": "shop-prd-orders", "aws_lambda_function.api": "shop-prd-api"})
	attribute := cty.Object(map[string]cty.Type{"name": cty.String, "type": cty.String})
	tracing := cty.Object(map[string]cty.Type{"mode": cty.String})
	for _, tc := range []struct {
		address, block, variable string
		single                   bool
		ty                       cty.Type
		fields                   []string
	}{
		{"aws_dynamodb_table.orders", "attribute", "orders_attribute", false, cty.List(attribute), []string{"name", "type"}},
		{"aws_lambda_function.api", "tracing_config", "api_tracing_config", true, tracing, []string{"mode"}},
	} {
		r := resourceAt(t, m, tc.address)
		if r.body == nil || len(r.body.blocks) != 1 {
			t.Fatalf("%s: residue %q", tc.address, r.residue)
		}
		b := r.body.blocks[0]
		if b.typ != tc.block || b.body != nil || b.variable == nil || b.variable.name != tc.variable || b.single != tc.single || !b.variable.ty.Equals(tc.ty) || !slices.Equal(b.fields, tc.fields) {
			t.Errorf("%s: block %s, variable %v, single %t, fields %v", tc.address, b.typ, b.variable, b.single, b.fields)
		}
	}
	// The block dev lacks is null there by default.
	if v := variableNamed(t, m, "api_tracing_config"); !v.hasDefault || v.defaultValue != nil {
		t.Error("api_tracing_config isn't null by default")
	}
	if _, ok := tfvarsOf(m, 0)["api_tracing_config"]; ok {
		t.Error("dev sets api_tracing_config")
	}
}

func TestMergeStackAzure(t *testing.T) {
	const subscription = "/subscriptions/00000000-0000-0000-0000-00000000000%d/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s"
	account := func(group, name string) string {
		return `resource "azurerm_storage_account" "logs" {
  account_tier        = "Standard"
  location            = "uksouth"
  name                = "` + name + `"
  resource_group_name = "` + group + `"
}
`
	}
	dev := testEnvRoot(t, "dev", nil, "00000000-0000-0000-0000-000000000001", account("rg-blue", "shopdevlogs"), map[string]string{"azurerm_storage_account.logs": fmt.Sprintf(subscription, 1, "rg-blue", "shopdevlogs")})
	dev.group = "rg-blue"
	prod := testEnvRoot(t, "prod", []string{"prd"}, "00000000-0000-0000-0000-000000000002", account("rg-green", "shopprdlogs"), map[string]string{"azurerm_storage_account.logs": fmt.Sprintf(subscription, 2, "rg-green", "shopprdlogs")})
	prod.group = "rg-green"
	m := mergeStack("azurerm", mergeProfiles["azurerm"], []envRoot{dev, prod}, nil)
	logs := resourceAt(t, m, "azurerm_storage_account.logs")
	if logs.rule != byLabel {
		t.Errorf("matched by %d, want by label", logs.rule)
	}
	if a := attributeOf(t, logs, "resource_group_name"); flatExpr(a.value) != `"${var.resource_group_name}"` {
		t.Errorf("resource_group_name = %s", flatExpr(a.value))
	}
	for e, want := range []map[string]string{
		{"env": `"dev"`, "logs_name": `"shopdevlogs"`, "resource_group_name": `"rg-blue"`},
		{"env": `"prd"`, "logs_name": `"shopprdlogs"`, "resource_group_name": `"rg-green"`},
	} {
		if got := tfvarsOf(m, e); !maps.Equal(got, want) {
			t.Errorf("%s tfvars = %v, want %v", m.envs[e], got, want)
		}
	}
	if len(m.data) != 0 {
		t.Errorf("data = %v, though no value holds the subscription", m.data)
	}
}

// TestLiteralTokens checks that a differing value a variable takes is one
// a var file can hold: a function call, such as the jsonencode of a
// policy, is evaluated into the value it gives, and one that can't be
// evaluated isn't taken.
func TestLiteralTokens(t *testing.T) {
	for _, tc := range []struct {
		src, want string
		ok        bool
	}{
		{src: `"plain"`, want: `"plain"`, ok: true},
		{src: `{ a = 1, b = ["x"] }`, want: `{ a = 1, b = ["x"] }`, ok: true},
		{src: `jsonencode({ Version = "2012-10-17", Statement = [{ Sid = "Dev" }] })`, want: `"{\"Statement\":[{\"Sid\":\"Dev\"}],\"Version\":\"2012-10-17\"}"`, ok: true},
		{src: `upper("x")`, ok: false},
	} {
		got, ok := literalTokens(hclfile.ParseExpr(tc.src))
		if ok != tc.ok {
			t.Errorf("%s: ok %v, want %v", tc.src, ok, tc.ok)
			continue
		}
		if ok && strings.Join(strings.Fields(string(got.Bytes())), " ") != tc.want {
			t.Errorf("%s: %s, want %s", tc.src, got.Bytes(), tc.want)
		}
	}
}
