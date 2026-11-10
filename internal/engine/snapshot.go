// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"log"
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
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
	snapshot, err := g.snapshotOf(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		log.Printf("%s: import-once: no snapshot, planning as before: %s", g.dir, secret.RedactText(err.Error(), secret.TextForms(secret.KnownValues(g.literal))))
		return nil
	}
	// The plan of the copy imports the root once.
	g.counting.Add(1, 1)
	g.cache.Use(snapshot)
	log.Printf("%s: import-once: imported %d resources into a private snapshot the later plans read", g.dir, snapshot.Len())
	return nil
}

func (g *generation) snapshotOf(ctx context.Context) (*tfrun.Snapshot, error) {
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
		Vars:      g.r.secrets.Options(),
		Timeout:   g.opts.CommandTimeout,
	})
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
	return &tfrun.MoveCopy{Dir: copied, Plan: plan, Apply: apply, Resources: resources, Vars: g.r.secrets.Options()}, nil
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
