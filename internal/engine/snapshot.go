// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/logging"
)

// ImportOnce has Generate import the resources of a root once, into a
// private state snapshot, and plan the rest of the generation against it
// (see tfrun.Snapshot), instead of importing and reading every resource
// in each of the 10 to 40 plans of a root. The verification gate still
// plans the root as the user will, with import blocks and no state.
type ImportOnce struct {
	// NewRunner returns a Terraform runner for the private copy of the
	// root the snapshot is built in, as the root's own: it plans with the
	// same fresh credentials. It must return a tfrun.StateTerraform, as
	// *tfrun.Runner is, for the snapshot to be built.
	NewRunner gate.NewRunner
}

// SnapshotError is the error of a generation that planned against a
// snapshot, which Generate returns so that the caller can generate the root
// again without one (see NeedsFreshGeneration).
type SnapshotError struct {
	Err error
}

func (e *SnapshotError) Error() string { return e.Err.Error() }

func (e *SnapshotError) Unwrap() error { return e.Err }

// NeedsFreshGeneration reports whether the root that Generate returned
// result and err for should be generated again, without Options.ImportOnce:
// it planned against a snapshot, and either failed (but for being stopped)
// or failed its gate's plan check, the one check that reads what the
// snapshot caches: a snapshot that is wrong must cost time, never a wrong
// root.
func NeedsFreshGeneration(result *Result, err error) bool {
	if err != nil {
		var snapshot *SnapshotError
		return errors.As(err, &snapshot) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	}
	if result == nil || !result.UsedSnapshot {
		return false
	}
	for _, c := range result.Gate {
		if c.Name == gate.CheckPlan && !c.Passed {
			return true
		}
	}
	return false
}

// importBlocks returns the import blocks of the root in dir.
func importBlocks(dir string) ([]tfrun.ImportBlock, error) {
	files, err := hclfile.ConfigFiles(dir)
	if err != nil {
		return nil, err
	}
	var blocks []tfrun.ImportBlock
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			to, ok := b.Body.Attributes["to"]
			if b.Type != "import" || !ok {
				continue
			}
			id, _ := hclfile.ImportIDOf(b.Body)
			blocks = append(blocks, tfrun.ImportBlock{To: strings.TrimSpace(string(to.Expr.Range().SliceBytes(f.Src))), ID: id})
		}
	}
	return blocks, nil
}

// resourceAddresses returns the resources of the root in dir, as
// "<type>.<name>".
func resourceAddresses(dir string) ([]string, error) {
	files, err := hclfile.ConfigFiles(dir)
	if err != nil {
		return nil, err
	}
	var addresses []string
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			if b.Type == "resource" && len(b.Labels) == 2 {
				addresses = append(addresses, b.Labels[0]+"."+b.Labels[1])
			}
		}
	}
	return addresses, nil
}

// providerConfig returns a fingerprint of the provider blocks of the root in
// dir: the configuration a snapshot is read with. A tidy pass that changes
// it, such as one that moves the tags every resource shares into the
// provider's default_tags, changes how the provider reads the same resource
// (AWS leaves the default tags out of a resource's tags), so that a plan
// against a snapshot read before it shows differences a plan that imports
// does not (see tfrun.Cached). Where a block is in a file, its formatting
// and the order of the blocks don't matter.
func providerConfig(dir string) (string, error) {
	files, err := hclfile.ConfigFiles(dir)
	if err != nil {
		return "", err
	}
	var blocks []string
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			if b.Type == "provider" {
				blocks = append(blocks, strings.Join(strings.Fields(string(b.Range().SliceBytes(f.Src))), " "))
			}
		}
	}
	slices.Sort(blocks)
	sum := sha256.Sum256([]byte(strings.Join(blocks, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

// followProviders has the plans against the snapshot follow the provider
// configuration of the root (see tfrun.Cached.Providers).
func (g *generation) followProviders() {
	if g.cache == nil {
		return
	}
	g.cache.Providers(func() (string, error) { return providerConfig(g.dir) }, g.rebuildSnapshot)
}

// rebuildSnapshot builds the snapshot of the root as a tidy pass left it,
// for the plans that follow a change of its provider configuration. The
// secrets are variables by then: those without a value get placeholders, as
// the plans of the steps after the secrets do. It costs the plan that reads
// the cloud, which the plan it takes the place of would have cost.
func (g *generation) rebuildSnapshot(ctx context.Context) (*tfrun.Snapshot, error) {
	snapshot, err := g.snapshotOf(ctx, append(secret.Placeholders(g.result.Secrets), g.r.secrets.Options()...))
	if err != nil {
		// The caller logs it: errors can quote the values they reject.
		return nil, errors.New(secret.RedactText(err.Error(), secret.TextForms(secret.KnownValues(g.literal))))
	}
	g.counting.Add(1, 1)
	logging.Infof("%s: import-once: imported %d resources again, for the provider configuration the tidy steps changed", g.dir, snapshot.Len())
	return snapshot, nil
}

// cachedRunner returns plain, which plans against a snapshot once there is
// one, if opts.ImportOnce is set and raw, the runner of the root without
// the empty state of tfrun.WithoutLocalState, can run a snapshot's
// commands; otherwise nil.
func cachedRunner(plain tfrun.Terraform, raw tfrun.Terraform, dir string, opts Options) *tfrun.Cached {
	if opts.ImportOnce == nil || opts.ImportOnce.NewRunner == nil {
		return nil
	}
	state, ok := raw.(tfrun.StateTerraform)
	if !ok {
		return nil
	}
	return tfrun.NewCached(plain, state, opts.CommandTimeout, func() ([]tfrun.ImportBlock, error) { return importBlocks(dir) })
}

// buildSnapshot builds the snapshot the plans after it run against, once
// the root plans without errors and has left out what it won't import. A
// snapshot that can't be built (the plan of the copy isn't only imports, or
// fails) is no failure: the generation goes on as before, planning the
// root as it does without one.
func (g *generation) buildSnapshot(ctx context.Context) error {
	if g.cache == nil {
		return nil
	}
	g.counting.Step(stepSnapshot)
	snapshot, err := g.snapshotOf(ctx, g.r.secrets.Options())
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		logging.Warnf("%s: import-once: no snapshot, planning as before: %s", g.dir, secret.RedactText(err.Error(), secret.TextForms(secret.KnownValues(g.literal))))
		return nil
	}
	// The plan of the copy imports the root once.
	g.counting.Add(1, 1)
	g.cache.Use(snapshot)
	logging.Infof("%s: import-once: imported %d resources into a private snapshot the later plans read", g.dir, snapshot.Len())
	return nil
}

// snapshotOf builds the snapshot of the root, whose copy is planned with vars.
func (g *generation) snapshotOf(ctx context.Context, vars []tfexec.PlanOption) (*tfrun.Snapshot, error) {
	imports, err := importBlocks(g.dir)
	if err != nil {
		return nil, err
	}
	resources, err := resourceAddresses(g.dir)
	if err != nil {
		return nil, err
	}
	copied, remove, err := gate.VerifyCopy(g.dir)
	if err != nil {
		return nil, err
	}
	defer remove()
	tf, err := g.opts.ImportOnce.NewRunner(copied)
	if err != nil {
		return nil, err
	}
	apply, ok := tf.(tfrun.StateTerraform)
	if !ok {
		return nil, errors.New("the runner can't apply")
	}
	// The copy's plans show nothing of the secrets either.
	plan := &secret.RedactingTerraform{Terraform: tfrun.WithTimeout(tf, g.opts.CommandTimeout)}
	plan.Hide(g.literal)
	return tfrun.BuildSnapshot(ctx, tfrun.SnapshotBuild{
		Plan:      plan,
		Apply:     apply,
		Dir:       copied,
		Resources: resources,
		Imports:   imports,
		Vars:      vars,
		Prepare:   func(ctx context.Context, tf tfrun.Terraform) error { return fillWithheld(ctx, tf, copied) },
		Blame:     func(diags []tfjson.Diagnostic) []string { return blamed(copied, diags) },
		Timeout:   g.opts.CommandTimeout,
	})
}

// fillWithheld gives the arguments of the private copy in dir, which tf runs,
// that the provider requires, or requires one of, and Terraform withheld
// from generated.tf as "null # sensitive", a placeholder (see
// secret.FillMissing), such as an SSM parameter's value, which only the
// secrets step reads from a variable. The copy only validates and imports:
// every resource ignores all changes, so the placeholder is never planned,
// stored or applied, and the import reads the real value into the
// snapshot, as it does for any resource. It asks Terraform to validate the
// copy only if the file has such an attribute, and fills only the ones
// that validation says are missing: a placeholder for another could fail a
// provider's rules for it where null passes.
func fillWithheld(ctx context.Context, tf tfrun.Terraform, dir string) error {
	generated, err := hclfile.Load(filepath.Join(dir, rootfile.GeneratedFileName))
	if err != nil {
		return err
	}
	found := secret.FindSensitive(generated)
	if len(found) == 0 {
		return nil
	}
	validation, err := tf.Validate(ctx)
	if err != nil {
		return fmt.Errorf("terraform validate: %w", err)
	}
	if validation.Valid {
		return nil
	}
	imports, err := hclfile.Load(filepath.Join(dir, rootfile.ImportsFileName))
	if err != nil {
		return err
	}
	errs, _ := byResource(tfrun.ErrorDiagnostics(validation.Diagnostics), generated, imports)
	schemas, err := tf.ProvidersSchema(ctx)
	if err != nil {
		return fmt.Errorf("terraform providers schema: %w", err)
	}
	// Write-only attributes (value_wo) are never stored: they stay null.
	if secret.FillMissing(secret.WithoutWriteOnly(found, schemas), errs, schemas) == 0 {
		return nil
	}
	return generated.Save()
}

// blamed returns the resources of the root in dir that diags are about, or
// nil if some of diags is about none of them.
func blamed(dir string, diags []tfjson.Diagnostic) []string {
	errs, err := unresolved("terraform plan", dir, diags)
	if err != nil {
		return nil
	}
	return slices.Sorted(maps.Keys(errs))
}

// moveCopy makes the private copy of the root that the snapshot's moves
// are made in at once (see tfrun.MoveCopy): as snapshotOf's copy, without
// the import blocks, which the plan of the moves must not import, or the
// root's own moved blocks, which would move more than it asks for.
func (g *generation) moveCopy(context.Context) (*tfrun.MoveCopy, error) {
	copied, remove, err := gate.VerifyCopy(g.dir)
	if err != nil {
		return nil, err
	}
	c, err := g.moveCopyIn(copied)
	if err != nil {
		remove()
		return nil, err
	}
	c.Remove = remove
	return c, nil
}

func (g *generation) moveCopyIn(copied string) (*tfrun.MoveCopy, error) {
	if err := dropBlocks(copied, "import", "moved"); err != nil {
		return nil, err
	}
	resources, err := resourceAddresses(copied)
	if err != nil {
		return nil, err
	}
	tf, err := g.opts.ImportOnce.NewRunner(copied)
	if err != nil {
		return nil, err
	}
	apply, ok := tf.(tfrun.StateTerraform)
	if !ok {
		return nil, errors.New("the runner can't apply")
	}
	// The copy's plans show nothing of the secrets either.
	plan := &secret.RedactingTerraform{Terraform: tfrun.WithTimeout(tf, g.opts.CommandTimeout)}
	plan.Hide(g.literal)
	// The secrets read from variables by now: those without a value get
	// placeholders, as the plans of the steps after the secrets do.
	vars := append(secret.Placeholders(g.result.Secrets), g.r.secrets.Options()...)
	return &tfrun.MoveCopy{Dir: copied, Plan: plan, Apply: apply, Resources: resources, Vars: vars}, nil
}

// dropBlocks removes the top-level blocks of the types types from the
// configuration files of the root in dir.
func dropBlocks(dir string, types ...string) error {
	files, err := hclfile.ConfigFiles(dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		body := f.Edit().Body()
		dropped := false
		for _, b := range body.Blocks() {
			if slices.Contains(types, b.Type()) {
				body.RemoveBlock(b)
				dropped = true
			}
		}
		if dropped {
			if err := f.Save(); err != nil {
				return err
			}
		}
	}
	return nil
}
