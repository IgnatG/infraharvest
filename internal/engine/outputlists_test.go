// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"reflect"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
)

func TestOutputLists(t *testing.T) {
	trials := []*trial{{name: "vpc", call: &adapters.Call{Outputs: map[string]map[string]string{
		"aws_vpc.main":       {"id": "vpc_id"},
		"aws_subnet.a":       {"id": "public_subnets[0]", "arn": "public_subnet_arns[0]"},
		"aws_subnet.b":       {"id": "public_subnets[1]", "arn": "public_subnet_arns[1]"},
		"aws_subnet.private": {"id": "private_subnets[1]"},
	}}}}
	// private_subnets[0] isn't a member's: the list isn't all members.
	want := map[string]int{"module.vpc.public_subnets": 2, "module.vpc.public_subnet_arns": 2}
	if got := outputLists(trials); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

var listSchemas = &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
	"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
		"aws_autoscaling_group": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
			"vpc_zone_identifier": {AttributeType: cty.Set(cty.String), Optional: true},
		}}},
		"aws_elasticache_subnet_group": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
			"subnet_ids": {AttributeType: cty.List(cty.String), Optional: true},
		}}},
	}},
}}

// A list of every element of a module's list output is the output: in any
// order where the argument is a set, else only in the output's order.
func TestCollapseOutputLists(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_lb" "web" {
  security_groups = [aws_security_group.web.id]
  subnet_mapping {
    subnet_id = module.vpc.public_subnets[1]
  }
  subnet_mapping {
    subnet_id = module.vpc.public_subnets[0]
  }
}

resource "aws_lb" "fixed" {
  subnet_mapping {
    allocation_id = aws_eip.a.id
    subnet_id     = module.vpc.public_subnets[0]
  }
  subnet_mapping {
    subnet_id = module.vpc.public_subnets[1]
  }
}

resource "aws_autoscaling_group" "web" {
  vpc_zone_identifier = [module.vpc.private_subnets[1], module.vpc.private_subnets[0]]
}

resource "aws_db_subnet_group" "db" {
  subnet_ids = [module.vpc.private_subnets[0]]
}

resource "aws_elasticache_subnet_group" "cache" {
  subnet_ids = [module.vpc.public_subnets[1], module.vpc.public_subnets[0]]
}

resource "aws_eks_cluster" "main" {
  vpc_config {
    subnet_ids = [module.vpc.public_subnets[0], module.vpc.public_subnets[1]]
  }
}

resource "aws_network_acl" "mixed" {
  subnet_ids = [module.vpc.public_subnets[0], module.vpc.private_subnets[1]]
}

module "alb" {
  subnets = [module.vpc.public_subnets[1], module.vpc.public_subnets[0]]
  targets = [module.vpc.public_subnets[0], module.vpc.public_subnets[1]]
}
`)
	lists := map[string]int{"module.vpc.public_subnets": 2, "module.vpc.private_subnets": 2}
	fetched := 0
	schemas := func() (*tfjson.ProviderSchemas, error) {
		fetched++
		return listSchemas, nil
	}

	changed, err := collapseOutputLists(dir, lists, schemas)
	if err != nil || !changed {
		t.Fatalf("want lists collapsed, got %v, %v", changed, err)
	}
	if fetched != 1 {
		t.Errorf("want the schemas fetched once, got %d", fetched)
	}
	got := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{
		// The blocks become the argument that takes the subnets.
		`resource "aws_lb" "web" { security_groups = [aws_security_group.web.id] subnets = module.vpc.public_subnets }`,
		// A set, in another order.
		"vpc_zone_identifier = module.vpc.private_subnets",
		// In order, in a nested block and in a module call.
		"vpc_config { subnet_ids = module.vpc.public_subnets }",
		"targets = module.vpc.public_subnets",
		// A mapping that sets more than the subnet stays.
		"allocation_id = aws_eip.a.id",
		// Part of the output, a list in another order, a module input in
		// another order, and two outputs stay.
		"subnet_ids = [module.vpc.private_subnets[0]]",
		"subnet_ids = [module.vpc.public_subnets[1], module.vpc.public_subnets[0]]",
		"subnets = [module.vpc.public_subnets[1], module.vpc.public_subnets[0]]",
		"subnet_ids = [module.vpc.public_subnets[0], module.vpc.private_subnets[1]]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Count(got, "subnet_mapping") != 2 {
		t.Errorf("want only the fixed balancer's mappings:\n%s", got)
	}
}
