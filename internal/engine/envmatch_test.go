// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"slices"
	"strings"
	"testing"
)

// testEnvRoot returns environment env's root of generated, with tokens,
// its account and the import IDs by address.
func testEnvRoot(t *testing.T, env string, tokens []string, scope, generated string, ids map[string]string) envRoot {
	t.Helper()
	f, err := parseHCL("generated.tf", []byte(generated))
	if err != nil {
		t.Fatal(err)
	}
	return envRoot{env: env, tokens: tokens, scope: scope, file: f, importIDs: ids}
}

func TestNormaliserValue(t *testing.T) {
	n := newNormaliser(envRoot{env: "prod", tokens: []string{"prd", "prod"}, scope: "222222222222"})
	for _, tc := range []struct{ in, want string }{
		{"shop-prd-db", "shop-{env}-db"},
		{"shop-prod-db", "shop-{env}-db"},
		{"shop_prd.logs", "shop_{env}.logs"},
		// In any case.
		{"shop-PRD-db", "shop-{env}-db"},
		{"Shop-Prod-DB", "Shop-{env}-DB"},
		{"prd", "{env}"},
		{"/shop/prd/db_password", "/shop/{env}/db_password"},
		// Not whole words.
		{"production-db", "production-db"},
		{"shopprd", "shopprd"},
		// Not a template sequence.
		{"$prd", "$prd"},
		// The account, in an ARN, and not inside a longer number.
		{"arn:aws:iam::222222222222:role/shop-prd-ci", "arn:aws:iam::{account}:role/shop-{env}-ci"},
		{"1222222222222", "1222222222222"},
	} {
		if got := n.value(tc.in); got != tc.want {
			t.Errorf("value(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := n.denormalise("arn:aws:iam::{account}:role/shop-{env}-ci"); got != "arn:aws:iam::222222222222:role/shop-prd-ci" {
		t.Errorf("denormalise = %q", got)
	}
}

func TestNormaliserResourceGroup(t *testing.T) {
	const subscription = "00000000-0000-0000-0000-000000000001"
	id := "/subscriptions/" + subscription + "/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/logs"
	for _, tc := range []struct {
		name, group, want string
	}{
		{"a group named after no token becomes a placeholder", "rg-blue", "/subscriptions/{account}/resourceGroups/{resource_group}/providers/Microsoft.Storage/storageAccounts/logs"},
		{"a group named after the environment holds its token", "rg-shop-dev", "/subscriptions/{account}/resourceGroups/rg-shop-{env}/providers/Microsoft.Storage/storageAccounts/logs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newNormaliser(envRoot{env: "dev", scope: subscription, group: tc.group})
			if got := n.value(strings.Replace(id, "%s", tc.group, 1)); got != tc.want {
				t.Errorf("value = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormaliserLabel(t *testing.T) {
	n := newNormaliser(envRoot{env: "prod", tokens: []string{"prd", "prod"}, scope: "222222222222"})
	for _, tc := range []struct{ in, want string }{
		{"web", "web"},
		{"logs_prd", "logs"},
		{"shop_prod_web", "shop_web"},
		{"prd", "main"},
		{"r_222222222222_state", "state"},
		{"state_222222222222", "state"},
		{"prd_2", "r_2"},
	} {
		if got := n.label(tc.in); got != tc.want {
			t.Errorf("label(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestChooseToken(t *testing.T) {
	root := testEnvRoot(t, "prod", []string{"prd", "prod"}, "", `resource "aws_sqs_queue" "a" {
  name = "shop-prod-a"
  tags = {
    Name = "shop-prod-a"
  }
}

resource "aws_sqs_queue" "b" {
  name = "shop-prd-b"
}
`, nil)
	if n := newNormaliser(root); n.token != "prod" {
		t.Errorf("token = %q, want prod, the token the names use most", n.token)
	}
	if n := newNormaliser(envRoot{env: "prod", tokens: []string{"prd", "prod"}}); n.token != "prd" {
		t.Errorf("token = %q, want prd, the first", n.token)
	}
	upper := testEnvRoot(t, "prod", nil, "", `resource "aws_sqs_queue" "a" {
  name = "shop-PROD-a"
}

resource "aws_sqs_queue" "b" {
  name = "shop-PROD-b"
}

resource "aws_sqs_queue" "c" {
  name = "shop-prod-c"
}
`, nil)
	n := newNormaliser(upper)
	if n.token != "PROD" {
		t.Errorf("token = %q, want PROD, the way the names write it most", n.token)
	}
	if got := n.denormalise(n.envValue("shop-PROD-a")); got != "shop-PROD-a" {
		t.Errorf("denormalise = %q, want the name back", got)
	}
}

// matchRoots matches roots, as mergeStack does.
func matchRoots(roots []envRoot) []*stackResource {
	norms := make([]*normaliser, len(roots))
	for i, root := range roots {
		norms[i] = newNormaliser(root)
	}
	return matchResourcesWith(roots, norms, nil, nil)
}

// describeMatches describes each of resources: its address, how it was
// matched, and its resource in each environment of envs.
func describeMatches(resources []*stackResource, envs []string) []string {
	rules := []string{notMatched: "none", byImportID: "id", byLabel: "label", byReferences: "refs"}
	out := make([]string, 0, len(resources))
	for _, r := range resources {
		parts := []string{r.address(), rules[r.rule]}
		for e, m := range r.members {
			if m != nil {
				parts = append(parts, envs[e]+"="+m.block.address)
			}
		}
		out = append(out, strings.Join(parts, " "))
	}
	return out
}

func TestMatchResources(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dev, prod string
		devIDs    map[string]string
		prodIDs   map[string]string
		want      []string
	}{
		{
			name:    "by import ID, whatever the labels",
			dev:     `resource "aws_s3_bucket" "logs" { bucket = "shop-dev-logs" }`,
			prod:    `resource "aws_s3_bucket" "archive" { bucket = "shop-prd-logs" }`,
			devIDs:  map[string]string{"aws_s3_bucket.logs": "shop-dev-logs"},
			prodIDs: map[string]string{"aws_s3_bucket.archive": "shop-prd-logs"},
			want:    []string{"aws_s3_bucket.logs id dev=aws_s3_bucket.logs prod=aws_s3_bucket.archive"},
		},
		{
			name:    "by import ID with the account in an ARN",
			dev:     `resource "aws_iam_policy" "ci" { name = "ci" }`,
			prod:    `resource "aws_iam_policy" "deploy" { name = "ci" }`,
			devIDs:  map[string]string{"aws_iam_policy.ci": "arn:aws:iam::111111111111:policy/ci"},
			prodIDs: map[string]string{"aws_iam_policy.deploy": "arn:aws:iam::222222222222:policy/ci"},
			want:    []string{"aws_iam_policy.ci id dev=aws_iam_policy.ci prod=aws_iam_policy.deploy"},
		},
		{
			name: "by import ID, labelled main in one environment",
			dev:  `resource "aws_iam_role" "main" { name = "infraharvest-readonly" }`,
			prod: `resource "aws_iam_role" "alerts" { name = "alerts" }
resource "aws_iam_role" "infraharvest_readonly" { name = "infraharvest-readonly" }`,
			devIDs:  map[string]string{"aws_iam_role.main": "infraharvest-readonly"},
			prodIDs: map[string]string{"aws_iam_role.alerts": "alerts", "aws_iam_role.infraharvest_readonly": "infraharvest-readonly"},
			want: []string{
				"aws_iam_role.alerts none prod=aws_iam_role.alerts",
				"aws_iam_role.main id dev=aws_iam_role.main prod=aws_iam_role.infraharvest_readonly",
			},
		},
		{
			name: "module calls by the import IDs of the resources in them",
			dev: `module "iam_role" {
  source = "terraform-aws-modules/iam/aws//modules/iam-role"
  name   = "infraharvest-readonly"
}`,
			prod: `module "alerts" {
  source = "terraform-aws-modules/iam/aws//modules/iam-role"
  name   = "alerts"
}

module "infraharvest_readonly" {
  source = "terraform-aws-modules/iam/aws//modules/iam-role"
  name   = "infraharvest-readonly"
}`,
			devIDs: map[string]string{"module.iam_role.aws_iam_role.this[0]": "infraharvest-readonly"},
			prodIDs: map[string]string{
				"module.alerts.aws_iam_role.this[0]":                                  "alerts",
				`module.alerts.aws_iam_role_policy_attachment.this["ReadOnlyAccess"]`: "alerts/arn:aws:iam::aws:policy/ReadOnlyAccess",
				"module.infraharvest_readonly.aws_iam_role.this[0]":                   "infraharvest-readonly",
			},
			want: []string{
				"module.alerts none prod=module.alerts",
				"module.iam_role id dev=module.iam_role prod=module.infraharvest_readonly",
			},
		},
		{
			name: "module calls whose resources' IDs differ match by label",
			dev: `module "logs" {
  source = "terraform-aws-modules/s3-bucket/aws"
  bucket = "shop-dev-logs"
}`,
			prod: `module "logs" {
  source = "terraform-aws-modules/s3-bucket/aws"
  bucket = "shop-prd-archive"
}`,
			devIDs:  map[string]string{"module.logs.aws_s3_bucket.this[0]": "shop-dev-logs"},
			prodIDs: map[string]string{"module.logs.aws_s3_bucket.this[0]": "shop-prd-archive"},
			want:    []string{"module.logs label dev=module.logs prod=module.logs"},
		},
		{
			name:    "by label, where the IDs are the cloud's",
			dev:     `resource "aws_vpc" "main" { cidr_block = "10.0.0.0/16" }`,
			prod:    `resource "aws_vpc" "main" { cidr_block = "10.1.0.0/16" }`,
			devIDs:  map[string]string{"aws_vpc.main": "vpc-0aaa"},
			prodIDs: map[string]string{"aws_vpc.main": "vpc-0bbb"},
			want:    []string{"aws_vpc.main label dev=aws_vpc.main prod=aws_vpc.main"},
		},
		{
			name: "labels that collide once normalised match nothing",
			dev:  `resource "aws_s3_bucket" "logs" { bucket = "a" }`,
			prod: `resource "aws_s3_bucket" "logs" { bucket = "b" }
resource "aws_s3_bucket" "logs_prd" { bucket = "c" }`,
			devIDs:  map[string]string{"aws_s3_bucket.logs": "a"},
			prodIDs: map[string]string{"aws_s3_bucket.logs": "b", "aws_s3_bucket.logs_prd": "c"},
			want: []string{
				"aws_s3_bucket.logs none dev=aws_s3_bucket.logs",
				"aws_s3_bucket.logs_prod none prod=aws_s3_bucket.logs",
				"aws_s3_bucket.logs_prod_2 none prod=aws_s3_bucket.logs_prd",
			},
		},
		{
			name: "by references to matched resources",
			dev: `resource "aws_s3_bucket" "logs" { bucket = "shop-dev-logs" }
resource "aws_s3_bucket_versioning" "a" { bucket = aws_s3_bucket.logs.id }`,
			prod: `resource "aws_s3_bucket" "logs" { bucket = "shop-prd-logs" }
resource "aws_s3_bucket_versioning" "b" { bucket = aws_s3_bucket.logs.id }`,
			devIDs:  map[string]string{"aws_s3_bucket.logs": "shop-dev-logs", "aws_s3_bucket_versioning.a": "x1"},
			prodIDs: map[string]string{"aws_s3_bucket.logs": "shop-prd-logs", "aws_s3_bucket_versioning.b": "y1"},
			want: []string{
				"aws_s3_bucket.logs id dev=aws_s3_bucket.logs prod=aws_s3_bucket.logs",
				"aws_s3_bucket_versioning.a refs dev=aws_s3_bucket_versioning.a prod=aws_s3_bucket_versioning.b",
			},
		},
		{
			name: "not by references where two candidates are left",
			dev: `resource "aws_s3_bucket" "logs" { bucket = "shop-dev-logs" }
resource "aws_s3_bucket_policy" "a" { bucket = aws_s3_bucket.logs.id }
resource "aws_s3_bucket_policy" "b" { bucket = aws_s3_bucket.logs.id }`,
			prod: `resource "aws_s3_bucket" "logs" { bucket = "shop-prd-logs" }
resource "aws_s3_bucket_policy" "c" { bucket = aws_s3_bucket.logs.id }
resource "aws_s3_bucket_policy" "d" { bucket = aws_s3_bucket.logs.id }`,
			devIDs:  map[string]string{"aws_s3_bucket.logs": "shop-dev-logs"},
			prodIDs: map[string]string{"aws_s3_bucket.logs": "shop-prd-logs"},
			want: []string{
				"aws_s3_bucket.logs id dev=aws_s3_bucket.logs prod=aws_s3_bucket.logs",
				"aws_s3_bucket_policy.a none dev=aws_s3_bucket_policy.a",
				"aws_s3_bucket_policy.b none dev=aws_s3_bucket_policy.b",
				"aws_s3_bucket_policy.c none prod=aws_s3_bucket_policy.c",
				"aws_s3_bucket_policy.d none prod=aws_s3_bucket_policy.d",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roots := []envRoot{
				testEnvRoot(t, "dev", nil, "111111111111", tc.dev, tc.devIDs),
				testEnvRoot(t, "prod", []string{"prd"}, "222222222222", tc.prod, tc.prodIDs),
			}
			got := describeMatches(matchRoots(roots), []string{"dev", "prod"})
			if !slices.Equal(got, tc.want) {
				t.Errorf("matches:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}
