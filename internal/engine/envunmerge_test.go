// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeUnmergeRoot writes a root dev and prod share into a new directory
// and returns it.
func writeUnmergeRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"variables.tf": `variable "env" {
  type = string
}

variable "deploy_audit" {
  type    = bool
  default = false
}

variable "orders_timeout" {
  type = number
}

variable "queues" {
  type    = map(string)
  default = {}
}
`,
		LocalsFileName: `locals {
  naming_prefix  = "shop-${var.env}"
  aws_account_id = data.aws_caller_identity.current.account_id
  aws_region     = data.aws_region.current.region
  regional       = "${local.naming_prefix}-${data.aws_region.current.name}"
  other_region   = data.aws_region.other.region
}
`,
		DataFileName: `data "aws_caller_identity" "current" {}

data "aws_region" "current" {}

data "aws_partition" "current" {}

data "aws_caller_identity" "audit" {
  count = var.deploy_audit ? 1 : 0
}

data "aws_region" "other" {
  region = "us-east-1"
}

data "aws_vpc" "shop" {
  tags = { Name = "shop" }
}
`,
		"sqs.tf": `resource "aws_sqs_queue" "orders" {
  name                       = "${local.naming_prefix}-orders"
  visibility_timeout_seconds = var.orders_timeout
  tags                       = merge({ Team = "shop" }, { Env = var.env })
  policy                     = aws_iam_policy.orders.arn
}

resource "aws_sqs_queue" "audit" {
  count = var.deploy_audit ? 1 : 0

  name              = "${local.naming_prefix}-audit"
  kms_master_key_id = "alias/${data.aws_caller_identity.audit[0].account_id}"

  lifecycle {
    ignore_changes = [tags]
  }
}

resource "aws_sqs_queue" "this" {
  for_each = var.queues

  name = each.value
}

resource "aws_sqs_queue" "owned" {
  name              = "shop-${local.aws_account_id}"
  kms_master_key_id = "alias/${data.aws_caller_identity.current.account_id}"
  policy            = jsonencode({ Resource = "arn:${data.aws_partition.current.partition}:sqs:${local.aws_region}:${local.aws_account_id}:${local.regional}" })
}

resource "aws_sqs_queue" "elsewhere" {
  name = "${local.naming_prefix}-${local.other_region}"
}

resource "aws_sqs_queue" "peered" {
  name = "${local.naming_prefix}-${data.aws_vpc.shop.id}"
}
`,
		"vars/common.tfvars":        "orders_timeout = 60\n",
		"vars/dev/terraform.tfvars": "env            = \"dev\"\ndeploy_audit   = true\norders_timeout = 30\n",
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// devEnv is the environment of writeUnmergeRoot's tests.
var devEnv = UnmergeEnv{Name: "dev", Account: "111111111111", Region: "eu-west-2"}

// flatHCL returns s with its runs of white space made one space.
func flatHCL(s string) string { return strings.Join(strings.Fields(s), " ") }

// UnmergedBlocks gives a resource of a merged root as the environment's
// own root would have it: without its count, with the values of the
// environment's var files and the locals they give put in.
func TestUnmergedBlocks(t *testing.T) {
	t.Parallel()
	dir := writeUnmergeRoot(t)
	blocks, why, err := UnmergedBlocks(dir, devEnv, map[string]string{"aws_sqs_queue.orders": "q-orders", "aws_sqs_queue.audit[0]": "q-audit"})
	if err != nil || why != "" {
		t.Fatalf("%q %v", why, err)
	}
	orders := blocks["aws_sqs_queue.orders"]
	if orders.Address != "aws_sqs_queue.orders" || orders.File != "sqs.tf" {
		t.Errorf("orders: %+v", orders)
	}
	for _, want := range []string{`resource "aws_sqs_queue" "orders" {`, `name = "shop-dev-orders"`, "visibility_timeout_seconds = 30", `Env = "dev"`, `Team = "shop"`, "policy = aws_iam_policy.orders.arn"} {
		if !strings.Contains(flatHCL(orders.Text), want) {
			t.Errorf("orders misses %q:\n%s", want, orders.Text)
		}
	}
	if want := `import { to = aws_sqs_queue.orders id = "q-orders" }`; flatHCL(orders.Import) != want {
		t.Errorf("orders' import: %s, want %s", flatHCL(orders.Import), want)
	}
	audit := blocks["aws_sqs_queue.audit[0]"]
	if audit.Address != "aws_sqs_queue.audit" || strings.Contains(audit.Text, "count") || !strings.Contains(flatHCL(audit.Text), `name = "shop-dev-audit"`) || !strings.Contains(flatHCL(audit.Text), "lifecycle { ignore_changes = [tags] }") {
		t.Errorf("audit: %+v", audit)
	}
	// The counted data source of the account is its one instance.
	if !strings.Contains(flatHCL(audit.Text), `kms_master_key_id = "alias/111111111111"`) {
		t.Errorf("audit's key:\n%s", audit.Text)
	}
	if !strings.Contains(audit.Import, `"q-audit"`) || !strings.Contains(audit.Import, "aws_sqs_queue.audit\n") {
		t.Errorf("audit's import:\n%s", audit.Import)
	}

	for address, want := range map[string]string{
		`aws_sqs_queue.this["a"]`:          "for_each",
		"module.logs.aws_s3_bucket.this":   "module call",
		"aws_sqs_queue.missing":            "no block",
		"aws_sqs_queue.this[0].nested.bad": "isn't the address",
	} {
		if blocks, why, err := UnmergedBlocks(dir, devEnv, map[string]string{address: "id"}); err != nil || blocks != nil || !strings.Contains(why, want) {
			t.Errorf("%s: %v, %q, %v, want why with %q", address, blocks, why, err, want)
		}
	}
}

// The data sources of the environment's account and region, which the
// merged root's locals read, get the values the environment's own root
// has, as merged again they become those locals again; with the var
// files' values where a local reads both. A data source with arguments of
// its own, or what the run doesn't know of the environment, can't be
// told, and the reason names it.
func TestUnmergedBlocksReadingDataSources(t *testing.T) {
	t.Parallel()
	dir := writeUnmergeRoot(t)
	blocks, why, err := UnmergedBlocks(dir, devEnv, map[string]string{"aws_sqs_queue.owned": "q-owned"})
	if err != nil || why != "" {
		t.Fatalf("%q %v", why, err)
	}
	owned := flatHCL(blocks["aws_sqs_queue.owned"].Text)
	for _, want := range []string{`name = "shop-111111111111"`, `kms_master_key_id = "alias/111111111111"`, `arn:aws:sqs:eu-west-2:111111111111:shop-dev-eu-west-2`} {
		if !strings.Contains(owned, want) {
			t.Errorf("owned misses %q:\n%s", want, owned)
		}
	}
	if strings.Contains(owned, "data.") || strings.Contains(owned, "local.") {
		t.Errorf("owned reads what the environment's root lacks:\n%s", owned)
	}

	for _, c := range []struct {
		env              UnmergeEnv
		address, reading string
	}{
		{devEnv, "aws_sqs_queue.elsewhere", "data.aws_region.other.region"},
		{devEnv, "aws_sqs_queue.peered", "data.aws_vpc.shop.id"},
		{UnmergeEnv{Name: "dev", Region: "eu-west-2"}, "aws_sqs_queue.owned", "data.aws_caller_identity.current.account_id"},
		{UnmergeEnv{Name: "dev", Account: "111111111111"}, "aws_sqs_queue.owned", "data.aws_partition.current.partition, data.aws_region.current.name, data.aws_region.current.region"},
	} {
		blocks, why, err := UnmergedBlocks(dir, c.env, map[string]string{c.address: "id"})
		if err != nil || blocks != nil || !strings.Contains(why, "reads "+c.reading+", whose value") {
			t.Errorf("%s as %+v: %v, %q, %v, want why reading %s", c.address, c.env, blocks, why, err, c.reading)
		}
	}
}

// awsPartition tells the partitions of commercial, China and GovCloud
// regions, and no other.
func TestAWSPartition(t *testing.T) {
	t.Parallel()
	for region, want := range map[string][2]string{
		"eu-west-2":     {"aws", "amazonaws.com"},
		"cn-north-1":    {"aws-cn", "amazonaws.com.cn"},
		"us-gov-west-1": {"aws-us-gov", "amazonaws.com"},
		"us-iso-east-1": {"", ""},
		"":              {"", ""},
	} {
		if partition, suffix := awsPartition(region); partition != want[0] || suffix != want[1] {
			t.Errorf("%q: %q %q, want %q", region, partition, suffix, want)
		}
	}
}
