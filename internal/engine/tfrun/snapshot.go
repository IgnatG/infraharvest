// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// A state snapshot is the import-once cache of a root: the state of
// importing every resource of the root, kept in a private file outside the
// output, so that the plans that follow need not import and read every
// resource again. Every plan of a root otherwise has an import block per
// resource and no state, and so reads the whole root from the cloud.
//
// The snapshot is built by applying a saved plan in a private copy of the
// root whose resources ignore all changes: the plan only imports, so the
// apply writes the state and calls no provider write. The plans that follow
// run with -state=<snapshot> -refresh=false: Terraform skips the import of
// an address that is in state. See Cached for how those plans keep the view
// the engine expects, and docs/development.md for the security model.

// SnapshotPattern names the private temporary directories of snapshots.
const SnapshotPattern = "infraharvest-snapshot-"

// The patterns below name the private temporary directories other packages
// create with MkdirTempHeld, which RemoveStaleSnapshots sweeps too: they
// hold copies of roots and selections that a killed run never removed.
const (
	// DriftRootsPattern names the copies of the confirmed roots a drift run
	// reads. DriftPattern is a prefix of it, so one prefix match covers both.
	DriftRootsPattern = "infraharvest-drift-roots-"
	// DriftPattern names the private environments of drift runs over
	// untrusted roots.
	DriftPattern = "infraharvest-drift-"
	// MCPPattern names the private copies of files the MCP server reads.
	MCPPattern = "infraharvest-mcp-"
)

// staleSnapshotAge is how old a snapshot directory must be to count as left
// behind by a run that was killed, rather than in use by another run.
const staleSnapshotAge = 12 * time.Hour

// SnapshotOverrideFile is the override file of the private copy a snapshot
// is built in.
const SnapshotOverrideFile = "infraharvest_snapshot_override.tf"

// ImportBlock is an import block of a root: the address it imports into and
// the ID it imports with, "" where that isn't a literal.
type ImportBlock struct {
	To, ID string
}

// StateTerraform is a Terraform that also runs the commands a snapshot is
// built and kept with: *Runner is one.
type StateTerraform interface {
	Terraform
	Apply(ctx context.Context, opts ...tfexec.ApplyOption) error
	StateMv(ctx context.Context, source, destination string, opts ...tfexec.StateMvCmdOption) error
	StateRm(ctx context.Context, address string, opts ...tfexec.StateRmCmdOption) error
}

var _ StateTerraform = (*Runner)(nil)

var staleOnce sync.Once

// RemoveStaleSnapshots removes the directories of snapshots, saved plans,
// empty states, drift environments and MCP copies in the system's
// temporary directory that a run killed before it could remove them left
// behind, once per process: snapshots and
// saved plans hold secret values. Only directories of the current user,
// older than a run can last, are removed, as another run may be using a
// younger one; and none whose lock another process holds (see
// MkdirTempHeld), as the MCP server and a long drift run outlast any age.
func RemoveStaleSnapshots() {
	staleOnce.Do(func() { removeStaleSnapshots(os.TempDir(), time.Now(), staleSnapshotAge) })
}

// stalePatterns name the temporary directories RemoveStaleSnapshots
// removes. Matching is by prefix, so DriftPattern covers DriftRootsPattern.
var stalePatterns = []string{SnapshotPattern, PlanPattern, EmptyStatePattern, DriftPattern, MCPPattern}

func removeStaleSnapshots(tmp string, now time.Time, maxAge time.Duration) {
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !slices.ContainsFunc(stalePatterns, func(p string) bool { return strings.HasPrefix(name, p) }) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < maxAge || !ownedByCurrentUser(info) {
			continue
		}
		dir := filepath.Join(tmp, name)
		if inUse(dir) {
			continue
		}
		_ = os.RemoveAll(dir)
	}
}

// Snapshot is the state of a root's imports, in a private file (see the
// comment above). It holds every attribute of every resource in the clear,
// secrets included: it lives in a directory of its own in the system's
// temporary directory, and is removed by Remove.
type Snapshot struct {
	dir   string
	state string
	// ids are the import IDs of the entries by address, as they were
	// imported or moved to: state doesn't keep them.
	ids map[string]string
	// partial are the resources the copy's plans can't have ignore all
	// changes, with the arguments they ignore instead (see
	// SnapshotBuild.Blame).
	partial map[string][]string
}

// Path returns the snapshot's state file.
func (s *Snapshot) Path() string { return s.state }

// Len returns how many resources the snapshot holds.
func (s *Snapshot) Len() int { return len(s.ids) }

// Remove removes the snapshot, state and directory. It may be called more
// than once.
func (s *Snapshot) Remove() {
	if s != nil && s.dir != "" {
		_ = os.RemoveAll(s.dir)
	}
}

// SnapshotBuild is what BuildSnapshot needs.
type SnapshotBuild struct {
	// Plan runs the commands that don't write state in the private copy of
	// the root (see gate.VerifyCopy), with the bounds and the redaction the
	// run's own plans have; Apply is the same runner, unwrapped, for the
	// one command that does. The copy is not initialised yet.
	Plan  Terraform
	Apply StateTerraform
	// Dir is the copy's directory.
	Dir string
	// Resources are the copy's resources, as "<type>.<name>".
	Resources []string
	// Imports are the import blocks of the root: the plan must import them
	// all.
	Imports []ImportBlock
	// Vars are the plan options that give the copy's variables values.
	Vars []tfexec.PlanOption
	// Prepare, if set, edits the copy once it is initialised and before it
	// is planned, with the runner that plans it: the copy only has to
	// validate and import, so it can take what the root's own files lack,
	// such as a value the root keeps out of its files.
	Prepare func(ctx context.Context, tf Terraform) error
	// Blame, if set, returns the resources, as "<type>.<name>", that the
	// errors of the copy's plan are about, each with the arguments and
	// blocks its configuration sets, or nil if it can't tell for each
	// error. Ignoring all changes hands the provider a resource's imported
	// values as its configuration, and some providers' plan checks reject
	// values the API sets but the configuration leaves out
	// (azurerm_postgresql_flexible_server's storage_iops without
	// PremiumV2_LRS storage, aws_nat_gateway's
	// secondary_private_ip_address_count on a public gateway): those
	// resources are planned again ignoring only what their configuration
	// sets, so that what it leaves out stays unset.
	Blame func(diags []tfjson.Diagnostic) map[string][]string
	// Timeout bounds the apply.
	Timeout time.Duration
}

// snapshotOverrideTries is how many times the copy is planned: the first
// plan, then one more each time Blame names resources to plan ignoring
// only what their configuration sets, and each time a plan shows those
// resources taking provider defaults (see providerDefaults).
const snapshotOverrideTries = 3

// BuildSnapshot builds a snapshot of the root b describes: it adds an
// override file that has every resource ignore all changes to the copy,
// plans the copy into a saved plan, checks the plan only imports (see
// CheckImportOnly), and applies it into a new private state file. It
// returns an error, and leaves nothing behind, if the plan isn't just
// imports: the caller then goes on without a snapshot.
func BuildSnapshot(ctx context.Context, b SnapshotBuild) (*Snapshot, error) {
	if len(b.Imports) == 0 {
		return nil, errors.New("no import blocks")
	}
	RemoveStaleSnapshots()
	dir, err := os.MkdirTemp("", SnapshotPattern)
	if err != nil {
		return nil, err
	}
	// The mode is 0700 already; this also covers Windows, which the mode
	// doesn't.
	if _, err := fsutil.MakePrivate(dir); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	s := &Snapshot{dir: dir, state: filepath.Join(dir, LocalStateFileName), ids: map[string]string{}}
	if err := s.build(ctx, b); err != nil {
		s.Remove()
		return nil, err
	}
	return s, nil
}

func (s *Snapshot) build(ctx context.Context, b SnapshotBuild) error {
	// The lock file in the copy pins the providers, so that the state
	// the plan is applied to is of the release the root's plans use.
	if err := Init(ctx, b.Plan, tfexec.Backend(false)); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}
	if b.Prepare != nil {
		if err := b.Prepare(ctx, b.Plan); err != nil {
			return fmt.Errorf("prepare the copy: %w", err)
		}
	}
	planFile := filepath.Join(s.dir, "plan")
	var p *tfjson.Plan
	for try := 1; ; try++ {
		if err := fsutil.WriteFile(filepath.Join(b.Dir, SnapshotOverrideFile), SnapshotOverride(b.Resources, s.partial), 0o600); err != nil {
			return err
		}
		planned, _, diags, err := showPlanTo(ctx, b.Plan, planFile, b.Vars)
		if err != nil {
			return err
		}
		if len(diags) == 0 {
			defaulted := providerDefaults(planned, s.partial)
			if len(defaulted) == 0 || try == snapshotOverrideTries {
				p = planned
				break
			}
			for r, names := range defaulted {
				s.partial[r] = mergeNames(s.partial[r], names)
			}
			continue
		}
		blamed := blamedIgnored(b, s.partial, diags)
		if len(blamed) == 0 || try == snapshotOverrideTries {
			return fmt.Errorf("the copy doesn't plan: %w", DiagnosticsError(diags))
		}
		if s.partial == nil {
			s.partial = map[string][]string{}
		}
		maps.Copy(s.partial, blamed)
	}
	if err := CheckImportOnly(p); err != nil {
		return err
	}
	importing := 0
	for _, rc := range p.ResourceChanges {
		if rc.Mode == tfjson.ManagedResourceMode {
			importing++
		}
	}
	if importing != len(b.Imports) {
		return fmt.Errorf("the plan imports %d resources, the root has %d import blocks", importing, len(b.Imports))
	}
	applyCtx := ctx
	if b.Timeout > 0 {
		var cancel context.CancelFunc
		applyCtx, cancel = context.WithTimeout(ctx, b.Timeout)
		defer cancel()
	}
	// -backup=- keeps a second copy of the state from being written. -state
	// is a legacy flag that Terraform and OpenTofu still take for the local
	// backend, as statelessTerraform uses it.
	state := tfexec.State(s.state) //nolint:staticcheck // see above
	if err := b.Apply.Apply(applyCtx, tfexec.DirOrPlan(planFile), state, tfexec.StateOut(s.state), tfexec.Lock(false), tfexec.Backup("-")); err != nil {
		return fmt.Errorf("terraform apply of the import-only plan: %w", err)
	}
	_ = os.Remove(planFile)
	for _, imp := range b.Imports {
		s.ids[imp.To] = imp.ID
	}
	return nil
}

// blamedIgnored returns the resources b.Blame finds diags are about, with
// what their configuration sets, if each is one the copy still has ignore
// all changes; otherwise nil, as the override isn't what fails the plan.
func blamedIgnored(b SnapshotBuild, partial map[string][]string, diags []tfjson.Diagnostic) map[string][]string {
	if b.Blame == nil {
		return nil
	}
	blamed := b.Blame(diags)
	for r := range blamed {
		if _, done := partial[r]; done || !slices.Contains(b.Resources, r) {
			return nil
		}
	}
	return blamed
}

// providerDefaults returns, for the resources of p that the copy has ignore
// only some arguments (partial), the arguments the provider fills in with a
// default its read never returns: the import leaves them null, the
// configuration leaves them out, and the plan sets them (aws_sns_topic_
// subscription's confirmation_timeout_in_minutes and endpoint_auto_confirms
// after a tidy pass drops the values that equal the default). The copy
// ignores them too: it only has to import, and ignoring an argument keeps
// the imported value, null, so that the plan changes nothing. It returns
// nil unless every change of p that isn't an import is such an update of a
// partial resource: any other change stays what CheckImportOnly rejects.
func providerDefaults(p *tfjson.Plan, partial map[string][]string) map[string][]string {
	if p == nil || len(partial) == 0 {
		return nil
	}
	found := map[string][]string{}
	for _, rc := range p.ResourceChanges {
		c := rc.Change
		if c == nil || rc.Mode != tfjson.ManagedResourceMode || (c.Actions.NoOp() && c.Importing != nil) {
			continue
		}
		key := rc.Type + "." + rc.Name
		if rc.Type == "" {
			key, _, _ = strings.Cut(rc.Address, "[")
		}
		if _, ok := partial[key]; !ok || !c.Actions.Update() || c.Importing == nil {
			return nil
		}
		before, _ := c.Before.(map[string]any)
		after, _ := c.After.(map[string]any)
		changed := changedArguments(c)
		if len(changed) == 0 {
			return nil
		}
		for _, name := range changed {
			if before[name] != nil || after[name] == nil {
				return nil
			}
		}
		found[key] = mergeNames(found[key], changed)
	}
	return found
}

// mergeNames returns the sorted union of a and b.
func mergeNames(a, b []string) []string {
	merged := slices.Concat(a, b)
	slices.Sort(merged)
	return slices.Compact(merged)
}

// SnapshotOverride returns the override file that has every one of
// resources, as "<type>.<name>", ignore all changes, but for those partial
// has, which ignore changes to the arguments and blocks it gives for each:
// the plan of the copy then imports the resources and changes nothing.
func SnapshotOverride(resources []string, partial map[string][]string) []byte {
	var b bytes.Buffer
	for _, r := range resources {
		typ, name, ok := strings.Cut(r, ".")
		if !ok {
			continue
		}
		ignored := "all"
		if arguments, ok := partial[r]; ok {
			ignored = "[" + strings.Join(arguments, ", ") + "]"
		}
		fmt.Fprintf(&b, "resource %q %q {\n  lifecycle {\n    ignore_changes = %s\n  }\n}\n\n", typ, name, ignored)
	}
	return b.Bytes()
}

// CheckImportOnly checks that p only imports: every managed resource
// change is a no-op that imports, data sources only read, and there is
// nothing to add, change, remove or invoke. Applying such a plan writes
// state and calls no provider write, which is what makes a snapshot safe
// to build; a Terraform or OpenTofu release that changes what an import
// plans fails here, not in the cloud.
func CheckImportOnly(p *tfjson.Plan) error {
	if p == nil {
		return errors.New("no plan")
	}
	if len(p.ActionInvocations) > 0 {
		return errors.New("the plan invokes actions")
	}
	if len(p.DeferredChanges) > 0 {
		return errors.New("the plan defers changes")
	}
	for _, rc := range p.ResourceChanges {
		if rc.Change == nil {
			continue
		}
		switch rc.Mode {
		case tfjson.DataResourceMode:
			if !rc.Change.Actions.Read() && !rc.Change.Actions.NoOp() {
				return fmt.Errorf("%s: the plan does more than read it", rc.Address)
			}
		default:
			if !rc.Change.Actions.NoOp() || rc.Change.Importing == nil {
				what := strings.Join(ActionNames(rc.Change.Actions), ",")
				if changed := changedArguments(rc.Change); len(changed) > 0 {
					what += " of " + strings.Join(changed, ", ")
				}
				return fmt.Errorf("%s: the plan does more than import it (%s)", rc.Address, what)
			}
		}
	}
	return nil
}

// changedArguments returns the names of the top-level arguments whose
// values c changes, never the values: they may be secrets.
func changedArguments(c *tfjson.Change) []string {
	before, _ := c.Before.(map[string]any)
	after, _ := c.After.(map[string]any)
	unknown, _ := c.AfterUnknown.(map[string]any)
	var names []string
	for name, value := range after {
		if u, ok := unknown[name].(bool); ok && u {
			continue
		}
		if !reflect.DeepEqual(before[name], value) {
			names = append(names, name)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}
