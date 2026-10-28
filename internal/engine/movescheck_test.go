// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

const movesCheckConfig = `resource "aws_subnet" "b" {
  cidr_block = "10.0.1.0/24"
}

resource "aws_subnet" "c" {
  count      = 2
  cidr_block = "10.0.2.0/24"
}

module "network" {
  source = "./modules/network"
}
`

func TestCheckMovesPasses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, movesCheckConfig)
	writeConfig(t, dir, rootfile.MovesFileName, `moved {
  from = aws_subnet.a
  to   = aws_subnet.b
}

moved {
  from = aws_subnet.c
  to   = aws_subnet.c[0]
}

moved {
  from = aws_vpc.main
  to   = module.network.aws_vpc.this
}

removed {
  from = aws_subnet.gone

  lifecycle {
    destroy = false
  }
}

removed {
  from = module.old
}

moved {
  from = aws_alb.web[1]
  to   = aws_alb.departed_web_1
}

removed {
  from = aws_alb.departed_web_1

  lifecycle {
    destroy = false
  }
}

moved {
  from = aws_subnet.c[2]
  to   = aws_subnet.departed_c_2
}

removed {
  from = aws_subnet.departed_c_2

  lifecycle {
    destroy = false
  }
}
`)
	check, err := checkMoves(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Passed || check.Name != gate.CheckMoves {
		t.Errorf("want the moves check passed, got %+v", check)
	}
}

func TestCheckMovesWithoutMoves(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, movesCheckConfig)
	check, err := checkMoves(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Passed || len(check.Details) != 0 {
		t.Errorf("want a root without moves passed, got %+v", check)
	}
}

func TestCheckMovesFindings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, moves, want string
	}{
		{"moved to an address not declared", `moved {
  from = aws_subnet.a
  to   = aws_subnet.missing
}
`, "moves.tf:1: moved block to aws_subnet.missing, which the root doesn't declare, and no removed block takes"},
		{"moved to an instance a removed block can't take", `moved {
  from = aws_subnet.a
  to   = aws_subnet.gone[0]
}

removed {
  from = aws_subnet.gone
}
`, "moved block to aws_subnet.gone[0], which the root doesn't declare"},
		{"moved into a module call not made", `moved {
  from = aws_subnet.a
  to   = module.missing.aws_subnet.a
}
`, "moved block to module.missing.aws_subnet.a, which the root doesn't declare"},
		{"moved from a resource still declared", `moved {
  from = aws_subnet.b
  to   = aws_subnet.c[0]
}
`, "moved block from aws_subnet.b, which the root still declares"},
		{"moved from a module call still made", `moved {
  from = module.network
  to   = aws_subnet.b
}
`, "moved block from module.network, which the root still declares"},
		{"moved to another type", `moved {
  from = aws_alb.a
  to   = aws_subnet.b
}
`, "moved block from aws_alb.a to aws_subnet.b changes the resource type"},
		{"moved between a module call and a resource", `moved {
  from = module.old
  to   = aws_subnet.b
}
`, "moved block from module.old to aws_subnet.b moves between a module call and a resource"},
		{"removed while still declared", `removed {
  from = aws_subnet.b

  lifecycle {
    destroy = false
  }
}
`, "removed block from aws_subnet.b, which the root still declares"},
		{"removed module call still made", `removed {
  from = module.network
}
`, "removed block from module.network, which the root still declares"},
		{"removed instance", `removed {
  from = aws_subnet.gone[0]
}
`, "removed block from aws_subnet.gone[0] names an instance"},
		{"moved and removed", `moved {
  from = aws_subnet.a
  to   = aws_subnet.b
}

removed {
  from = aws_subnet.b
}
`, "removed block from aws_subnet.b, which the root still declares"},
		{"two moves onto one address", `moved {
  from = aws_subnet.a
  to   = aws_subnet.b
}

moved {
  from = aws_subnet.x
  to   = aws_subnet.b
}
`, "more than one resource moves to aws_subnet.b"},
		{"a cycle", `moved {
  from = aws_subnet.x
  to   = aws_subnet.y
}

moved {
  from = aws_subnet.y
  to   = aws_subnet.x
}
`, "come back to"},
		{"a resource moved twice", `moved {
  from = aws_subnet.a
  to   = aws_subnet.b
}

moved {
  from = aws_subnet.a
  to   = aws_subnet.c[1]
}
`, "aws_subnet.a moves both to aws_subnet.b and to aws_subnet.c[1]"},
		{"not an address", `moved {
  from = "aws_subnet.a"
  to   = aws_subnet.b
}
`, "moved block's from isn't an address"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeConfig(t, dir, rootfile.GeneratedFileName, movesCheckConfig)
			writeConfig(t, dir, rootfile.MovesFileName, tc.moves)
			check, err := checkMoves(dir)
			if err != nil {
				t.Fatal(err)
			}
			if check.Passed || !strings.Contains(strings.Join(check.Details, "\n"), tc.want) {
				t.Errorf("want a failure with %q, got %+v", tc.want, check)
			}
		})
	}
}

// With state, a resource a moved block moves plans as a no-op at its new
// address, with where it was: the plan check counts it as no change, and
// not as an import.
func TestPlanCheckCountsAMoveAsNoChange(t *testing.T) {
	t.Parallel()
	moved := &tfjson.ResourceChange{
		Address: "aws_subnet.b", PreviousAddress: "aws_subnet.a", Type: "aws_subnet", Mode: tfjson.ManagedResourceMode,
		Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionNoop}},
	}
	imported := change("aws_vpc.main", tfjson.Actions{tfjson.ActionNoop}, nil, nil)
	check := gate.PlanCheck(&tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{moved, imported}}, nil, nil, nil, 1)
	if !check.Passed || len(check.Changes) != 0 {
		t.Errorf("want a move and an import passed, got %+v", check)
	}
}
