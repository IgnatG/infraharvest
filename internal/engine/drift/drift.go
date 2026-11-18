// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package drift finds what changed in the cloud since a root was last
// applied, and takes the changes it is told to accept into the root's code
// where it can be told how, held to the plan of the edited code and to the
// checks of the verification gate. It plans a staged copy of the root, and
// never writes the root's state.
package drift

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/sentence"
)

// What changed in the cloud (Change.Kind).
const (
	// Changed: an attribute has another value in the cloud than in
	// the state.
	Changed = "changed"
	// Deleted: the resource is in the state but no longer in the
	// cloud.
	Deleted = "deleted"
)

// What the code makes of a change (Change.Outcome).
const (
	// Reverts: the code still has the value of the state, so the
	// next apply puts it back in the cloud. Check reports such a change
	// as Kept, or as what accepting it did.
	Reverts = "reverts"
	// Absorbed: the code doesn't set what changed, such as an
	// attribute the provider computes or one ignore_changes names, so the
	// next apply keeps the cloud's value.
	Absorbed = "absorbed"
	// NotAssessable: whether the next apply puts the value back
	// can't be told, such as for a secret whose variable has no value.
	NotAssessable = "not_assessable"
	// Kept: the code keeps its value, so the next apply puts it back
	// in the cloud.
	Kept = "kept"
	// Written: the cloud's value was accepted and written into the
	// code.
	Written = "written"
	// RejectedByPlan: the cloud's value was accepted, but the plan
	// of the edited code didn't take it as it should, so the edit wasn't
	// written.
	RejectedByPlan = "rejected_by_plan"
	// RejectedByCheck: the cloud's value was accepted, and the plan
	// of the edited code took it, but the edit fails a check of the
	// verification gate, such as format or secrets, so it wasn't written.
	RejectedByCheck = "rejected_by_check"
	// Manual: the cloud's value was accepted, but where the code
	// sets it takes a hand edit, as Reason says.
	Manual = "manual"
	// Secret: the cloud's value was accepted, but it is a secret,
	// which is never written into the code.
	Secret = "secret"
)

// What is decided for a change (Change.Decision).
const (
	// Accept: take the cloud's value into the code.
	Accept = "accept"
	// Keep: keep the code's value, which the next apply puts back.
	Keep = "keep"
)

// Options configure Check.
type Options struct {
	// StateOnly names, per resource type, arguments the provider keeps
	// only in state: their changes aren't drift (see
	// engine.Options.StateOnly).
	StateOnly map[string][]string
	// CommandTimeout bounds each Terraform command (see
	// engine.Options.CommandTimeout).
	CommandTimeout time.Duration
	// Accept says whether to take the cloud's value into the code for a
	// change the next apply would revert; nil keeps every change, as
	// detection alone does.
	Accept func(Change) bool
	// Rules are the engine's rules for its output, which the edits that
	// accept changes are held to (see Rules).
	Rules Rules
	// Adapters are the adapters of the modules the root may call, as
	// generation had them (see engine.Options.Adapters): a change of a
	// resource in a call of a curated module is traced to the call through
	// its adapter (see curatedAdapter).
	Adapters []adapters.Adapter
	// Environment is, for a root environments share (see
	// hclfile.MergedEnvironments), the environment to check it as: with its
	// backend settings, vars/<env>/backend.hcl, or, for a root without a
	// backend, the local state of the workspace named after it, and with
	// its var files. The environments share the root's code, so a change
	// accepted for one isn't written into it: where a variable sets the
	// whole value, or the instance's key of a map variable for_each is
	// over, and every other argument it reaches has the cloud's value too,
	// it is written into the environment's vars/<env>/terraform.tfvars
	// instead; in a block only the environment has, counted by a variable
	// only it sets true, the block is edited; else it needs a hand edit.
	Environment string
	// WithRootLock, for a root environments share, runs what Check does
	// in the root's own directory, initialise it with Environment's
	// backend settings, pull its state and stage its files, holding a lock
	// the checks of the root's other environments share: run at once,
	// they reconfigure the backend of the same directory, and a pull
	// between another's init and its own would read that environment's
	// state. nil runs it as it is.
	WithRootLock func(func() error) error
	// DataDir is where the Terraform Check is given for the root keeps its
	// working files, the TF_DATA_DIR it runs with, which Check reads the
	// modules its init installed from; "" for .terraform in the root.
	DataDir string
	// NoEnvVars says the plans get no TF_VAR_ value, as for roots from
	// someone else: every variable without a default or a tfvars value
	// gets a placeholder, whatever the environment holds.
	NoEnvVars bool
	// OutDir is the output directory the root is in: an accepted change
	// is written into a generated module the root calls only if the
	// module is in it too (see addModuleFiles). "" takes the directory the
	// module's modules directory is in for it.
	OutDir string
	// Backends are the types of the backends Check reads state from,
	// besides local state: those the clouds keep state in whose
	// providers infraharvest is built with (see harvest.Profile.Backend).
	// A root with another backend, such as one whose plans run remotely,
	// or HCP Terraform, fails with an UnsupportedBackendError.
	Backends []string

	// writeFile writes an edited file into the root; nil is
	// fsutil.WriteFile. For tests.
	writeFile func(path string, content []byte, perm os.FileMode) error
}

// Rules are what the engine knows of the code it generates that the
// edits accepting changes are held to, so that they write what
// generation would (see engine.DriftRules).
type Rules struct {
	// Standards and Moves run the verification gate's standards and moves
	// checks on a directory (see gate.Policy.Standards), which an edit may
	// add no finding to. Both must be set.
	Standards, Moves func(dir string) (gate.Check, error)
	// IsDefault reports whether expr is the value the provider gives the
	// argument at path, such as storage_class, of a resource of type typ
	// when it isn't told: a change accepted to it leaves the argument out
	// rather than writing it. nil knows no defaults.
	IsDefault func(typ, path string, expr hclsyntax.Expression) bool
	// Referrer returns, for the values of a root's resources in its
	// state, by address, what refers to a resource by a value it has
	// (see Referrer). nil refers to none.
	Referrer func(values map[string]map[string]any) Referrer
}

// A Referrer returns, for value, the cloud's value of an argument named
// argument, such as vpc_id, the resource that has it as an identifier,
// by address, and the reference to the attribute that has it, such as
// aws_vpc.main.id, which takes the value in the code; false if no
// resource's value is the one the argument names.
type Referrer func(argument, value string) (address string, ref hcl.Traversal, ok bool)

// Result is what Check found in one root.
type Result struct {
	// Backend is the type of the root's backend, local for none.
	Backend string
	// Resources counts the managed resources in the root's state.
	Resources int
	// NotApplied are the resources the root's import blocks import that
	// the state doesn't have yet: their drift can't be told.
	NotApplied []string
	// Placeholders are the root's variables without a value, from a
	// default, TF_VAR_ or a tfvars file, that got a placeholder value:
	// what they set is not assessable.
	Placeholders []string
	// Changes are what changed in the cloud, sorted by address and
	// attribute.
	Changes []Change
}

// Change is one attribute, or one resource, that changed in the
// cloud since the state last recorded it.
type Change struct {
	// Address is the resource's, or, for a change of tags several
	// resources get from one place (see Covers), that place's: local.tags,
	// or the provider's map literal, such as provider.aws.default_tags.
	Address string
	Type    string
	// ModuleAddress is the module call the resource is in, such as
	// module.logs, or "".
	ModuleAddress string
	// Attribute is the path of what changed, such as tags.Env or
	// ingress[1].cidr_blocks[0]; tags_all, effective_labels and
	// terraform_labels are folded into tags and labels. It is "" when the
	// resource was deleted.
	Attribute string
	// Before is the state's value, After the cloud's, with secrets
	// replaced by "(sensitive value)".
	Before, After any
	Kind          string // Changed or Deleted
	// Decision is Accept or Keep.
	Decision string
	// Outcome is Absorbed or NotAssessable, or, for a change the
	// next apply would revert, Kept, Written,
	// RejectedByPlan, RejectedByCheck, Manual or
	// Secret.
	Outcome string
	Reason  string
	// ForcesReplacement says the next apply would replace the resource
	// to put the code's value back: keeping the code replaces it.
	ForcesReplacement bool
	// Source is where the root's configuration sets the attribute, as
	// best it can be told: for a resource in a module call, the call's
	// argument the module gets it from; nil when it can't be told.
	Source *Source

	// Covers are, for a change of tags several resources get from one
	// place, the resources it is of, sorted: each changed the key to the
	// same value, or removed it, so that accepting it edits that place
	// once. Type is then "".
	Covers []string

	// IDs are what the state records the resource as, to find it in the
	// cloud's audit log: its id, and its arn or self_link where it has one
	// (see stateIDs). None for a change of Covers.
	IDs []string

	// value is the cloud's value, and sensitive whether the plans mark
	// it sensitive: for the edit that accepts it, never for a report.
	value     any
	sensitive bool
	// shared is what a change of shared tags holds (see shareTags).
	shared *tagShare
}

// Source is where a root's configuration sets an attribute.
type Source struct {
	File       string // relative to the root
	Line       int
	Expression string // the expression that sets it, if on one line
	// ModulePath says, for a resource in a module call, how the module
	// gets the value from the call's argument at File and Line: the
	// module's file, line and expression, or, for a curated module, its
	// release and the input its adapter maps the attribute onto; for calls
	// inside modules, each call's, from the root's inwards, separated by
	// "; ".
	ModulePath string
}

// UnsupportedBackendError is the error of a root whose backend Check
// doesn't read state from.
type UnsupportedBackendError struct {
	Backend string // the backend's type, or "cloud" for HCP Terraform
	// Supported are the backends Check reads state from, besides local
	// state (see Options.Backends).
	Supported []string
}

func (e *UnsupportedBackendError) Error() string {
	supported := "drift supports local state"
	switch {
	case len(e.Supported) == 1:
		supported = "drift supports the " + e.Supported[0] + " backend and local state"
	case len(e.Supported) > 1:
		supported = "drift supports the " + sentence.List(e.Supported, "and") + " backends and local state"
	}
	if e.Backend == "cloud" {
		return "HCP Terraform (the cloud block) isn't supported, as its plans run remotely: " + supported
	}
	return fmt.Sprintf("the %q backend isn't supported, as drift plans locally against the state it pulls: %s", e.Backend, supported)
}

// Check finds what changed in the cloud since the root in dir was last
// applied, with one refresh of the cloud. It initialises the root with
// its backend, through tf, and pulls the state; then, in staging, a
// private directory it creates and removes, it plans a copy of the root
// with that state as local state and no backend, through the Terraform
// staged returns for it: a refresh-only plan says what differs in the
// cloud; applied to the copy's state, which is all it writes, a plan of
// the code without refreshing again says which of those changes the
// next apply would put back. The root's state is never written: plans
// don't lock it, as a lock needs write access.
//
// The changes the next apply would put back that opts.Accept accepts are
// taken into the root's code, where it can be told how (see codeSource):
// edited in the copy, planned again without refreshing, and written into
// the root only once the plan and the checks of the verification gate
// say the edits take them and change nothing else (see acceptDrift); the
// others are kept. For a root environments share, checked as
// opts.Environment, only that environment's var file, staged with the
// copy, is edited, for a value a variable sets (see
// variableResolver.env). Each change's Outcome says what became of it.
//
// A root with an unsupported backend fails with an
// UnsupportedBackendError before Terraform runs.
func Check(ctx context.Context, tf tfrun.DriftTerraform, dir, staging string, staged func(dir string) (tfrun.DriftTerraform, error), opts Options) (*Result, error) {
	s, err := openDriftSession(dir, staging, opts)
	if err != nil {
		return nil, err
	}
	defer s.close()
	result := &Result{Backend: s.backend}
	applied, err := s.stage(ctx, tfrun.DriftWithTimeout(tf, opts.CommandTimeout))
	if err != nil {
		return nil, err
	}
	if !applied {
		result.NotApplied = s.notApplied(nil)
		return result, nil
	}
	if err := s.open(ctx, staged); err != nil {
		return nil, err
	}
	inState, err := s.inState(ctx)
	if err != nil {
		return nil, err
	}
	result.Resources, result.NotApplied = len(inState), s.notApplied(inState)
	if result.Placeholders, err = s.placeholders(); err != nil {
		return nil, err
	}
	refresh, err := s.refresh(ctx)
	if err != nil {
		return nil, err
	}
	if !hasDrift(refresh) {
		return result, nil
	}
	code, err := s.refreshed(ctx)
	if err != nil {
		return nil, err
	}
	result.Changes = classifyDrift(refresh, code, opts.StateOnly, len(result.Placeholders) > 0)
	root, err := s.contents()
	if err != nil {
		return nil, err
	}
	editor, err := s.editor(refresh, code, root)
	if err != nil {
		return nil, err
	}
	if result.Changes, err = shareTags(result.Changes, root, refresh, code, editor); err != nil {
		return nil, err
	}
	sources, err := addDriftSources(result.Changes, root, editor)
	if err != nil {
		return nil, err
	}
	if err := s.accept(ctx, root, decideDrift(result.Changes, sources, opts.Accept), code, editor); err != nil {
		return nil, err
	}
	result.Changes = unsharedChanges(result.Changes)
	return result, nil
}

// rootBackend returns the type of the backend the configuration files
// configure, local if none, or an UnsupportedBackendError for one other
// than local and backends.
func rootBackend(files []*hclfile.File, backends []string) (string, error) {
	backend := "local"
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			if b.Type != "terraform" {
				continue
			}
			for _, inner := range b.Body.Blocks {
				switch {
				case inner.Type == "cloud":
					return "", &UnsupportedBackendError{Backend: "cloud", Supported: backends}
				case inner.Type == "backend" && len(inner.Labels) == 1:
					backend = inner.Labels[0]
				}
			}
		}
	}
	if backend != "local" && !slices.Contains(backends, backend) {
		return "", &UnsupportedBackendError{Backend: backend, Supported: backends}
	}
	return backend, nil
}

// stateHasResources reports whether a pulled state, as JSON, has any
// resource; an empty one, as state pull prints for a root never applied,
// has none.
func stateHasResources(state string) bool {
	if strings.TrimSpace(state) == "" {
		return false
	}
	var s struct {
		Resources []json.RawMessage `json:"resources"`
	}
	if err := json.Unmarshal([]byte(state), &s); err != nil {
		// Terraform plans with it all the same, and says what is wrong.
		return true
	}
	return len(s.Resources) > 0
}

// stageDrift writes into staging, which it creates private to the user
// (the state holds secrets), a copy of the root in dir: its files without
// backend.tf, any backend or cloud block or state, with the sources of
// local module calls rewritten to reach the same modules from staging, or
// copies in it of the generated ones (see stageModule),
// and state as its local terraform.tfstate. Subdirectories, such as a
// module of the root's own, stay where they are.
func stageDrift(dir, staging string, state []byte) error {
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(staging), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		return err
	}
	// The copy is removed once planned: its access needn't be restored.
	if _, err := fsutil.MakePrivate(staging); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || name == hclfile.BackendFileName || strings.HasPrefix(name, tfrun.LocalStateFileName) || name == ".terraform.tfstate.lock.info" {
			continue
		}
		path := filepath.Join(dir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(name, ".tf") {
			if content, err = stagedConfig(dir, staging, path, content); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(staging, name), content, 0o600); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(staging, tfrun.LocalStateFileName), state, 0o600)
}

// stagedConfig returns content, the configuration file at path of the
// root in dir, as the copy in staging has it: without backend or cloud
// blocks, and with local module sources relative to staging, or calling
// copies in it of the generated modules (see stageModule).
func stagedConfig(dir, staging, path string, content []byte) ([]byte, error) {
	f, err := hclfile.Parse(path, content)
	if err != nil {
		return nil, err
	}
	blocks := f.Edit().Body().Blocks()
	changed := false
	for i, b := range f.Syntax.Blocks {
		if i >= len(blocks) {
			break
		}
		switch b.Type {
		case "terraform":
			for _, inner := range blocks[i].Body().Blocks() {
				if inner.Type() == "backend" || inner.Type() == "cloud" {
					blocks[i].Body().RemoveBlock(inner)
					changed = true
				}
			}
		case "module":
			m := gate.LocalModuleDir(dir, b)
			if m == "" {
				continue
			}
			abs, err := filepath.Abs(m)
			if err != nil {
				return nil, err
			}
			if generatedModuleDir(abs) {
				// A copy, which an accepted change may edit (see
				// promoteLiteral).
				copied, err := stageModule(staging, abs)
				if err != nil {
					return nil, err
				}
				if copied != "" {
					blocks[i].Body().SetAttributeValue("source", cty.StringVal(copied))
					changed = true
					continue
				}
			}
			source, err := hclfile.CallSource(staging, abs)
			if err != nil {
				return nil, err
			}
			blocks[i].Body().SetAttributeValue("source", cty.StringVal(source))
			changed = true
		}
	}
	if !changed {
		return content, nil
	}
	return f.Edit().Bytes(), nil
}

// stateAddresses returns the addresses of the managed resources in state.
func stateAddresses(state *tfjson.State) map[string]bool {
	addresses := map[string]bool{}
	if state == nil || state.Values == nil {
		return addresses
	}
	var walk func(m *tfjson.StateModule)
	walk = func(m *tfjson.StateModule) {
		if m == nil {
			return
		}
		for _, r := range m.Resources {
			if r.Mode == tfjson.ManagedResourceMode {
				addresses[r.Address] = true
			}
		}
		for _, c := range m.ChildModules {
			walk(c)
		}
	}
	walk(state.Values.RootModule)
	return addresses
}

// notApplied returns the addresses the import blocks of files import
// into that inState doesn't have, sorted; those with for_each are left
// out, as their addresses are known only once planned.
func notApplied(files []*hclfile.File, inState map[string]bool) []string {
	var targets []string
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			if b.Type != "import" {
				continue
			}
			if _, ok := b.Body.Attributes["for_each"]; ok {
				continue
			}
			to, ok := b.Body.Attributes["to"]
			if !ok {
				continue
			}
			rng := to.Expr.Range()
			address := strings.Join(strings.Fields(string(f.Src[rng.Start.Byte:rng.End.Byte])), "")
			if !inState[address] {
				targets = append(targets, address)
			}
		}
	}
	sort.Strings(targets)
	return slices.Compact(targets)
}
