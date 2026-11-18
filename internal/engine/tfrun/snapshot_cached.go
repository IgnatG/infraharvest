// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/logging"
)

// Cached is a Terraform whose plans, while it has a snapshot (see Use), run
// against it: with -state=<snapshot> -refresh=false -lock=false, so that
// the resources the snapshot holds are not imported and read again. The
// snapshot and the root's import blocks are reconciled before each plan,
// and the plan's output and its shown plan get back what the import left
// out, so that what reads them sees the plans as before: the summary's
// import count, and importing on the changes of the imports. Without a
// snapshot, or once it is released, it runs the plain Terraform it was
// made with.
//
// A snapshot holds what the provider read as it was configured when the
// snapshot was built: a provider block that changes after (default_tags,
// ignore_tags, an alias) changes how the provider reads the same resource,
// so a plan against the old state shows differences a real import plan
// does not. With Providers set, each plan therefore runs against the
// snapshot of the provider configuration the root has now: where there is
// none, the plan is a plain one, which reads the cloud as the user's plan
// does, and the next plan of the same configuration builds one (a
// configuration planned once and undone costs nothing but that plan).
type Cached struct {
	// Terraform runs every command but a cached plan: it is the plain
	// runner, as the engine plans without a snapshot.
	Terraform

	raw     StateTerraform
	timeout time.Duration
	imports func() ([]ImportBlock, error)
	// copies makes the private copy many moves are made in at once, if
	// set (see Snapshot.reconcile).
	copies MoveCopier

	// providers returns a fingerprint of the root's provider configuration,
	// and rebuild builds a snapshot of the root as it is, if set (see
	// Providers).
	providers func() (string, error)
	rebuild   func(ctx context.Context) (*Snapshot, error)

	snap *Snapshot
	// config is the provider configuration snap was built for, and parked
	// the snapshots of others, which a root may return to when an edit is
	// undone.
	config string
	parked map[string]*Snapshot
	// bypassed is the configuration the last plain plan was made for, for
	// want of a snapshot, and failed those a snapshot could not be built for.
	bypassed string
	failed   map[string]bool
	// kept are the imports the last cached plan skipped, by address.
	kept   map[string]string
	cached int
}

// maxSnapshots bounds how many snapshots a root keeps, each of which holds
// every attribute of every resource in the clear.
const maxSnapshots = 3

// NewCached returns plain, with plans against a snapshot once it is given
// one (see Use). raw is the runner the cached plans and the snapshot's
// state commands run with, without anything that sets another -state, such
// as Stateless; timeout bounds each of those commands, if set, and imports
// returns the root's import blocks as they are.
func NewCached(plain Terraform, raw StateTerraform, timeout time.Duration, imports func() ([]ImportBlock, error)) *Cached {
	return &Cached{Terraform: plain, raw: raw, timeout: timeout, imports: imports}
}

// MoveIn has the snapshot's moves made in copies that copies makes, where
// a reconcile moves many resources at once (see Snapshot.reconcile).
func (c *Cached) MoveIn(copies MoveCopier) {
	c.copies = copies
}

// Providers has the plans follow the root's provider configuration (see
// Cached): providers returns a fingerprint of it as the files have it, and
// rebuild builds a snapshot of the root as it is, once the configuration
// has stayed as it is for a second plan. It must be set before Use.
func (c *Cached) Providers(providers func() (string, error), rebuild func(ctx context.Context) (*Snapshot, error)) {
	c.providers, c.rebuild = providers, rebuild
}

// Use has the plans that follow run against s, which was built for the
// root's provider configuration as it is.
func (c *Cached) Use(s *Snapshot) {
	for _, parked := range c.parked {
		parked.Remove()
	}
	c.parked, c.failed, c.bypassed = nil, nil, ""
	c.snap, c.kept = s, nil
	if c.providers == nil {
		return
	}
	cfg, err := c.providers()
	if err != nil {
		logging.Warnf("import-once: dropping the snapshot: %v", err)
		c.Release()
		return
	}
	c.config = cfg
}

// Release removes the snapshot, if there is one, and has the plans that
// follow run plain. It may be called more than once.
func (c *Cached) Release() {
	if c == nil {
		return
	}
	c.removeAll()
	c.snap, c.kept = nil, nil
}

// removeAll removes the snapshot and those parked.
func (c *Cached) removeAll() {
	c.snap.Remove()
	for _, s := range c.parked {
		s.Remove()
	}
	c.parked, c.failed, c.bypassed, c.config = nil, nil, "", ""
}

// follow makes snap the snapshot of the provider configuration the root has
// now, if there is one, and reports whether it is: where it isn't, the plan
// runs plain. A snapshot of the configuration, kept from before an edit
// that was undone, is used again; the second plan of one there is none of
// builds one.
func (c *Cached) follow(ctx context.Context) (bool, error) {
	cfg, err := c.providers()
	if err != nil {
		return false, err
	}
	if cfg == c.config {
		return true, nil
	}
	if s, ok := c.parked[cfg]; ok {
		c.park(cfg, s)
		return true, nil
	}
	if c.bypassed != cfg || c.rebuild == nil || c.failed[cfg] || len(c.parked)+1 >= maxSnapshots {
		c.bypassed = cfg
		c.kept = nil
		return false, nil
	}
	s, err := c.rebuild(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		logging.Warnf("import-once: no snapshot for the changed provider configuration, planning as before: %v", err)
		if c.failed == nil {
			c.failed = map[string]bool{}
		}
		c.failed[cfg] = true
		c.kept = nil
		return false, nil
	}
	c.park(cfg, s)
	return true, nil
}

// park makes s, the snapshot of the provider configuration cfg, the one
// the plans run against, and keeps the one before it for a return.
func (c *Cached) park(cfg string, s *Snapshot) {
	if c.parked == nil {
		c.parked = map[string]*Snapshot{}
	}
	delete(c.parked, cfg)
	c.parked[c.config] = c.snap
	c.snap, c.config, c.kept = s, cfg, nil
}

// Active reports whether plans run against a snapshot.
func (c *Cached) Active() bool { return c != nil && c.snap != nil }

// CachedPlans returns how many plans ran against a snapshot.
func (c *Cached) CachedPlans() int {
	if c == nil {
		return 0
	}
	return c.cached
}

func (c *Cached) PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	if c.snap == nil {
		return c.Terraform.PlanJSON(ctx, w, opts...)
	}
	if c.providers != nil {
		following, err := c.follow(ctx)
		if err != nil {
			logging.Warnf("import-once: dropping the snapshot: %v", err)
			c.Release()
			return c.Terraform.PlanJSON(ctx, w, opts...)
		}
		if !following {
			return c.Terraform.PlanJSON(ctx, w, opts...)
		}
	}
	blocks, err := c.imports()
	if err == nil {
		c.kept, err = c.snap.reconcile(ctx, c.raw, blocks, c.timeout, c.copies)
	}
	if err != nil {
		// A snapshot that can't follow the root is of no use: plan as
		// before, and never from a state that may not match it.
		logging.Warnf("import-once: dropping the snapshot: %v", err)
		c.Release()
		return c.Terraform.PlanJSON(ctx, w, opts...)
	}
	plan := append(append([]tfexec.PlanOption(nil), opts...),
		tfexec.State(c.snap.state), tfexec.Refresh(false), tfexec.Lock(false)) //nolint:staticcheck // -state: see statelessTerraform
	var out bytes.Buffer
	changed, planErr := WithTimeout(c.raw, c.timeout).PlanJSON(ctx, &out, plan...)
	c.cached++
	data, err := withImports(out.Bytes(), len(c.kept))
	if err != nil {
		data = out.Bytes()
	}
	if _, werr := w.Write(data); werr != nil && planErr == nil {
		planErr = werr
	}
	return changed, planErr
}

// ShowPlanFile shows the plan, with importing set on the changes of the
// imports the cached plan skipped.
func (c *Cached) ShowPlanFile(ctx context.Context, planPath string, opts ...tfexec.ShowOption) (*tfjson.Plan, error) {
	p, err := c.Terraform.ShowPlanFile(ctx, planPath, opts...)
	if err != nil || p == nil || c.snap == nil {
		return p, err
	}
	for _, rc := range p.ResourceChanges {
		if id, ok := c.kept[rc.Address]; ok && rc.Mode == tfjson.ManagedResourceMode && rc.Change != nil && rc.Change.Importing == nil {
			rc.Change.Importing = &tfjson.Importing{ID: id}
		}
	}
	return p, nil
}

// withImports returns out, Terraform's machine-readable output of a plan,
// with skipped more imports in its change summary.
func withImports(out []byte, skipped int) ([]byte, error) {
	if skipped == 0 {
		return out, nil
	}
	var result bytes.Buffer
	err := eachUIMessage(out, func(line []byte) {
		if bytes.Contains(line, []byte(`"change_summary"`)) {
			if edited, ok := summaryWithImports(line, skipped); ok {
				line = edited
			}
		}
		result.Write(line)
		result.WriteByte('\n')
	})
	return result.Bytes(), err
}

func summaryWithImports(line []byte, skipped int) ([]byte, bool) {
	var msg map[string]json.RawMessage
	if json.Unmarshal(line, &msg) != nil {
		return nil, false
	}
	var typ string
	if json.Unmarshal(msg["type"], &typ) != nil || typ != "change_summary" {
		return nil, false
	}
	var changes map[string]json.RawMessage
	if json.Unmarshal(msg["changes"], &changes) != nil {
		return nil, false
	}
	var imported int
	if raw, ok := changes["import"]; ok && json.Unmarshal(raw, &imported) != nil {
		return nil, false
	}
	count, err := json.Marshal(imported + skipped)
	if err != nil {
		return nil, false
	}
	changes["import"] = count
	if msg["changes"], err = json.Marshal(changes); err != nil {
		return nil, false
	}
	edited, err := json.Marshal(msg)
	return edited, err == nil
}
