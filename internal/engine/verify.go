// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// Verify runs the verification gate on a directory Generate wrote, for
// example after people edit it: on a private copy of it without its
// backend (see verifyCopy), which newTF returns a runner for, initialised
// without a backend, so verifying needs no access to the state and
// changes nothing in the directory.
// Secret variables need values, from TF_VAR_ environment variables or a
// .auto.tfvars file; opts.Omit and opts.StateOnly are the provider's.
func Verify(ctx context.Context, newTF NewRunner, dir string, opts Options) (Gate, error) {
	copied, remove, err := verifyCopy(dir)
	if err != nil {
		return nil, err
	}
	defer remove()
	tf, err := newTF(copied)
	if err != nil {
		return nil, err
	}
	tf = withTimeout(tf, opts.CommandTimeout)
	if err := initTerraform(ctx, tf, tfexec.Backend(false)); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	return runGate(ctx, tf, copied, nil, opts, nil)
}
