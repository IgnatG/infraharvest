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

// pullDriftState returns the state of the root in dir, whose backend is
// backend (see rootBackend), pulled through tf once initialised with its
// backend. For environment env of a root environments share, the backend
// is initialised with env's settings (see backendConfigFile); a root
// without a backend keeps each environment's state in the workspace named
// after it (see workspaceStateFile), which is read as it is: "" where env
// has none yet.
func pullDriftState(ctx context.Context, tf DriftTerraform, dir, backend, env string) (string, error) {
	if env != "" && backend == "local" {
		content, err := os.ReadFile(workspaceStateFile(dir, env))
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return string(content), err
	}
	opts := []tfexec.InitOption{tfexec.Backend(true), tfexec.Reconfigure(true)}
	if env != "" {
		settings, err := backendConfigFile(dir, env)
		if err != nil {
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

// driftContents returns the contents of the .tf files of dir, by name
// (see tfContents), and, for environment env of a root environments
// share, those of its var file, by envVarsPath, where drift takes a value
// a variable sets.
func driftContents(dir, env string) (map[string][]byte, error) {
	contents, err := tfContents(dir)
	if err != nil || env == "" {
		return contents, err
	}
	content, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(envVarsPath(env))))
	if errors.Is(err, fs.ErrNotExist) {
		return contents, nil
	}
	if err != nil {
		return nil, err
	}
	contents[envVarsPath(env)] = content
	return contents, nil
}

// stageVarFiles copies the var files environment env plans the root in
// dir with (see existingVarFiles) to the same paths under staging, the
// staged copy, whose plans read them there, as drift edits them.
func stageVarFiles(dir, staging, env string) error {
	for _, name := range existingVarFiles(dir, env) {
		content, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		target := filepath.Join(staging, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			return err
		}
	}
	return nil
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
