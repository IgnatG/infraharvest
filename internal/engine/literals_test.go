// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
)

var targetGroupSchemas = &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
	"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
		"aws_lb_target_group": {Block: &tfjson.SchemaBlock{
			Attributes: map[string]*tfjson.SchemaAttribute{
				"enabled": {AttributeType: cty.Bool, Optional: true},
				"name":    {AttributeType: cty.String, Optional: true},
				"port":    {AttributeType: cty.Number, Optional: true},
				"ports":   {AttributeType: cty.List(cty.Number), Optional: true},
				"vpc_id":  {AttributeType: cty.String, Optional: true},
			},
			NestedBlocks: map[string]*tfjson.SchemaBlockType{
				"health_check": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
					"interval": {AttributeType: cty.Number, Optional: true},
				}}},
			},
		}},
	}},
}}

// Literals take the types of the arguments and module variables they set:
// numbers, strings and bools as the schema or the variable has them,
// element by element in lists. References and what has the right type
// already stay as they are.
func TestTypeLiterals(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, "alb.tf", `resource "aws_lb_target_group" "a" {
  enabled = "true"
  name    = 123
  port    = "80"
  ports   = ["80", 443]
  vpc_id  = aws_vpc.main.id
  health_check {
    interval = "30"
  }
}

module "svc" {
  source               = "./svc"
  bake_time_in_minutes = "0"
  names                = [1, "b"]
  other                = "1"
}
`)
	svc := filepath.Join(dir, ".terraform", "modules", "svc")
	if err := os.MkdirAll(svc, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, svc, "variables.tf", `variable "bake_time_in_minutes" {
  type = number
}

variable "names" {
  type = list(string)
}

variable "other" {
  type = any
}
`)
	if err := os.WriteFile(filepath.Join(dir, ".terraform", "modules", "modules.json"), []byte(`{"Modules":[{"Key":"","Source":"","Dir":"."},{"Key":"svc","Source":"./svc","Dir":".terraform/modules/svc"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := typeLiterals(dir, targetGroupSchemas); err != nil {
		t.Fatal(err)
	}
	got := squashed(readFile(t, dir, "alb.tf"))
	for _, want := range []string{
		"enabled = true", `name = "123"`, "port = 80", "ports = [80, 443]", "vpc_id = aws_vpc.main.id", "interval = 30",
		"bake_time_in_minutes = 0", `names = ["1", "b"]`, `other = "1"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("alb.tf misses %q:\n%s", want, got)
		}
	}
}
