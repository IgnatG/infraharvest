// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
)

// omitResult writes config, leaves out what omit names, and returns what
// is left.
func omitResult(t *testing.T, config string, omit map[string][]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), GeneratedFileName)
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OmitArguments(path, omit); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

const targetGroupsGenerated = `resource "aws_lb_target_group" "web" {
  name     = "web"
  protocol = "HTTP"
  stickiness {
    cookie_duration = 86400
    enabled         = false
    type            = "lb_cookie"
  }
}

resource "aws_lb_target_group" "api" {
  name     = "api"
  protocol = "HTTP"
  stickiness {
    cookie_duration = 3600
    enabled         = true
    type            = "lb_cookie"
  }
}
`

// A block goes where a condition on its own arguments holds, and only
// there: the disabled stickiness goes, the enabled one stays.
func TestOmitBlockWhenItsArgumentsHold(t *testing.T) {
	got := omitResult(t, targetGroupsGenerated, map[string][]string{"aws_lb_target_group": {"stickiness when stickiness.enabled=false"}})
	web, api, _ := strings.Cut(got, `resource "aws_lb_target_group" "api"`)
	if strings.Contains(web, "stickiness") {
		t.Errorf("web's disabled stickiness not left out:\n%s", got)
	}
	if !strings.Contains(api, "stickiness {") || !strings.Contains(api, "cookie_duration = 3600") {
		t.Errorf("api's stickiness left out:\n%s", got)
	}
}

const instanceGenerated = `resource "aws_instance" "app" {
  availability_zone = "eu-west-2a"
  instance_type     = "t3.micro"
  subnet_id         = "subnet-1"
  root_block_device {
    iops        = 3000
    throughput  = 125
    volume_size = 8
    volume_type = "gp3"
  }
  ebs_block_device {
    device_name = "/dev/sdf"
    iops        = 3000
    volume_type = "io2"
  }
}
`

// a.b leaves b out of every block a where the conditions hold, the
// conditions starting with a referring to that block, the others to the
// resource.
func TestOmitNestedArguments(t *testing.T) {
	got := omitResult(t, instanceGenerated, map[string][]string{"aws_instance": {
		"availability_zone when subnet_id",
		"root_block_device.iops=3000 when root_block_device.volume_type=gp3",
		"root_block_device.throughput=125 when root_block_device.volume_type=gp3",
		"ebs_block_device.iops=3000 when ebs_block_device.volume_type=gp3",
		"instance_type when !subnet_id",
	}})
	for _, gone := range []string{"availability_zone", "throughput"} {
		if strings.Contains(got, gone) {
			t.Errorf("%s not left out:\n%s", gone, got)
		}
	}
	root, ebs, _ := strings.Cut(got, "ebs_block_device")
	if strings.Contains(root, "iops") {
		t.Errorf("the gp3 root volume's iops not left out:\n%s", got)
	}
	for _, kept := range []string{"iops        = 3000", "volume_type = \"io2\""} {
		if !strings.Contains(ebs, kept) {
			t.Errorf("the io2 volume lost %q:\n%s", kept, got)
		}
	}
	if !strings.Contains(got, `"t3.micro"`) {
		t.Errorf("instance_type left out although subnet_id is set:\n%s", got)
	}
}

const policiesGenerated = `resource "aws_sns_topic" "alerts" {
  name = "alerts"
  policy = jsonencode({
    Id = "__default_policy_ID"
    Statement = [{
      Action    = ["SNS:Publish", "SNS:Subscribe"]
      Effect    = "Allow"
      Principal = { AWS = "*" }
      Resource  = "arn:aws:sns:eu-west-2:123456789012:alerts"
    }]
    Version = "2008-10-17"
  })
}

resource "aws_sns_topic" "events" {
  name = "events"
  policy = jsonencode({
    Id = "__default_policy_ID"
    Statement = [{
      Action    = ["SNS:Publish", "SNS:Subscribe"]
      Effect    = "Allow"
      Principal = { AWS = "*" }
      Resource  = "arn:aws:sns:eu-west-2:123456789012:alerts"
    }]
    Version = "2008-10-17"
  })
}
`

// A JSON value compares as JSON, with arrays in any order, {*} matching
// any text and {name} the resource's name: the topic whose policy names
// another topic keeps it.
func TestOmitJSONValue(t *testing.T) {
	pattern := `policy={"Version":"2008-10-17","Id":"__default_policy_ID","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":["SNS:Subscribe","SNS:Publish"],"Resource":"arn:{*}:sns:{*}:{*}:{name}"}]}`
	got := omitResult(t, policiesGenerated, map[string][]string{"aws_sns_topic": {pattern}})
	alerts, events, _ := strings.Cut(got, `resource "aws_sns_topic" "events"`)
	if strings.Contains(alerts, "policy") {
		t.Errorf("alerts' default policy not left out:\n%s", got)
	}
	if !strings.Contains(events, "policy") {
		t.Errorf("events' policy, which names another topic, left out:\n%s", got)
	}
}

// Entries under a resource's address apply to that resource only.
func TestOmitByAddress(t *testing.T) {
	omit := withResourceOmissions(map[string][]string{"aws_lb_target_group": {"name"}}, []Import{
		{Type: "aws_lb_target_group", Name: "api", ID: "arn:api", Omit: []string{"protocol"}},
		{Type: "aws_lb_target_group", Name: "web", ID: "arn:web"},
	})
	got := omitResult(t, targetGroupsGenerated, omit)
	web, api, _ := strings.Cut(got, `resource "aws_lb_target_group" "api"`)
	if strings.Contains(got, "name ") {
		t.Errorf("the type's omission not applied:\n%s", got)
	}
	if strings.Contains(api, "protocol") || !strings.Contains(web, "protocol") {
		t.Errorf("api's own omission applied elsewhere or not at all:\n%s", got)
	}
}

// The standards check flags a nested argument at the value it is left
// out for, where its conditions hold.
func TestCheckOmittedNested(t *testing.T) {
	f := writeConfig(t, t.TempDir(), GeneratedFileName, targetGroupsGenerated)
	var findings []string
	for _, b := range f.syntax.Blocks {
		checkOmitted(b, map[string][]string{"aws_lb_target_group": {"stickiness when stickiness.enabled=false", "stickiness.cookie_duration=3600"}}, func(hcl.Range) string { return "x" }, func(rule, _ string, _ ...any) {
			findings = append(findings, rule)
		})
	}
	if len(findings) != 2 {
		t.Errorf("got %d findings, want 2: web's stickiness block and api's cookie_duration", len(findings))
	}
}

const vaultsGenerated = `resource "azurerm_key_vault" "open" {
  access_policy = []
  name          = "open"
  network_acls {
    bypass                     = "AzureServices"
    default_action             = "Allow"
    ip_rules                   = []
    virtual_network_subnet_ids = []
  }
}

resource "azurerm_key_vault" "closed" {
  access_policy = [{
    object_id = "00000000-0000-0000-0000-000000000003"
  }]
  name = "closed"
  network_acls {
    bypass                     = "AzureServices"
    default_action             = "Allow"
    ip_rules                   = ["203.0.113.0/24"]
    virtual_network_subnet_ids = []
  }
}
`

// An empty list compares as JSON, and a block whose required arguments
// are at Azure's values and whose rules are empty goes: the open vault
// loses both, the vault with a policy and an IP rule keeps them.
func TestOmitEmptyValuesAndOpenNetworkRules(t *testing.T) {
	got := omitResult(t, vaultsGenerated, map[string][]string{"azurerm_key_vault": {
		"access_policy=[]",
		"network_acls when network_acls.bypass=AzureServices and network_acls.default_action=Allow and network_acls.ip_rules=[] and network_acls.virtual_network_subnet_ids=[]",
	}})
	open, closed, _ := strings.Cut(got, `resource "azurerm_key_vault" "closed"`)
	if strings.Contains(open, "access_policy") || strings.Contains(open, "network_acls") {
		t.Errorf("open's empty policies or open network rules not left out:\n%s", got)
	}
	if !strings.Contains(closed, "access_policy = [{") || !strings.Contains(closed, `ip_rules                   = ["203.0.113.0/24"]`) {
		t.Errorf("closed's policies or rules left out:\n%s", got)
	}
}
