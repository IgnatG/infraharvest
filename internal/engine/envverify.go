// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// VerifyMerged runs the verification gate on the root environments share
// in root as environment env plans it, with a Terraform runner newTF
// returns with env's credentials, on a private copy of the root without
// its backend (see verifyCopy): with its var files, vars/common.tfvars,
// vars/<env>/terraform.tfvars and, until it is deleted after the first
// apply, vars/<env>/imports.tfvars, whose every import the plan must
// make. It initialises the copy without a backend, as Verify does.
// Secret variables need values, from TF_VAR_ environment variables or a
// .auto.tfvars file.
func VerifyMerged(ctx context.Context, newTF NewRunner, root, env string, opts Options) (Gate, error) {
	dir, remove, err := verifyCopy(root)
	if err != nil {
		return nil, err
	}
	defer remove()
	tf, err := newTF(dir)
	if err != nil {
		return nil, err
	}
	tf = withTimeout(tf, opts.CommandTimeout)
	if err := initTerraform(ctx, tf, tfexec.Backend(false)); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	vars, _, err := mergedVarFiles(dir, env)
	if err != nil {
		return nil, err
	}
	expected := 0
	if _, err := os.Stat(filepath.Join(dir, ImportsFileName)); err == nil {
		ids, err := MergedImportIDs(dir, env)
		if err != nil {
			return nil, err
		}
		expected = len(ids)
	}
	return gateWith(ctx, tf, dir, opts, func() (Check, []string, error) {
		p, _, diags, err := showPlanWithSummary(ctx, tf, vars)
		if err != nil {
			return Check{}, nil, err
		}
		check := planCheck(p, diags, nil, opts.StateOnly, expected)
		check.Details = append([]string{"environment " + env}, check.Details...)
		return check, sensitiveValues(p), nil
	}, nil)
}
