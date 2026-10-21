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
  naming_prefix = "shop-${var.env}"
  account_id    = data.aws_caller_identity.current.account_id
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

  name = "${local.naming_prefix}-audit"

  lifecycle {
    ignore_changes = [tags]
  }
}

resource "aws_sqs_queue" "this" {
  for_each = var.queues

  name = each.value
}

resource "aws_sqs_queue" "owned" {
  name = "shop-${local.account_id}"
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

// UnmergedBlocks gives a resource of a merged root as the environment's
// own root would have it: without its count, with the values of the
// environment's var files and the locals they give put in.
func TestUnmergedBlocks(t *testing.T) {
	t.Parallel()
	dir := writeUnmergeRoot(t)
	blocks, why, err := UnmergedBlocks(dir, "dev", map[string]string{"aws_sqs_queue.orders": "q-orders", "aws_sqs_queue.audit[0]": "q-audit"})
	if err != nil || why != "" {
		t.Fatalf("%q %v", why, err)
	}
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	orders := blocks["aws_sqs_queue.orders"]
	if orders.Address != "aws_sqs_queue.orders" || orders.File != "sqs.tf" {
		t.Errorf("orders: %+v", orders)
	}
	for _, want := range []string{`resource "aws_sqs_queue" "orders" {`, `name = "shop-dev-orders"`, "visibility_timeout_seconds = 30", `Env = "dev"`, `Team = "shop"`, "policy = aws_iam_policy.orders.arn"} {
		if !strings.Contains(flat(orders.Text), want) {
			t.Errorf("orders misses %q:\n%s", want, orders.Text)
		}
	}
	if want := `import { to = aws_sqs_queue.orders id = "q-orders" }`; flat(orders.Import) != want {
		t.Errorf("orders' import: %s, want %s", flat(orders.Import), want)
	}
	audit := blocks["aws_sqs_queue.audit[0]"]
	if audit.Address != "aws_sqs_queue.audit" || strings.Contains(audit.Text, "count") || !strings.Contains(flat(audit.Text), `name = "shop-dev-audit"`) || !strings.Contains(flat(audit.Text), "lifecycle { ignore_changes = [tags] }") {
		t.Errorf("audit: %+v", audit)
	}
	if !strings.Contains(audit.Import, `"q-audit"`) || !strings.Contains(audit.Import, "aws_sqs_queue.audit\n") {
		t.Errorf("audit's import:\n%s", audit.Import)
	}

	for address, want := range map[string]string{
		`aws_sqs_queue.this["a"]`:          "for_each",
		"aws_sqs_queue.owned":              "don't tell",
		"module.logs.aws_s3_bucket.this":   "module call",
		"aws_sqs_queue.missing":            "no block",
		"aws_sqs_queue.this[0].nested.bad": "isn't the address",
	} {
		if blocks, why, err := UnmergedBlocks(dir, "dev", map[string]string{address: "id"}); err != nil || blocks != nil || !strings.Contains(why, want) {
			t.Errorf("%s: %v, %q, %v, want why with %q", address, blocks, why, err, want)
		}
	}
}
