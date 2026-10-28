// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

// driftSession is one check of a root for drift: the root in dir, the
// staged copy of it Drift plans in staging, and, for a root environments
// share, the environment checked, env, whose backend settings, state,
// import IDs and var files it reads. Where a root of its own and a root
// environments share differ, the session says which, so that Drift is
// the same steps for both.
type driftSession struct {
	dir, staging string
	// env is the environment checked, "" for a root of its own.
	env  string
	opts DriftOptions
	// files are the root's configuration files, and backend the type of
	// its backend (see rootBackend).
	files   []*hclFile
	backend string
	// imports are env's import IDs, by address (see MergedImportIDs).
	imports map[string]string
	// staged says the copy was made, to remove.
	staged bool
	// tf plans the copy, once opened.
	tf DriftTerraform
	// vars are the -var and -var-file options every plan of the copy
	// takes: env's var files, as staged, then the placeholders.
	vars []tfexec.PlanOption
	// varFiles are env's var files in the copy, as absolute paths.
	varFiles []string
	// state is the copy's state, once read, and forms the secrets it and
	// the refresh-only plan hold, which the plans' errors leave out (see
	// driftSecretForms).
	state *tfjson.State
	forms []string
}

// openDriftSession reads the root in dir to check it as opts say, or
// fails with an UnsupportedBackendError for a backend Drift doesn't read
// state from.
func openDriftSession(dir, staging string, opts DriftOptions) (*driftSession, error) {
	files, err := configFiles(dir)
	if err != nil {
		return nil, err
	}
	backend, err := rootBackend(files, opts.Backends)
	if err != nil {
		return nil, err
	}
	s := &driftSession{dir: dir, staging: staging, env: opts.Environment, opts: opts, files: files, backend: backend}
	if s.env != "" {
		if s.imports, err = MergedImportIDs(dir, s.env); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// stage pulls the root's state through tf and, where it has resources,
// makes the copy with it as its local state (see stageDrift), with env's
// var files, which the plans read from the copy, where an accepted change
// is edited first. It reports whether the state has resources: without,
// there is nothing to check, and no copy. Checks of the root's other
// environments wait until it is done (see DriftOptions.WithRootLock).
func (s *driftSession) stage(ctx context.Context, tf DriftTerraform) (bool, error) {
	locked := s.opts.WithRootLock
	if locked == nil {
		locked = func(f func() error) error { return f() }
	}
	var staged bool
	err := locked(func() (err error) {
		staged, err = s.pullAndStage(ctx, tf)
		return err
	})
	return staged, err
}

// pullAndStage is stage, with the root's lock held.
func (s *driftSession) pullAndStage(ctx context.Context, tf DriftTerraform) (bool, error) {
	state, err := pullDriftState(ctx, tf, s.dir, s.backend, s.env)
	if err != nil || !stateHasResources(state) {
		return false, err
	}
	s.staged = true
	if err := stageDrift(s.dir, s.staging, []byte(state)); err != nil {
		return false, err
	}
	if s.env == "" {
		return true, nil
	}
	if err := stageVarFiles(s.dir, s.staging, s.env); err != nil {
		return false, err
	}
	s.vars, s.varFiles, err = mergedVarFiles(s.staging, s.env)
	return true, err
}

// close removes the copy, if stage made one.
func (s *driftSession) close() {
	if s.staged {
		_ = os.RemoveAll(s.staging)
	}
}

// open initialises the copy through the Terraform staged returns for it.
func (s *driftSession) open(ctx context.Context, staged func(dir string) (DriftTerraform, error)) error {
	stf, err := staged(s.staging)
	if err != nil {
		return err
	}
	s.tf = driftWithTimeout(stf, s.opts.CommandTimeout)
	if err := initTerraform(ctx, s.tf); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}
	return nil
}

// inState returns the addresses of the managed resources of the copy's
// state.
func (s *driftSession) inState(ctx context.Context) (map[string]bool, error) {
	shown, err := s.tf.ShowStateFile(ctx, filepath.Join(s.staging, localStateFileName))
	if err != nil {
		return nil, fmt.Errorf("terraform show: %w", err)
	}
	s.state, s.forms = shown, driftSecretForms(shown)
	return stateAddresses(shown), nil
}

// notApplied returns the addresses the root imports, or, for a root
// environments share, env's import IDs do, that inState doesn't have,
// sorted (see notApplied and notImported).
func (s *driftSession) notApplied(inState map[string]bool) []string {
	if s.env != "" {
		return notImported(s.imports, inState)
	}
	return notApplied(s.files, inState)
}

// placeholders gives the plans a placeholder value for each variable of
// the copy without one (see variablePlaceholders), and returns their
// names.
func (s *driftSession) placeholders() ([]string, error) {
	files, err := configFiles(s.staging)
	if err != nil {
		return nil, err
	}
	vars, names := variablePlaceholders(s.staging, files, s.varFiles...)
	s.vars = append(s.vars, vars...)
	return names, nil
}

// refresh plans the copy refresh-only, into a plan file the copy's state
// is applied from (see refreshed).
func (s *driftSession) refresh(ctx context.Context) (*tfjson.Plan, error) {
	p, err := savedPlan(ctx, s.tf, s.refreshPlan(), s.planOptions(tfexec.RefreshOnly(true))...)
	if err != nil {
		return nil, fmt.Errorf("terraform plan -refresh-only: %w", redactError(err, s.forms))
	}
	s.forms = driftSecretForms(s.state, p)
	return p, nil
}

// refreshed applies the refresh-only plan to the copy's state, which is
// all it writes, and returns the plan of the code with that state,
// without refreshing again: the changes of the cloud it plans are those
// the next apply would put back.
func (s *driftSession) refreshed(ctx context.Context) (*tfjson.Plan, error) {
	// A saved plan applies without refreshing again.
	if err := s.tf.Apply(ctx, tfexec.DirOrPlan(s.refreshPlan()), tfexec.Lock(false)); err != nil {
		return nil, fmt.Errorf("terraform apply of the refresh-only plan to the staged state: %w", redactError(err, s.forms))
	}
	p, err := savedPlan(ctx, s.tf, filepath.Join(s.staging, "code.tfplan"), s.planOptions(tfexec.Refresh(false))...)
	if err != nil {
		return nil, fmt.Errorf("terraform plan -refresh=false: %w", redactError(err, s.forms))
	}
	return p, nil
}

func (s *driftSession) refreshPlan() string {
	return filepath.Join(s.staging, "refresh.tfplan")
}

// planOptions are the options of a plan of the copy: the variables, then
// more, without locking the state.
func (s *driftSession) planOptions(more tfexec.PlanOption) []tfexec.PlanOption {
	return append(slices.Clone(s.vars), more, tfexec.Lock(false))
}

// contents returns the root's files drift edits, by name (see
// driftContents).
func (s *driftSession) contents() (map[string][]byte, error) {
	return driftContents(s.dir, s.env)
}

// editor returns the editor of the changes of the plans refresh and code
// in root, the root's files drift edits with those of the modules it
// calls added to them (see addModuleFiles); for a root environments
// share, it edits only env's var file, and the blocks env alone has (see
// envOnlyFlags).
func (s *driftSession) editor(refresh, code *tfjson.Plan, root map[string][]byte) (*driftEditor, error) {
	e := newDriftEditor(refresh, code, s.opts.Defaults)
	e.modules = loadDriftModules(s.dir, s.opts.DataDir)
	e.adapters = s.opts.Adapters
	if err := addModuleFiles(root, s.dir, s.opts.OutDir, e.modules); err != nil {
		return nil, err
	}
	e.syncModules(root)
	if s.env == "" {
		return e, nil
	}
	e.env, e.envVars = s.env, envVarsPath(s.env)
	var err error
	if e.ownFlags, err = envOnlyFlags(s.dir, s.env, s.files); err != nil {
		return nil, err
	}
	return e, nil
}

// accept takes the accepted changes into the root's code, root, as the
// editor finds them, where the plan of the edited copy and the checks of
// the verification gate say it can (see acceptDrift).
func (s *driftSession) accept(ctx context.Context, root map[string][]byte, accepted []*DriftChange, code *tfjson.Plan, e *driftEditor) error {
	if len(accepted) == 0 {
		return nil
	}
	return acceptDrift(ctx, s.tf, s.dir, s.staging, root, accepted, code, s.vars, e, s.opts)
}
