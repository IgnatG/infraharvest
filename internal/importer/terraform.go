// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	goversion "github.com/hashicorp/go-version"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// terraformRun is how a root's Terraform runs: the binary, the plugin cache
// every root shares, and what the provider adds to its environment, such
// as fresh credentials (see terraformEnv), and, when the provider adds
// any, how to get them again before each plan.
type terraformRun struct {
	execPath, pluginCacheDir string
	env                      map[string]string
	freshEnv                 func(context.Context) (map[string]string, error)
}

// in returns the Terraform runner of dir: one that asks freshEnv for its
// environment before each plan, when it is set (see
// tfrun.NewFreshEnv).
func (t terraformRun) in(dir string) (tfrun.Terraform, error) {
	if t.freshEnv != nil {
		return tfrun.NewFreshEnv(dir, t.execPath, t.pluginCacheDir, t.env, t.freshEnv)
	}
	tf, err := tfrun.New(dir, t.execPath, t.pluginCacheDir, t.env)
	if err != nil {
		return nil, err
	}
	return tf, nil
}

// freshTerraformEnv returns how to get provider's Terraform environment
// again (see terraformEnv), for each plan, or nil when the provider adds
// nothing to it.
func freshTerraformEnv(provider harvest.Provider) func(context.Context) (map[string]string, error) {
	if _, ok := provider.(harvest.ProviderWithTerraformEnv); !ok {
		return nil
	}
	return func(ctx context.Context) (map[string]string, error) {
		return terraformEnv(ctx, provider)
	}
}

// terraformEnv returns what Terraform needs in its environment to import
// provider's resources, such as the credentials it lists with (see
// harvest.ProviderWithTerraformEnv), fresh for each root and each plan.
func terraformEnv(ctx context.Context, provider harvest.Provider) (map[string]string, error) {
	withEnv, ok := provider.(harvest.ProviderWithTerraformEnv)
	if !ok {
		return nil, nil
	}
	env, err := withEnv.TerraformEnv(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: credentials for Terraform: %w", provider.Name(), err)
	}
	return env, nil
}

// generateDir runs engine.Generate in dir, seeding it with *lock if set and
// keeping its lock file in *lock otherwise.
func generateDir(ctx context.Context, dir string, tr terraformRun, imports []rootfile.Import, opts engine.Options, lock *[]byte) (*engine.Result, error) {
	tf, err := tr.in(dir)
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dir, tfrun.LockFileName)
	if *lock != nil {
		if err := os.WriteFile(lockPath, *lock, 0o644); err != nil {
			return nil, err
		}
	}
	result, err := engine.Generate(ctx, tf, dir, imports, opts)
	if *lock == nil {
		if content, readErr := os.ReadFile(lockPath); readErr == nil {
			*lock = content
		}
	}
	return result, err
}

// lockMeets reports whether the provider at source that lock records meets
// constraint, so that lock can seed a root with that constraint: terraform
// init fails on a locked version the configuration doesn't accept. A lock
// that records no version of source, or a constraint or version that
// doesn't parse, counts as meeting it, as before such a check.
func lockMeets(lock []byte, source, constraint string) bool {
	locked := engine.LockedVersion(lock, source)
	if locked == "" || constraint == "" {
		return true
	}
	v, err := goversion.NewVersion(locked)
	if err != nil {
		return true
	}
	c, err := goversion.NewConstraint(constraint)
	if err != nil {
		return true
	}
	return c.Check(v)
}

func infraharvestCacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find a cache directory for Terraform downloads: %w", err)
	}
	return filepath.Join(dir, "infraharvest"), nil
}
