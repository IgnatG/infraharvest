// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// refineFailed starts the warning of a root whose Refine edits were rolled
// back.
const refineFailed = "the edits that refine the root (with --cfn-stack, a CloudFormation stack's template carried into it) "

// refine runs opts.Refine on dir (see Options.Refine), and returns the
// root's files as they were before it, for gateRefined to roll the edits
// back with, or nil when there is nothing to roll back, as where Refine
// changed none of them. Refine is an optional enhancement: should it fail,
// or should the schemas it needs not be read, its edits are rolled back
// and a warning says why, and the root goes on without them.
func refine(ctx context.Context, tf tfrun.Terraform, dir string, result *Result, opts Options) (map[string][]byte, error) {
	before, err := rootFileContents(dir)
	if err != nil {
		return nil, err
	}
	schemas, err := tf.ProvidersSchema(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		result.Warnings = append(result.Warnings, refineFailed+"didn't run, as terraform providers schema failed: "+err.Error())
		return nil, nil
	}
	warnings, err := opts.Refine(ctx, dir, result.Addresses, schemas)
	if err != nil {
		if restoreErr := restoreRoot(dir, before); restoreErr != nil {
			return nil, errors.Join(err, restoreErr)
		}
		result.Warnings = append(result.Warnings, refineFailed+"failed, and were rolled back: "+err.Error())
		return nil, nil
	}
	result.Warnings = append(result.Warnings, warnings...)
	after, err := rootFileContents(dir)
	if err != nil {
		return nil, err
	}
	if maps.EqualFunc(before, after, bytes.Equal) {
		return nil, nil
	}
	return before, nil
}

// gateRefined runs the gate on a root Refine edited, and, should it fail,
// rolls the edits back and runs it again. When the root passes without
// them, it goes on as it was before them, with a warning, and the Refine
// warnings go, as they describe edits no longer there: an optional
// enhancement never fails a root that would pass. When it fails without
// them too, the edits aren't what fails it: they are put back, and the
// root goes on with the gate that failed with them, and Refine's warnings.
// known, if set, has the values of the secrets Generate found (see
// secret.Values).
func gateRefined(ctx context.Context, tf tfrun.Terraform, dir string, result *Result, opts Options, known *secret.Values, before map[string][]byte, refineWarnings int) error {
	checks, err := runGate(ctx, tf, dir, result.Secrets, opts, known)
	if err != nil {
		return err
	}
	if before == nil || checks.Passed() {
		result.Gate = checks
		return nil
	}
	refined, err := rootFileContents(dir)
	if err != nil {
		return err
	}
	if err := restoreRoot(dir, before); err != nil {
		return err
	}
	without, err := runGate(ctx, tf, dir, result.Secrets, opts, known)
	if err != nil {
		return err
	}
	if !without.Passed() {
		result.Gate = checks
		return restoreRoot(dir, refined)
	}
	result.Warnings = append(result.Warnings[:refineWarnings], refineFailed+"made its gate fail ("+failedChecks(checks)+"), so they were rolled back")
	result.Gate = without
	return nil
}

// failedChecks names the checks that failed.
func failedChecks(checks gate.Gate) string {
	var failed []string
	for _, c := range checks {
		if !c.Passed {
			failed = append(failed, c.Name)
		}
	}
	return strings.Join(failed, ", ")
}

// restoreRoot puts the regular files directly in dir back as files has
// them (see rootFileContents): a file not in it is removed, and one whose
// content changed written back, with the mode it has.
func restoreRoot(dir string, files map[string][]byte) error {
	now, err := rootFileContents(dir)
	if err != nil {
		return err
	}
	for name := range now {
		if _, ok := files[name]; !ok {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	for name, content := range files {
		if have, ok := now[name]; ok && bytes.Equal(have, content) {
			continue
		}
		path := filepath.Join(dir, name)
		mode := fs.FileMode(0o644)
		if info, err := os.Stat(path); err == nil {
			mode = info.Mode().Perm()
		}
		if err := fsutil.WriteFile(path, content, mode); err != nil {
			return err
		}
	}
	return nil
}
