// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
)

// DriftTerraform is a Terraform that can also read a root's state and
// apply a saved plan, which only Drift needs: *Runner is one.
type DriftTerraform interface {
	Terraform
	StatePull(ctx context.Context, opts ...tfexec.StatePullOption) (string, error)
	Apply(ctx context.Context, opts ...tfexec.ApplyOption) error
	ShowStateFile(ctx context.Context, statePath string, opts ...tfexec.ShowOption) (*tfjson.State, error)
}

var _ DriftTerraform = (*Runner)(nil)

// What changed in the cloud (DriftChange.Kind).
const (
	// DriftChanged: an attribute has another value in the cloud than in
	// the state.
	DriftChanged = "changed"
	// DriftDeleted: the resource is in the state but no longer in the
	// cloud.
	DriftDeleted = "deleted"
)

// What the code makes of a change (DriftChange.Outcome).
const (
	// DriftReverts: the code still has the value of the state, so the
	// next apply puts it back in the cloud. Drift reports such a change
	// as DriftKept, or as what accepting it did.
	DriftReverts = "reverts"
	// DriftAbsorbed: the code doesn't set what changed, such as an
	// attribute the provider computes or one ignore_changes names, so the
	// next apply keeps the cloud's value.
	DriftAbsorbed = "absorbed"
	// DriftNotAssessable: whether the next apply puts the value back
	// can't be told, such as for a secret whose variable has no value.
	DriftNotAssessable = "not_assessable"
	// DriftKept: the code keeps its value, so the next apply puts it back
	// in the cloud.
	DriftKept = "kept"
	// DriftWritten: the cloud's value was accepted and written into the
	// code.
	DriftWritten = "written"
	// DriftRejectedByPlan: the cloud's value was accepted, but the plan
	// of the edited code didn't take it as it should, so the edit wasn't
	// written.
	DriftRejectedByPlan = "rejected_by_plan"
	// DriftManual: the cloud's value was accepted, but where the code
	// sets it takes a hand edit, as Reason says.
	DriftManual = "manual"
	// DriftSecret: the cloud's value was accepted, but it is a secret,
	// which is never written into the code.
	DriftSecret = "secret"
)

// What is decided for a change (DriftChange.Decision).
const (
	// DriftAccept: take the cloud's value into the code.
	DriftAccept = "accept"
	// DriftKeep: keep the code's value, which the next apply puts back.
	DriftKeep = "keep"
)

// driftBackends are the backends Drift reads state from: the others, and
// HCP Terraform, run plans remotely.
var driftBackends = []string{"s3", "gcs", "azurerm", "local"}

// backendFileName is the file of a generated root that configures its
// backend; the staged copy leaves it out.
const backendFileName = "backend.tf"

// stagedStateFile is the state the staged copy plans with.
const stagedStateFile = "terraform.tfstate"

// DriftOptions configure Drift.
type DriftOptions struct {
	// StateOnly names, per resource type, arguments the provider keeps
	// only in state: their changes aren't drift (see Options.StateOnly).
	StateOnly map[string][]string
	// CommandTimeout bounds each Terraform command (see
	// Options.CommandTimeout).
	CommandTimeout time.Duration
	// Accept says whether to take the cloud's value into the code for a
	// change the next apply would revert; nil keeps every change, as
	// detection alone does.
	Accept func(DriftChange) bool
	// Defaults name, per resource type, the values the provider gives
	// arguments it isn't told (see Options.Defaults): a change accepted
	// to one of them leaves the argument out rather than writing it.
	Defaults map[string][]string
	// Omit names, per resource type, the arguments the output leaves out
	// (see Options.Omit), which the standards check of the edited code
	// looks for.
	Omit map[string][]string
	// Environment is, for a root environments share (see
	// MergedEnvironments), the environment to check it as: with its
	// backend settings, vars/<env>/backend.hcl, or, for a root without a
	// backend, the local state of the workspace named after it, and with
	// its var files. The environments share the root's code, so a change
	// accepted for one isn't written into it: where a variable no other
	// argument uses sets the whole value, it is written into the
	// environment's vars/<env>/terraform.tfvars instead; else it needs a
	// hand edit.
	Environment string

	// writeFile writes an edited file into the root; nil is
	// fsutil.WriteFile. For tests.
	writeFile func(path string, content []byte, perm os.FileMode) error
}

// RootDrift is what Drift found in one root.
type RootDrift struct {
	// Backend is the type of the root's backend, local for none.
	Backend string
	// Resources counts the managed resources in the root's state.
	Resources int
	// NotApplied are the resources the root's import blocks import that
	// the state doesn't have yet: Drift can't check them.
	NotApplied []string
	// Placeholders are the root's variables without a value, from a
	// default, TF_VAR_ or a tfvars file, that got a placeholder value:
	// what they set is not assessable.
	Placeholders []string
	// Changes are what changed in the cloud, sorted by address and
	// attribute.
	Changes []DriftChange
}

// DriftChange is one attribute, or one resource, that changed in the
// cloud since the state last recorded it.
type DriftChange struct {
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
	Kind          string // DriftChanged or DriftDeleted
	// Decision is DriftAccept or DriftKeep.
	Decision string
	// Outcome is DriftAbsorbed or DriftNotAssessable, or, for a change the
	// next apply would revert, DriftKept, DriftWritten,
	// DriftRejectedByPlan, DriftManual or DriftSecret.
	Outcome string
	Reason  string
	// ForcesReplacement says the next apply would replace the resource
	// to put the code's value back: keeping the code replaces it.
	ForcesReplacement bool
	// Source is where the root's configuration sets the attribute, as
	// best it can be told: for a resource in a module call, the call's
	// argument the module gets it from; nil when it can't be told.
	Source *DriftSource

	// Covers are, for a change of tags several resources get from one
	// place, the resources it is of, sorted: each changed the key to the
	// same value, or removed it, so that accepting it edits that place
	// once. Type is then "".
	Covers []string

	// value is the cloud's value, and sensitive whether the plans mark
	// it sensitive: for the edit that accepts it, never for a report.
	value     any
	sensitive bool
	// shared is what a change of shared tags holds (see shareTags).
	shared *tagShare
}

// DriftSource is where a root's configuration sets an attribute.
type DriftSource struct {
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

// UnsupportedBackendError is the error of a root whose backend Drift
// doesn't read state from.
type UnsupportedBackendError struct {
	Backend string // the backend's type, or "cloud" for HCP Terraform
}

func (e *UnsupportedBackendError) Error() string {
	supported := "drift supports the s3, gcs and azurerm backends and local state"
	if e.Backend == "cloud" {
		return "HCP Terraform (the cloud block) isn't supported, as its plans run remotely: " + supported
	}
	return fmt.Sprintf("the %q backend isn't supported, as drift plans locally against the state it pulls: %s", e.Backend, supported)
}

// Drift finds what changed in the cloud since the root in dir was last
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
// taken into the root's code, where it can be told how (see Source):
// edited in the copy, planned again without refreshing, and written into
// the root only once the plan and the checks of the verification gate
// say the edits take them and change nothing else (see acceptDrift); the
// others are kept. For a root environments share, checked as
// opts.Environment, only that environment's var file, staged with the
// copy, is edited, for a value a variable sets (see envVariable). Each
// change's Outcome says what became of it.
//
// A root with an unsupported backend fails with an
// UnsupportedBackendError before Terraform runs.
func Drift(ctx context.Context, tf DriftTerraform, dir, staging string, staged func(dir string) (DriftTerraform, error), opts DriftOptions) (*RootDrift, error) {
	files, err := configFiles(dir)
	if err != nil {
		return nil, err
	}
	backend, err := rootBackend(files)
	if err != nil {
		return nil, err
	}
	result := &RootDrift{Backend: backend}
	tf = driftWithTimeout(tf, opts.CommandTimeout)
	var envImports map[string]string
	if opts.Environment != "" {
		if envImports, err = MergedImportIDs(dir, opts.Environment); err != nil {
			return nil, err
		}
	}
	state, err := pullDriftState(ctx, tf, dir, backend, opts.Environment)
	if err != nil {
		return nil, err
	}
	if !stateHasResources(state) {
		result.NotApplied = notApplied(files, nil)
		if opts.Environment != "" {
			result.NotApplied = notImported(envImports, nil)
		}
		return result, nil
	}
	defer os.RemoveAll(staging)
	if err := stageDrift(dir, staging, []byte(state)); err != nil {
		return nil, err
	}
	// The plans read the environment's var files from the staged copy,
	// where an accepted change is edited first.
	var envVars []tfexec.PlanOption
	var envVarPaths []string
	if opts.Environment != "" {
		if err := stageVarFiles(dir, staging, opts.Environment); err != nil {
			return nil, err
		}
		if envVars, envVarPaths, err = mergedVarFiles(staging, opts.Environment); err != nil {
			return nil, err
		}
	}
	stf, err := staged(staging)
	if err != nil {
		return nil, err
	}
	stf = driftWithTimeout(stf, opts.CommandTimeout)
	if err := initTerraform(ctx, stf); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	shown, err := stf.ShowStateFile(ctx, filepath.Join(staging, stagedStateFile))
	if err != nil {
		return nil, fmt.Errorf("terraform show: %w", err)
	}
	inState := stateAddresses(shown)
	result.Resources = len(inState)
	result.NotApplied = notApplied(files, inState)
	if opts.Environment != "" {
		result.NotApplied = notImported(envImports, inState)
	}

	stagedFiles, err := configFiles(staging)
	if err != nil {
		return nil, err
	}
	vars, placeholdered := variablePlaceholders(staging, stagedFiles, envVarPaths...)
	vars = append(envVars, vars...)
	result.Placeholders = placeholdered

	refreshPlan := filepath.Join(staging, "refresh.tfplan")
	refresh, err := savedPlan(ctx, stf, refreshPlan, append(slices.Clone(vars), tfexec.RefreshOnly(true), tfexec.Lock(false))...)
	if err != nil {
		return nil, fmt.Errorf("terraform plan -refresh-only: %w", err)
	}
	if !hasDrift(refresh) {
		return result, nil
	}
	// A saved plan applies without refreshing again; this one only
	// writes the copy's local state.
	if err := stf.Apply(ctx, tfexec.DirOrPlan(refreshPlan), tfexec.Lock(false)); err != nil {
		return nil, fmt.Errorf("terraform apply of the refresh-only plan to the staged state: %w", err)
	}
	code, err := savedPlan(ctx, stf, filepath.Join(staging, "code.tfplan"), append(slices.Clone(vars), tfexec.Refresh(false), tfexec.Lock(false))...)
	if err != nil {
		return nil, fmt.Errorf("terraform plan -refresh=false: %w", err)
	}
	result.Changes = classifyDrift(refresh, code, opts.StateOnly, len(placeholdered) > 0)
	root, err := driftContents(dir, opts.Environment)
	if err != nil {
		return nil, err
	}
	editor := newDriftEditor(refresh, code, opts.Defaults)
	editor.modules = loadDriftModules(dir)
	if opts.Environment != "" {
		editor.env, editor.envVars = opts.Environment, envVarsPath(opts.Environment)
	}
	if result.Changes, err = shareTags(result.Changes, root, refresh, code, editor); err != nil {
		return nil, err
	}
	sources, err := addDriftSources(result.Changes, root, editor)
	if err != nil {
		return nil, err
	}
	accepted := decideDrift(result.Changes, sources, opts.Accept)
	if len(accepted) > 0 {
		if err := acceptDrift(ctx, stf, dir, staging, root, accepted, code, vars, editor, opts); err != nil {
			return nil, err
		}
	}
	result.Changes = unsharedChanges(result.Changes)
	return result, nil
}

// rootBackend returns the type of the backend the configuration files
// configure, local if none, or an UnsupportedBackendError.
func rootBackend(files []*hclFile) (string, error) {
	backend := "local"
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			if b.Type != "terraform" {
				continue
			}
			for _, inner := range b.Body.Blocks {
				switch {
				case inner.Type == "cloud":
					return "", &UnsupportedBackendError{Backend: "cloud"}
				case inner.Type == "backend" && len(inner.Labels) == 1:
					backend = inner.Labels[0]
				}
			}
		}
	}
	if !slices.Contains(driftBackends, backend) {
		return "", &UnsupportedBackendError{Backend: backend}
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
// local module calls rewritten to reach the same modules from staging,
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
	if _, err := makePrivate(staging); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || name == backendFileName || strings.HasPrefix(name, stagedStateFile) || name == ".terraform.tfstate.lock.info" {
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
	return os.WriteFile(filepath.Join(staging, stagedStateFile), state, 0o600)
}

// stagedConfig returns content, the configuration file at path of the
// root in dir, as the copy in staging has it: without backend or cloud
// blocks, and with local module sources relative to staging.
func stagedConfig(dir, staging, path string, content []byte) ([]byte, error) {
	wf, diags := hclwrite.ParseConfig(content, path, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse %s: %w", path, diags)
	}
	sf, diags := hclsyntax.ParseConfig(content, path, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse %s: %w", path, diags)
	}
	syntax, ok := sf.Body.(*hclsyntax.Body)
	if !ok {
		return content, nil
	}
	blocks := wf.Body().Blocks()
	changed := false
	for i, b := range syntax.Blocks {
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
			m := localModuleDir(dir, b)
			if m == "" {
				continue
			}
			abs, err := filepath.Abs(m)
			if err != nil {
				return nil, err
			}
			source, err := callSource(staging, abs)
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
	return wf.Bytes(), nil
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
func notApplied(files []*hclFile, inState map[string]bool) []string {
	var targets []string
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
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
			address := strings.Join(strings.Fields(string(f.src[rng.Start.Byte:rng.End.Byte])), "")
			if !inState[address] {
				targets = append(targets, address)
			}
		}
	}
	sort.Strings(targets)
	return slices.Compact(targets)
}

// variablePlaceholders returns -var options with a placeholder value for
// each variable of files without a default, a TF_VAR_ environment
// variable or a value in a tfvars file of dir that Terraform reads by
// itself, or in varFiles, the other var files the plans read, and their
// names, sorted: a plan with -input=false fails without them.
func variablePlaceholders(dir string, files []*hclFile, varFiles ...string) ([]tfexec.PlanOption, []string) {
	set := tfvarsNames(dir, varFiles...)
	var names []string
	types := map[string]string{}
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			if b.Type != "variable" || len(b.Labels) != 1 {
				continue
			}
			name := b.Labels[0]
			if _, ok := b.Body.Attributes["default"]; ok || set[name] {
				continue
			}
			if _, ok := os.LookupEnv("TF_VAR_" + name); ok {
				continue
			}
			names = append(names, name)
			if t, ok := b.Body.Attributes["type"]; ok {
				rng := t.Expr.Range()
				types[name] = strings.TrimSpace(string(f.src[rng.Start.Byte:rng.End.Byte]))
			}
		}
	}
	sort.Strings(names)
	names = slices.Compact(names)
	vars := make([]tfexec.PlanOption, 0, len(names))
	for _, name := range names {
		vars = append(vars, tfexec.Var(name+"="+placeholderFor(types[name])))
	}
	return vars, names
}

// placeholderFor returns a placeholder value, as -var takes it, for a
// variable of the type constraint typ.
func placeholderFor(typ string) string {
	switch {
	case typ == "number":
		return "0"
	case typ == "bool":
		return "false"
	case strings.HasPrefix(typ, "list(") || strings.HasPrefix(typ, "set(") || strings.HasPrefix(typ, "tuple("):
		return "[]"
	case strings.HasPrefix(typ, "map(") || strings.HasPrefix(typ, "object("):
		return "{}"
	}
	return placeholderString
}

// tfvarsNames returns the variables that terraform.tfvars and the
// *.auto.tfvars files of dir set, and those of the var files more.
func tfvarsNames(dir string, more ...string) map[string]bool {
	names := map[string]bool{}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.auto.tfvars"))
	paths = append(paths, filepath.Join(dir, "terraform.tfvars"))
	paths = append(paths, more...)
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		f, diags := hclsyntax.ParseConfig(content, path, hcl.InitialPos)
		if diags.HasErrors() {
			continue
		}
		if body, ok := f.Body.(*hclsyntax.Body); ok {
			for name := range body.Attributes {
				names[name] = true
			}
		}
	}
	return names
}

// savedPlan plans with opts into the plan file path and returns it as
// JSON, or the plan's errors.
func savedPlan(ctx context.Context, tf DriftTerraform, path string, opts ...tfexec.PlanOption) (*tfjson.Plan, error) {
	diags, summary, err := plan(ctx, tf, append(opts, tfexec.Out(path))...)
	if err != nil {
		return nil, err
	}
	if len(diags) > 0 {
		return nil, diagnosticsError(diags)
	}
	if summary == nil {
		return nil, errors.New("terraform plan reported no change summary")
	}
	if _, err := os.Stat(path); err == nil {
		_ = os.Chmod(path, 0o600)
	}
	p, err := tf.ShowPlanFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("terraform show: %w", err)
	}
	return p, nil
}

// hasDrift reports whether a refresh-only plan found a managed resource
// that differs in the cloud.
func hasDrift(p *tfjson.Plan) bool {
	if p == nil {
		return false
	}
	for _, rc := range p.ResourceDrift {
		if rc.Mode == tfjson.ManagedResourceMode && rc.Change != nil {
			return true
		}
	}
	return false
}

// valueLeaf is one attribute that differs between two values: its path,
// as changedLeaves writes it, both values, whether the second is known
// only after apply, and whether either is sensitive.
type valueLeaf struct {
	path          string
	before, after any
	unknown       bool
	sensitive     bool
}

// valueLeaves returns the attributes that differ between before and
// after, as changedLeaves does, with their values and whether the masks
// mark them sensitive, sorted by path.
func valueLeaves(before, after, afterUnknown, beforeSensitive, afterSensitive any) []valueLeaf {
	var leaves []valueLeaf
	diffValues("", before, after, afterUnknown, beforeSensitive, afterSensitive, false, &leaves)
	sort.Slice(leaves, func(i, j int) bool { return leaves[i].path < leaves[j].path })
	return leaves
}

func diffValues(path string, before, after, unknown, bs, as any, sensitive bool, out *[]valueLeaf) {
	sensitive = sensitive || marked(bs) || marked(as)
	if u, ok := unknown.(bool); ok && u {
		*out = append(*out, valueLeaf{path: path, before: before, unknown: true, sensitive: sensitive})
		return
	}
	bm, bok := before.(map[string]any)
	am, aok := after.(map[string]any)
	um, uok := unknown.(map[string]any)
	bl, blok := before.([]any)
	al, alok := after.([]any)
	ul, ulok := unknown.([]any)
	n := len(*out)
	switch {
	case (bok || before == nil) && (aok || after == nil) && (bok || aok || uok):
		keys := map[string]bool{}
		for _, m := range []map[string]any{bm, am, um} {
			for k := range m {
				keys[k] = true
			}
		}
		for k := range keys {
			sub := k
			if path != "" {
				sub = path + "." + k
			}
			diffValues(sub, bm[k], am[k], um[k], maskKey(bs, k), maskKey(as, k), sensitive, out)
		}
	case (blok || before == nil) && (alok || after == nil) && (blok || alok || ulok):
		for i := 0; i < max(len(bl), len(al), len(ul)); i++ {
			diffValues(fmt.Sprintf("%s[%d]", path, i), at(bl, i), at(al, i), at(ul, i), maskIndex(bs, i), maskIndex(as, i), sensitive, out)
		}
	}
	// A JSON document, such as a policy, that only its formatting changed
	// is the same value (see driftSameValue).
	if len(*out) == n && !driftSameValue(before, after) {
		*out = append(*out, valueLeaf{path: path, before: before, after: after, sensitive: sensitive})
	}
}

func marked(mask any) bool {
	b, ok := mask.(bool)
	return ok && b
}

func maskKey(mask any, k string) any {
	m, _ := mask.(map[string]any)
	return m[k]
}

func maskIndex(mask any, i int) any {
	l, _ := mask.([]any)
	return at(l, i)
}

// foldedAttributes are the attributes that hold a resource's tags or
// labels together with those of the provider: their changes are reported
// as the resource's own.
var foldedAttributes = map[string]string{"tags_all": "tags", "effective_labels": "labels", "terraform_labels": "labels"}

// driftLeaves returns the leaves of a resource of type typ that count as
// drift: without the arguments stateOnly names, which the cloud doesn't
// have, and write-only arguments (*_wo), which Terraform never reads
// back; with tags_all, effective_labels and terraform_labels folded into
// tags and labels, each path once.
func driftLeaves(typ string, leaves []valueLeaf, stateOnly map[string][]string) []valueLeaf {
	skip := stateOnlyOf(stateOnly, typ)
	seen := map[string]bool{}
	var kept []valueLeaf
	for _, l := range leaves {
		top := topOf(l.path)
		if slices.Contains(skip, top) || strings.HasSuffix(top, "_wo") {
			continue
		}
		if to, ok := foldedAttributes[top]; ok {
			l.path = to + l.path[len(top):]
		}
		if seen[l.path] {
			continue
		}
		seen[l.path] = true
		kept = append(kept, l)
	}
	return kept
}

// plannedChange is what the plan of the code does to one resource.
type plannedChange struct {
	actions tfjson.Actions
	leaves  []valueLeaf
	// replacePaths are the paths that force the replacement, as the plan
	// has them.
	replacePaths []any
}

// find returns the leaf of c on path, or on a path that holds it or that
// it holds, such as tags for tags.Env.
func (c *plannedChange) find(path string) (valueLeaf, bool) {
	for _, l := range c.leaves {
		if l.path == path || within(path, l.path) || within(l.path, path) {
			return l, true
		}
	}
	return valueLeaf{}, false
}

// within reports whether path is inside parent, such as tags.Env in tags
// or ingress[0].cidr_blocks in ingress.
func within(path, parent string) bool {
	if parent == "" {
		return true
	}
	return strings.HasPrefix(path, parent) && len(path) > len(parent) && (path[len(parent)] == '.' || path[len(parent)] == '[')
}

// classifyDrift returns the changes of the refresh-only plan refresh,
// each attribute of each resource it finds changed in the cloud, as the
// plan of the code with the refreshed state, code, takes them: the code
// reverts a change it plans on the same attribute, and absorbs one it
// doesn't. Resources the code still imports are left out. With
// placeholders, a sensitive attribute the code changes may only be the
// placeholder's: it is not assessable.
func classifyDrift(refresh, code *tfjson.Plan, stateOnly map[string][]string, placeholders bool) []DriftChange {
	if refresh == nil {
		return nil
	}
	if code == nil {
		code = &tfjson.Plan{}
	}
	planned := map[string]*plannedChange{}
	importing := map[string]bool{}
	for _, rc := range code.ResourceChanges {
		if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil {
			continue
		}
		if rc.Change.Importing != nil {
			importing[rc.Address] = true
			continue
		}
		leaves := valueLeaves(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown, rc.Change.BeforeSensitive, rc.Change.AfterSensitive)
		planned[rc.Address] = &plannedChange{actions: rc.Change.Actions, leaves: driftLeaves(rc.Type, leaves, stateOnly), replacePaths: rc.Change.ReplacePaths}
	}
	forms := driftSensitiveForms(refresh, code)
	var changes []DriftChange
	for _, rc := range refresh.ResourceDrift {
		if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil || importing[rc.Address] {
			continue
		}
		base := DriftChange{Address: rc.Address, Type: rc.Type, ModuleAddress: rc.ModuleAddress}
		p := planned[rc.Address]
		if rc.Change.Actions.Delete() {
			c := base
			c.Kind, c.Outcome = DriftDeleted, DriftAbsorbed
			c.Reason = "the code no longer has the resource"
			if p != nil && (p.actions.Create() || p.actions.Replace()) {
				c.Outcome, c.Reason = DriftReverts, "the next apply creates the resource again"
			}
			changes = append(changes, c)
			continue
		}
		if !rc.Change.Actions.Update() {
			continue
		}
		leaves := valueLeaves(rc.Change.Before, rc.Change.After, nil, rc.Change.BeforeSensitive, rc.Change.AfterSensitive)
		for _, l := range driftLeaves(rc.Type, leaves, stateOnly) {
			c := base
			c.Kind, c.Attribute = DriftChanged, l.path
			c.Before, c.After = redactDriftValue(l.before, l.sensitive, forms), redactDriftValue(l.after, l.sensitive, forms)
			c.value, c.sensitive = l.after, l.sensitive
			c.Outcome = DriftAbsorbed
			if p != nil {
				if cl, ok := p.find(l.path); ok {
					switch {
					case cl.unknown:
						c.Outcome, c.Reason = DriftNotAssessable, "the code's value is known only after apply"
					case cl.sensitive && placeholders:
						c.Outcome, c.Reason = DriftNotAssessable, "a sensitive value whose variable has no value here: set its TF_VAR_ variable to assess it"
					default:
						c.Outcome = DriftReverts
						if p.actions.Replace() {
							c.Reason = "the next apply replaces the resource to put the code's value back"
							c.ForcesReplacement = forcesReplacement(p.replacePaths, l.path)
						}
					}
				}
			}
			changes = append(changes, c)
		}
	}
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Address != changes[j].Address {
			return changes[i].Address < changes[j].Address
		}
		return changes[i].Attribute < changes[j].Attribute
	})
	return changes
}

// forcesReplacement reports whether the attribute at path is one of
// paths, those a plan says force a replacement, or is in one; without
// paths, a replacement is taken to be the attribute's.
func forcesReplacement(paths []any, path string) bool {
	if len(paths) == 0 {
		return true
	}
	for _, p := range paths {
		steps, ok := p.([]any)
		if !ok || len(steps) == 0 {
			continue
		}
		if first, ok := steps[0].(string); ok && first == topOf(path) {
			return true
		}
	}
	return false
}

// driftSensitiveForms returns the values the plans mark sensitive, in
// the forms text quotes them in (see textForms): no report may hold one.
func driftSensitiveForms(plans ...*tfjson.Plan) []string {
	seen := map[string]bool{}
	for _, p := range plans {
		if p == nil {
			continue
		}
		for _, rcs := range [][]*tfjson.ResourceChange{p.ResourceDrift, p.ResourceChanges} {
			for _, rc := range rcs {
				if rc.Change == nil {
					continue
				}
				collectSensitive(rc.Change.Before, rc.Change.BeforeSensitive, seen)
				collectSensitive(rc.Change.After, rc.Change.AfterSensitive, seen)
			}
		}
	}
	values := make([]string, 0, len(seen))
	for v := range seen {
		values = append(values, v)
	}
	sort.Strings(values)
	return textForms(values)
}

// redactDriftValue returns v as a report may show it: "(sensitive value)"
// if sensitive, else with every string that looks like a credential (see
// credentialFormat) replaced, and every sensitive value of forms in the
// others.
func redactDriftValue(v any, sensitive bool, forms []string) any {
	if sensitive {
		return redacted
	}
	switch v := v.(type) {
	case string:
		if credentialFormat(v) != "" {
			return redacted
		}
		return redactText(v, forms)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, sub := range v {
			out[redactText(k, forms)] = redactDriftValue(sub, false, forms)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, sub := range v {
			out[i] = redactDriftValue(sub, false, forms)
		}
		return out
	}
	return v
}

// maxSourceExpression is the longest expression a DriftSource quotes.
const maxSourceExpression = 120

// addDriftSources sets, as best it can, where the root's code, contents
// by file name, sets each change's attribute (see driftEditor.resolve),
// and returns the sources it found, by change.
func addDriftSources(changes []DriftChange, contents map[string][]byte, editor *driftEditor) ([]Source, error) {
	cfg, err := parseDriftConfig(contents)
	if err != nil {
		return nil, err
	}
	sources := make([]Source, len(changes))
	for i := range changes {
		sources[i] = editor.resolve(cfg, &changes[i])
		changes[i].Source = reportSource(cfg, sources[i], editor.forms)
		if s := changes[i].shared; s != nil {
			for _, m := range s.members {
				m.Source = reportSource(cfg, editor.resolve(cfg, m), editor.forms)
			}
		}
	}
	return sources, nil
}

// timedDriftTerraform is a DriftTerraform whose every command runs for at
// most a timeout (see timedTerraform).
type timedDriftTerraform struct {
	*timedTerraform
	drift DriftTerraform
}

func driftWithTimeout(tf DriftTerraform, timeout time.Duration) DriftTerraform {
	if _, ok := tf.(*timedDriftTerraform); ok || timeout <= 0 {
		return tf
	}
	return &timedDriftTerraform{timedTerraform: &timedTerraform{Terraform: tf, timeout: timeout}, drift: tf}
}

func (t *timedDriftTerraform) StatePull(ctx context.Context, opts ...tfexec.StatePullOption) (string, error) {
	var state string
	err := t.bounded(ctx, func(ctx context.Context) error {
		var err error
		state, err = t.drift.StatePull(ctx, opts...)
		return err
	})
	return state, err
}

func (t *timedDriftTerraform) Apply(ctx context.Context, opts ...tfexec.ApplyOption) error {
	return t.bounded(ctx, func(ctx context.Context) error {
		return t.drift.Apply(ctx, opts...)
	})
}

func (t *timedDriftTerraform) ShowStateFile(ctx context.Context, statePath string, opts ...tfexec.ShowOption) (*tfjson.State, error) {
	var state *tfjson.State
	err := t.bounded(ctx, func(ctx context.Context) error {
		var err error
		state, err = t.drift.ShowStateFile(ctx, statePath, opts...)
		return err
	})
	return state, err
}
