// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"
	"testing"
)

// moduleRoots are a dev and a prod root of a stack whose resources are in
// module calls: a bucket's, of the same release in both, versioned in dev
// only; an alerts topic's prod only has; and a cache's, of another
// release in each.
func moduleRoots(t *testing.T) []envRoot {
	t.Helper()
	root := func(env, token, account string, prod bool) envRoot {
		generated := `module "logs" {
  source        = "terraform-aws-modules/s3-bucket/aws"
  version       = "5.7.0"
  bucket        = "shop-` + token + `-logs"
  force_destroy = ` + map[bool]string{true: "false", false: "true"}[prod] + `
}

module "cache" {
  source  = "example.com/platform/cache/aws"
  version = "` + map[bool]string{true: "2.0.0", false: "1.0.0"}[prod] + `"
  name    = "shop-` + token + `-cache"
}

resource "aws_sqs_queue" "orders" {
  name   = "shop-` + token + `-orders"
  policy = module.logs.s3_bucket_arn
}
`
		ids := map[string]string{
			"aws_sqs_queue.orders":                      "https://sqs.eu-west-2.amazonaws.com/" + account + "/shop-" + token + "-orders",
			"module.logs.aws_s3_bucket.this[0]":         "shop-" + token + "-logs",
			"module.cache.aws_elasticache_cluster.this": "shop-" + token + "-cache",
		}
		files := map[string]string{"module.logs": "s3.tf", "module.cache": "cache.tf", "aws_sqs_queue.orders": "sqs.tf"}
		if prod {
			generated += `
module "alerts" {
  source = "../../modules/sns_topic_0123abcd"
  name   = "shop-prd-alerts"
}
`
			ids[`module.alerts.aws_sns_topic.this["alerts"]`] = "arn:aws:sns:eu-west-2:" + account + ":shop-prd-alerts"
			files["module.alerts"] = "sns.tf"
		} else {
			ids["module.logs.aws_s3_bucket_versioning.this[0]"] = "shop-dev-logs"
		}
		return awsEnvRoot(t, env, []string{token}, account, generated, ids, files, nil, "")
	}
	return []envRoot{root("dev", "dev", "111111111111", false), root("prod", "prd", "222222222222", true)}
}

func TestMergeStackModules(t *testing.T) {
	t.Parallel()
	m := mergeStack("aws", mergeProfiles["aws"], moduleRoots(t), rootSchemas)
	files, err := mergedRootFiles(m)
	if err != nil {
		t.Fatal(err)
	}
	s3 := string(files["s3.tf"])
	for _, want := range []string{
		`module "logs" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "5.7.0"`,
		"bucket        = local.logs_name",
		`force_destroy = var.env == "dev"`,
	} {
		if !strings.Contains(s3, want) {
			t.Errorf("s3.tf misses %q:\n%s", want, s3)
		}
	}
	if sqs := string(files["sqs.tf"]); !strings.Contains(sqs, "policy = module.logs.s3_bucket_arn") {
		t.Errorf("sqs.tf:\n%s", sqs)
	}
	sns := string(files["sns.tf"])
	if !strings.Contains(sns, "count = var.deploy_alerts ? 1 : 0") || !strings.Contains(sns, `source = "../../modules/sns_topic_0123abcd"`) {
		t.Errorf("sns.tf:\n%s", sns)
	}
	cache := string(files["cache.tf"])
	if !strings.Contains(cache, `module "cache_dev"`) || !strings.Contains(cache, `module "cache_prod"`) || !strings.Contains(cache, `version = "2.0.0"`) {
		t.Errorf("each environment's release of the cache module is its own:\n%s", cache)
	}
	imports := string(files[ImportsFileName])
	for _, want := range []string{
		"to = module.logs.aws_s3_bucket.this[0]",
		`for_each = [for k in [0] : k if contains(keys(var.import_ids), "module.logs.aws_s3_bucket_versioning.this[0]")]`,
		"to = module.logs.aws_s3_bucket_versioning.this[each.value]",
		`to = module.alerts[0].aws_sns_topic.this[each.value]`,
		`id = var.import_ids["module.cache_prod[0].aws_elasticache_cluster.this"]`,
	} {
		if !strings.Contains(imports, want) {
			t.Errorf("imports.tf misses %q:\n%s", want, imports)
		}
	}
	prod := string(files["vars/prod/imports.tfvars"])
	for _, want := range []string{`"module.alerts[0].aws_sns_topic.this[\"alerts\"]"`, `"module.logs.aws_s3_bucket.this[0]"`} {
		if !strings.Contains(prod, want) {
			t.Errorf("prod's imports.tfvars misses %s:\n%s", want, prod)
		}
	}
	if strings.Contains(prod, "aws_s3_bucket_versioning") {
		t.Errorf("prod imports dev's versioning:\n%s", prod)
	}
}

func TestForEachImport(t *testing.T) {
	t.Parallel()
	forEach, to, ok := forEachImport(`module.alerts[0].aws_sns_topic.this["alerts"]`)
	if !ok || to != "module.alerts[0].aws_sns_topic.this[each.value]" || !strings.HasPrefix(forEach, `[for k in ["alerts"] : k if`) {
		t.Errorf("got %q %q %v", forEach, to, ok)
	}
	if _, _, ok := forEachImport("module.logs.aws_s3_bucket.this"); ok {
		t.Errorf("an address without an instance key")
	}
	byInstance := map[string]*stackResource{"module.logs": {resourceType: moduleType, label: "logs"}}
	if r := moduleInstance(byInstance, "module.logs.aws_s3_bucket.this[0]"); r == nil || r.label != "logs" {
		t.Errorf("moduleInstance: %+v", r)
	}
}
