// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"
	"testing"
)

const (
	globalRoot   = "aws/123456789012/global"
	regionalRoot = "aws/123456789012/eu-west-2"
)

var otherRoots = []RootResource{
	{Type: "aws_iam_role", ID: "app", Name: "app", Root: globalRoot},
	{Type: "aws_iam_role", ID: "ci", Name: "ci", Root: globalRoot},
	{Type: "aws_iam_instance_profile", ID: "ci", Name: "ci", Root: globalRoot},
	{Type: "aws_vpc", ID: "vpc-0abc1234", Name: "shop", Root: regionalRoot},
	{Type: "aws_lb", ID: "arn:aws:elasticloadbalancing:eu-west-2:123456789012:loadbalancer/app/web/50dc6c495c0c9188", Name: "web", Root: regionalRoot},
	{Type: "aws_kms_key", ID: keyID, Name: "ebs", Root: regionalRoot},
	{Type: "aws_s3_bucket", ID: "assets-1", Name: "assets", Root: regionalRoot},
}

var awsARNs = map[string]ARN{
	"aws_iam_role": {Service: "iam", ResourceType: "role"},
	"aws_kms_key":  {Service: "kms", ResourceType: "key"},
	"aws_lb":       {Service: "elasticloadbalancing", ResourceType: "loadbalancer"},
}

// What another root manages, and no data source reads, comes from a
// variable that says where; names only where the argument names the type.
func TestAddRootVariables(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_lambda_function" "app" {
  function_name = "app"
  role          = "arn:aws:iam::123456789012:role/service-role/app"
}

resource "aws_instance" "web" {
  iam_instance_profile = "ci"
  root_block_device {
    kms_key_id = "arn:aws:kms:eu-west-2:123456789012:key/`+keyID+`"
  }
}

resource "aws_route53_zone" "private" {
  name = "internal.example.com"
  vpc {
    vpc_id = "vpc-0abc1234"
  }
}

resource "aws_security_group" "web" {
  vpc_id = "vpc-0abc1234"
}

resource "aws_cloudfront_distribution" "web" {
  comment = "app"
  origin {
    domain_name = "web-1234567890.eu-west-2.elb.amazonaws.com"
    origin_id   = "web"
  }
  origin {
    domain_name = "assets-1.s3.eu-west-2.amazonaws.com"
    origin_id   = "assets"
  }
}
`)
	writeConfig(t, dir, VariablesFileName, `variable "ebs_key_arn" {
  description = "A secret."
  type        = string
  sensitive   = true
}
`)

	edited, err := addRootVariables(dir, otherRoots, awsARNs, Names{"var.app_role_arn": true}, map[string]int{"aws_security_group.web": 2})
	if err != nil || len(edited) != 4 || edited["aws_security_group.web"] {
		t.Fatalf("want four resources edited, got %v, %v", edited, err)
	}

	got := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{
		// The ARN of a role, by its name after the path.
		"role = var.app_role_arn_2",
		// A name, where the argument names the type: the profile, not the role.
		"iam_instance_profile = var.ci_profile_name",
		"kms_key_id = var.ebs_key_arn_2",
		"vpc_id = var.shop_vpc_id",
		// A load balancer's DNS name, by its name and region.
		"domain_name = var.web_lb_dns_name",
		// A bucket's regional domain name, by the bucket's name.
		"domain_name = var.assets_bucket_regional_domain_name",
		// Names the arguments don't name the type of stay.
		`function_name = "app"`,
		`comment = "app"`,
		`origin_id = "web"`,
		// Skipped at level 2.
		`resource "aws_security_group" "web" { vpc_id = "vpc-0abc1234" }`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	variables := squashed(readFile(t, dir, VariablesFileName))
	for _, want := range []string{
		`variable "ebs_key_arn" { description = "A secret." type = string sensitive = true }`,
		`variable "app_role_arn_2" { description = "The ARN of aws_iam_role.app, which the root ` + globalRoot + ` manages. The default is its value when this root was imported." type = string default = "arn:aws:iam::123456789012:role/service-role/app" }`,
		`variable "web_lb_dns_name" { description = "The DNS name of aws_lb.web, which the root ` + regionalRoot + ` manages.`,
		`variable "shop_vpc_id" {`,
	} {
		if !strings.Contains(variables, want) {
			t.Errorf("variables.tf misses %q:\n%s", want, variables)
		}
	}
	if n := strings.Count(variables, "variable "); n != 7 {
		t.Errorf("want 7 variables, got %d:\n%s", n, variables)
	}
}

// AWS-managed CloudFront policies and the EBS key are read by name.
func TestAddLookupsOfWhatAWSManages(t *testing.T) {
	t.Parallel()
	keyARN := "arn:aws:kms:eu-west-2:123456789012:key/" + keyID
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_cloudfront_distribution" "web" {
  default_cache_behavior {
    cache_policy_id          = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad"
    origin_request_policy_id = "216adef6-5c7f-47e4-b989-5492eafa07d3"
  }
  ordered_cache_behavior {
    cache_policy_id            = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad"
    response_headers_policy_id = "00000000-0000-0000-0000-000000000000"
  }
}

resource "aws_instance" "web" {
  root_block_device {
    kms_key_id = "`+keyARN+`"
  }
}

resource "aws_ebs_volume" "data" {
  kms_key_id = "`+keyARN+`"
}

resource "aws_rds_cluster" "db" {
  kms_key_id = "`+keyARN+`"
}
`)
	writeConfig(t, dir, DataFileName, dataFileHeader+`
data "aws_vpc" "vpc_0abc1234" {
  id = "vpc-0abc1234"
}
`)

	edited, err := addLookups(dir, nil, nil)
	if err != nil || len(edited) != 3 || edited["aws_rds_cluster.db"] {
		t.Fatalf("want three resources edited, got %v, %v", edited, err)
	}
	got := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{
		"origin_request_policy_id = data.aws_cloudfront_origin_request_policy.all_viewer.id",
		// A policy AWS doesn't manage, and a key of a database, stay.
		`response_headers_policy_id = "00000000-0000-0000-0000-000000000000"`,
		`resource "aws_rds_cluster" "db" { kms_key_id = "` + keyARN + `" }`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Count(got, "cache_policy_id = data.aws_cloudfront_cache_policy.caching_disabled.id") != 2 {
		t.Errorf("want both cache policies read by name:\n%s", got)
	}
	if strings.Count(got, "kms_key_id = data.aws_kms_alias.ebs.target_key_arn") != 2 {
		t.Errorf("want the volumes' keys read by alias:\n%s", got)
	}
	data := squashed(readFile(t, dir, DataFileName))
	for _, want := range []string{
		`data "aws_vpc" "vpc_0abc1234" { id = "vpc-0abc1234" }`,
		`data "aws_cloudfront_cache_policy" "caching_disabled" { name = "Managed-CachingDisabled" }`,
		`data "aws_cloudfront_origin_request_policy" "all_viewer" { name = "Managed-AllViewer" }`,
		`data "aws_kms_alias" "ebs" { name = "alias/aws/ebs" }`,
	} {
		if !strings.Contains(data, want) {
			t.Errorf("data.tf misses %q:\n%s", want, data)
		}
	}
	if n := strings.Count(data, "data \""); n != 4 {
		t.Errorf("want 4 data sources, got %d:\n%s", n, data)
	}
}

// A Google network in the global root is read by a data source in the
// regional roots, which name it by its self link on either API host; a
// resource whose plan that changes gets a variable instead.
func TestAddRootVariablesReadsGoogleNetworks(t *testing.T) {
	t.Parallel()
	const link = "https://www.googleapis.com/compute/v1/projects/acme/global/networks/main"
	others := []RootResource{
		{Type: "google_compute_network", ID: "projects/acme/global/networks/main", Name: "main", Root: "google/acme/global"},
		{Type: "google_compute_firewall", ID: "projects/acme/global/firewalls/allow", Name: "allow", Root: "google/acme/global"},
	}
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "google_compute_subnetwork" "app" {
  name    = "app"
  network = "`+link+`"
}

resource "google_compute_router" "nat" {
  network = "https://compute.googleapis.com/compute/v1/projects/acme/global/networks/main"
}

resource "google_compute_router" "other" {
  network = "`+link+`"
}

resource "google_compute_subnetwork" "elsewhere" {
  network = "https://www.googleapis.com/compute/v1/projects/acme/global/networks/elsewhere"
}
`)
	edited, err := addRootVariables(dir, others, nil, nil, map[string]int{"google_compute_router.other": 1})
	if err != nil || len(edited) != 3 {
		t.Fatalf("want three resources edited, got %v, %v", edited, err)
	}
	got := squashed(readFile(t, dir, GeneratedFileName))
	if strings.Count(got, "network = data.google_compute_network.main.self_link") != 2 {
		t.Errorf("want the subnetwork and the router to read the network:\n%s", got)
	}
	for _, want := range []string{
		"network = var.main_network_self_link",
		"networks/elsewhere",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if data := squashed(readFile(t, dir, DataFileName)); !strings.Contains(data, `data "google_compute_network" "main" { name = "main" project = "acme" }`) || strings.Count(data, "data \"") != 1 {
		t.Errorf("data.tf:\n%s", data)
	}
	if variables := squashed(readFile(t, dir, VariablesFileName)); !strings.Contains(variables, `variable "main_network_self_link" { description = "The self link of google_compute_network.main, which the root google/acme/global manages.`) {
		t.Errorf("variables.tf:\n%s", variables)
	}
}
