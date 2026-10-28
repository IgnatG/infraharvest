// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// Verify runs the verification gate on a directory Generate wrote, for
// example after people edit it: on a private copy of it without its
// backend (see verifyCopy), which newTF returns a runner for, initialised
// without a backend, so verifying needs no access to the state and
// changes nothing in the directory.
// Secret variables need values, from TF_VAR_ environment variables or a
// .auto.tfvars file; opts.Omit and opts.StateOnly are the provider's.
func Verify(ctx context.Context, newTF NewRunner, dir string, opts Options) (gate.Gate, error) {
	copied, remove, err := verifyCopy(dir)
	if err != nil {
		return nil, err
	}
	defer remove()
	tf, err := newTF(copied)
	if err != nil {
		return nil, err
	}
	tf = tfrun.WithTimeout(tf, opts.CommandTimeout)
	if err := tfrun.Init(ctx, tf, tfexec.Backend(false)); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	return runGate(ctx, tf, copied, nil, opts, nil)
}

// runGate runs the import gate on the directory as Generate leaves it:
// every check of the gate (see fullGate), with a plan that imports each
// resource an import block names and changes nothing else (see
// gate.PlanCheck). Secret variables Terraform doesn't show get placeholder
// values in the plan, so the arguments they set may change; so may the
// arguments opts.StateOnly names per type, which providers keep only in
// state and import can't set. known, if set, has the values of the
// secrets Generate found (see secret.Values).
func runGate(ctx context.Context, tf tfrun.Terraform, dir string, secrets []secret.Variable, opts Options, known *secret.Values) (gate.Gate, error) {
	expected, err := countImportBlocks(dir)
	if err != nil {
		return nil, err
	}
	policy := fullGate(opts)
	policy.Plan = func() (gate.Check, []string, error) {
		p, _, diags, err := tfrun.ShowPlan(ctx, tf, append(secret.Placeholders(secrets), known.Options()...))
		if err != nil {
			return gate.Check{}, nil, err
		}
		return gate.PlanCheck(p, diags, secrets, opts.StateOnly, expected), append(secret.InPlan(p), secret.KnownValues(secrets)...), nil
	}
	checks, _, err := policy.Run(ctx, tf, dir)
	return checks, err
}

// countImportBlocks counts the import blocks in dir's configuration: how
// many resources its plan must import.
func countImportBlocks(dir string) (int, error) {
	files, err := hclfile.ConfigFiles(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			if b.Type == "import" {
				n++
			}
		}
	}
	return n, nil
}

// fullGate returns the policy that runs every check of the gate, to the
// output standard, with the omissions and the organisation's own modules
// of opts (see checkStandards), and with the scanners and scanner slots of
// opts; its plan is still to be set.
func fullGate(opts Options) gate.Policy {
	own := ownModuleDirs(opts.Adapters)
	return gate.Policy{
		Standards: func(dir string) (gate.Check, error) { return checkStandards(dir, opts.Omit, own) },
		Moves:     gate.MovesCheck,
		Scanners:  opts.Scanners,
		Slots:     opts.ScannerSlots,
	}
}
