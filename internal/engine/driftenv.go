// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// workspaceStateDir holds the local state of each workspace but the
// default one, in terraform.tfstate.d/<workspace>/terraform.tfstate.
const workspaceStateDir = "terraform.tfstate.d"

// pullDriftState returns the state of the root in dir, whose backend is
// backend (see rootBackend), pulled through tf once initialised with its
// backend. For environment env of a root environments share, the backend
// is initialised with env's settings, vars/<env>/backend.hcl; a root
// without a backend keeps each environment's state in the workspace named
// after it, which is read as it is: "" where env has none yet.
func pullDriftState(ctx context.Context, tf DriftTerraform, dir, backend, env string) (string, error) {
	if env != "" && backend == "local" {
		content, err := os.ReadFile(filepath.Join(dir, workspaceStateDir, env, stagedStateFile))
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return string(content), err
	}
	opts := []tfexec.InitOption{tfexec.Backend(true), tfexec.Reconfigure(true)}
	if env != "" {
		settings, err := filepath.Abs(filepath.Join(dir, varsDirName, env, backendConfigFileName))
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(settings); err != nil {
			return "", fmt.Errorf("the %s backend's settings for %s: %w", backend, env, err)
		}
		opts = append(opts, tfexec.BackendConfig(settings))
	}
	if err := initTerraform(ctx, tf, opts...); err != nil {
		return "", fmt.Errorf("terraform init: %w", err)
	}
	state, err := tf.StatePull(ctx)
	if err != nil {
		return "", fmt.Errorf("terraform state pull: %w", err)
	}
	return state, nil
}

// notImported returns the addresses of ids, the import IDs of an
// environment of a root environments share by address (see
// MergedImportIDs), that inState doesn't have, sorted.
func notImported(ids map[string]string, inState map[string]bool) []string {
	var addresses []string
	for address := range ids {
		if !inState[address] {
			addresses = append(addresses, address)
		}
	}
	sort.Strings(addresses)
	return addresses
}
