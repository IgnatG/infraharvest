// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package tfrun runs Terraform, or OpenTofu, for the engine: it finds the
// binary, runs its commands in a root with the plugin cache the roots
// share, bounded in time or without the root's local state, and reads
// the diagnostics and the change summary of a plan.
package tfrun

import (
	"context"
	"io"
	"maps"
	"os"
	"runtime"
	"strings"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

// Terraform is the subset of *tfexec.Terraform the engine uses.
type Terraform interface {
	Init(ctx context.Context, opts ...tfexec.InitOption) error
	PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error)
	Validate(ctx context.Context) (*tfjson.ValidateOutput, error)
	ProvidersSchema(ctx context.Context) (*tfjson.ProviderSchemas, error)
	ShowPlanFile(ctx context.Context, planPath string, opts ...tfexec.ShowOption) (*tfjson.Plan, error)
	FormatCheck(ctx context.Context, opts ...tfexec.FormatOption) (bool, []string, error)
	SetStdout(w io.Writer)
}

// New returns a Terraform runner for dir that shares downloaded
// providers through pluginCacheDir, with env on top of the process's
// environment, such as the credentials the provider resolved. It creates
// both directories. Its init holds the plugin cache's lock (see
// Runner.Init).
func New(dir, execPath, pluginCacheDir string, env map[string]string) (*Runner, error) {
	for _, d := range []string{dir, pluginCacheDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	tf, err := tfexec.NewTerraform(dir, execPath)
	if err != nil {
		return nil, err
	}
	if err := tf.SetEnv(terraformEnviron(pluginCacheDir, env)); err != nil {
		return nil, err
	}
	return &Runner{Terraform: tf, pluginCacheDir: pluginCacheDir}, nil
}

// Runner is the Terraform runner New returns: tfexec's, whose
// init holds the lock of the plugin cache it shares.
type Runner struct {
	*tfexec.Terraform
	pluginCacheDir string
}

// Init runs terraform init holding the plugin cache's lock (see
// lockPluginCache): other processes, such as another infraharvest run on
// the machine, share the cache, and Terraform doesn't guarantee that
// installs into it at the same time are safe.
func (r *Runner) Init(ctx context.Context, opts ...tfexec.InitOption) error {
	lock, err := lockPluginCache(ctx, r.pluginCacheDir)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	return r.Terraform.Init(ctx, opts...)
}

// terraformEnviron is the environment Terraform runs with: the process's,
// with env on top, and the plugin cache pluginCacheDir. SetEnv replaces
// the inherited environment and rejects variables that tfexec manages
// itself (TF_LOG, TF_CLI_ARGS, ...), so those are dropped.
func terraformEnviron(pluginCacheDir string, env map[string]string) map[string]string {
	all := tfexec.CleanEnv(mergeEnv(environ(), env, runtime.GOOS == "windows"))
	all["TF_PLUGIN_CACHE_DIR"] = pluginCacheDir
	return all
}

// NewFreshEnv returns a Terraform runner for dir like New, with env on
// top of the process's environment, which it asks fresh for again before
// each plan: plans are the commands that call the cloud, and a root may
// plan for longer than temporary credentials last, such as an SSO
// session's, so each plan runs with credentials as fresh as the first.
// An error of fresh fails that plan.
func NewFreshEnv(dir, execPath, pluginCacheDir string, env map[string]string, fresh func(context.Context) (map[string]string, error)) (Terraform, error) {
	tf, err := New(dir, execPath, pluginCacheDir, env)
	if err != nil {
		return nil, err
	}
	return &freshEnvTerraform{Runner: tf, pluginCacheDir: pluginCacheDir, env: fresh}, nil
}

// freshEnvTerraform is a Terraform runner that sets its environment again,
// from env, before each plan (see NewFreshEnv). The engine runs a
// root's commands one at a time, so the environment never changes under a
// running one.
type freshEnvTerraform struct {
	*Runner
	pluginCacheDir string
	env            func(context.Context) (map[string]string, error)
}

// PlanJSON plans with the environment env gives now.
func (t *freshEnvTerraform) PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	env, err := t.env(ctx)
	if err != nil {
		return false, err
	}
	if err := t.SetEnv(terraformEnviron(t.pluginCacheDir, env)); err != nil {
		return false, err
	}
	return t.Runner.PlanJSON(ctx, w, opts...)
}

// DriftTerraform is a Terraform that can also read a root's state and
// apply a saved plan, which only the engine's Drift needs: *Runner is one.
type DriftTerraform interface {
	Terraform
	StatePull(ctx context.Context, opts ...tfexec.StatePullOption) (string, error)
	Apply(ctx context.Context, opts ...tfexec.ApplyOption) error
	ShowStateFile(ctx context.Context, statePath string, opts ...tfexec.ShowOption) (*tfjson.State, error)
}

var _ DriftTerraform = (*Runner)(nil)

// environ returns the current environment as a map.
func environ() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		for i := 1; i < len(kv); i++ { // start at 1: Windows has "=C:=C:\" entries
			if kv[i] == '=' {
				env[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	return env
}

// mergeEnv returns the environment base with over on top, for Terraform.
// Without base's TF_DATA_DIR, the process's: Terraform then keeps its
// working data in each root's .terraform, where the engine reads it, such
// as the modules init installed, and roots generating in parallel don't
// share one. over's, a directory of one Terraform's own, such as drift's
// for roots that may come from someone else (see the engine's
// DriftOptions.DataDir), is kept. Where variable names are
// case-insensitive (foldCase), as on Windows, a variable of over replaces
// base's of any case, so that Terraform doesn't get both, and the names of
// TF_ variables but input variables are upper case, so that
// tfexec.CleanEnv, which matches names exactly, finds those it manages.
func mergeEnv(base, over map[string]string, foldCase bool) map[string]string {
	key := func(name string) string { return name }
	if foldCase {
		key = strings.ToUpper
	}
	all := make(map[string]string, len(base)+len(over))
	names := map[string]string{} // by key, the name all has the variable under
	set := func(name, value string) {
		if old, ok := names[key(name)]; ok {
			delete(all, old)
		}
		names[key(name)] = name
		all[name] = value
	}
	for name, value := range base {
		if key(name) != key("TF_DATA_DIR") {
			set(name, value)
		}
	}
	for name, value := range over {
		set(name, value)
	}
	if foldCase {
		for name, value := range maps.Clone(all) {
			if upper := strings.ToUpper(name); upper != name && strings.HasPrefix(upper, "TF_") && !strings.HasPrefix(upper, "TF_VAR_") {
				delete(all, name)
				all[upper] = value
			}
		}
	}
	return all
}
