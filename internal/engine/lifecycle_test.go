// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

const deploymentsGenerated = `resource "aws_api_gateway_deployment" "prod" {
  rest_api_id = "a1b2c3"
}

resource "aws_api_gateway_deployment" "kept" {
  rest_api_id = "a1b2c3"
  lifecycle {
    create_before_destroy = false
  }
}

resource "aws_api_gateway_stage" "prod" {
  rest_api_id = "a1b2c3"
}
`

// Every resource of the type gets create_before_destroy, with the reason
// above it, but one that sets it already, and no other type.
func TestSetLifecycles(t *testing.T) {
	t.Parallel()
	lifecycles := map[string]Lifecycle{"aws_api_gateway_deployment": {CreateBeforeDestroy: true, Reason: "a stage refers to it"}}
	got, changed := annotated(t, deploymentsGenerated, func(f *hclfile.File) bool { return setLifecycles(f, lifecycles) })
	if !changed {
		t.Fatal("want a change")
	}
	prod, rest, _ := strings.Cut(got, `resource "aws_api_gateway_deployment" "kept"`)
	kept, stage, _ := strings.Cut(rest, `resource "aws_api_gateway_stage" "prod"`)
	if !strings.Contains(prod, "# A stage refers to it.\n") || !strings.Contains(prod, "create_before_destroy = true") {
		t.Errorf("prod: no create_before_destroy with its reason:\n%s", got)
	}
	if strings.Contains(kept, "= true") || strings.Count(kept, "lifecycle") != 1 {
		t.Errorf("kept: its own setting changed:\n%s", got)
	}
	if strings.Contains(stage, "lifecycle") {
		t.Errorf("the stage got a lifecycle block:\n%s", got)
	}
}
