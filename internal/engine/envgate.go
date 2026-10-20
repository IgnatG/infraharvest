// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// maxMergeRounds bounds how often MergeRoots merges a stack: each time a
// plan of the merged root changes resources its environment's root
// didn't, those become residue, and every environment plans again.
const maxMergeRounds = 3

// credentialsOverrideFileName is the override file that gives the merged
// root's provider the role an environment's root assumed, for the
// gate's plan of that environment only: the merged root sets no role of
// its own, as each environment runs with its own credentials.
const credentialsOverrideFileName = "infraharvest_credentials_override.tf"

// MergeOptions configure MergeRoots: what the merged root's gate checks,
// as Generate's does (see Options).
type MergeOptions struct {
	Omit, StateOnly map[string][]string
	Scanners        []Scanner
	Adapters        []adapters.Adapter
	CommandTimeout  time.Duration
}

// MergeResult is what MergeRoots did with the roots of a stack.
type MergeResult struct {
	// Reason says why the environments keep their roots of their own; ""
	// once they share the merged root.
	Reason string
	// Files are the merged root's files, by path in it with forward
	// slashes, sorted.
	Files []string
	// Shared counts the resources and module calls of the merged root that
	// more than one environment has.
	Shared int
	// Residue are the resources of one environment that the merged root
	// keeps as they are, not merged with the other environments', with
	// why.
	Residue []MergeResidue
	// Variables are the merged root's variables, by name.
	Variables []string
	// Environments are each environment's part of the merged root, in the
	// order of the roots.
	Environments []MergedEnvironment
}

// MergeResidue is a resource of one environment the merged root keeps as
// that environment's root had it: Address is its address there, and
// MergedAddress in the merged root.
type MergeResidue struct {
	Env, Address, MergedAddress, Reason string
}

// MergedEnvironment is one environment of a merged root: its gate, with
// its own credentials and var files, and the import IDs of its resources
// by their addresses in the merged root, which its imports.tfvars holds.
type MergedEnvironment struct {
	Env     string
	Gate    Gate
	Imports map[string]string
	// Addresses are where its root's resources are in the merged root, by
	// their addresses in the root.
	Addresses map[string]string
	// Unplanned says the merged root wasn't planned as the environment,
	// as the run had no Terraform runner for it (see MergeRoot.Terraform).
	Unplanned bool
}

// Passed reports whether every environment's gate passed.
func (r *MergeResult) Passed() bool {
	for _, e := range r.Environments {
		if !e.Gate.Passed() {
			return false
		}
	}
	return r.Reason == ""
}

// MergeRoots merges roots, the finished roots of one stack, each of
// another environment, into one root in dir that every environment plans
// and applies with its own var files (see writeMergedRoot), and checks it
// with the verification gate once per environment, with that
// environment's credentials and var files: every environment's plan must
// import what its root imported and change nothing else, as its root's
// did. Where a plan changes resources more than one environment shares,
// those become residue of each environment, and every environment plans
// again, up to maxMergeRounds merges. Where the roots can't be merged, or
// a plan still changes something then, the result's Reason says why, and
// dir is left as it was: the environments keep their roots. It returns
// an error only where it couldn't run.
func MergeRoots(ctx context.Context, dir string, roots []MergeRoot, opts MergeOptions) (*MergeResult, error) {
	if len(roots) < 2 {
		return nil, errors.New("merging takes the roots of two environments or more")
	}
	envRoots := make([]envRoot, len(roots))
	for i, r := range roots {
		root, reason, err := readEnvRoot(r, dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.Dir, err)
		}
		if reason != "" {
			return &MergeResult{Reason: reason}, nil
		}
		envRoots[i] = root
	}
	g := &mergeGate{dir: dir, roots: roots, opts: opts}
	result, err := g.merge(ctx, envRoots)
	if err != nil || result.Reason != "" {
		return result, errors.Join(err, g.clear())
	}
	return result, g.removeOverride()
}

// mergeGate writes and checks the merged root of a stack in dir.
type mergeGate struct {
	dir   string
	roots []MergeRoot
	opts  MergeOptions
	// written are the files it wrote in dir, by path in it with forward
	// slashes.
	written []string
	// newest is the root whose provider the merged root pins.
	newest int
}

// merge merges envRoots, read from g.roots, in rounds (see MergeRoots).
func (g *mergeGate) merge(ctx context.Context, envRoots []envRoot) (*MergeResult, error) {
	if err := os.MkdirAll(g.dir, 0o755); err != nil {
		return nil, err
	}
	// The provider release the merged root pins (see newestProvider).
	newest, err := newestProvider(envRoots)
	if err != nil {
		return &MergeResult{Reason: err.Error()}, nil
	}
	g.newest = newest
	schemas, err := g.schemas(ctx, envRoots)
	if err != nil {
		return nil, err
	}
	controls := &mergeControls{split: map[string]string{}, isolated: isolatedKeys(g.roots)}
	var failures []string
	for round := 0; round < maxMergeRounds; round++ {
		m := mergeStackWith(g.roots[0].Provider.Name, envRoots, schemas, controls)
		files, w, err := buildMergedRoot(m)
		if err != nil {
			return &MergeResult{Reason: err.Error()}, nil
		}
		if err := g.write(files); err != nil {
			return nil, err
		}
		envs, err := g.check(ctx, m, w)
		if err != nil {
			return nil, err
		}
		failed := false
		for _, e := range envs {
			if !e.Gate.Passed() {
				failed = true
			}
		}
		if !failed {
			return g.result(m, w, files, envs), nil
		}
		split, why := g.residueFor(m, envs)
		failures = why
		if len(split) == 0 {
			break
		}
		maps.Copy(controls.split, split)
	}
	return &MergeResult{Reason: "the merged root's gate failed: " + strings.Join(failures, "; ")}, nil
}

// isolatedKeys returns the resources roots keep apart (see
// MergeRoot.Isolated), by mergeKey.
func isolatedKeys(roots []MergeRoot) map[string]bool {
	keys := map[string]bool{}
	for e, r := range roots {
		for _, address := range r.Isolated {
			keys[mergeKey(e, address)] = true
		}
	}
	return keys
}

// schemas returns the provider's schemas, which type the merged root's
// variables, from an init of dir with the provider the roots require
// and the first root's lock file.
func (g *mergeGate) schemas(ctx context.Context, envRoots []envRoot) (*tfjson.ProviderSchemas, error) {
	if err := g.copyLock(); err != nil {
		return nil, err
	}
	required := RequiredVersion(MinimumVersion)
	if envRoots[g.newest].requiredVersion != "" {
		required = envRoots[g.newest].requiredVersion
	}
	if err := g.write(map[string][]byte{VersionsFileName: VersionsFile(required, envRoots[g.newest].provider)}); err != nil {
		return nil, err
	}
	first := slices.IndexFunc(g.roots, func(r MergeRoot) bool { return r.Terraform != nil })
	if first < 0 {
		return nil, errors.New("no environment of the stack to plan the merged root as")
	}
	tf, err := g.terraform(first)
	if err != nil {
		return nil, err
	}
	if err := initTerraform(ctx, tf, tfexec.Backend(false)); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	schemas, err := tf.ProvidersSchema(ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform providers schema: %w", err)
	}
	return schemas, nil
}

// copyLock copies the dependency lock file of the root whose provider
// the merged root pins into dir, so that the merged root plans with the
// provider release that root did.
func (g *mergeGate) copyLock() error {
	lock, err := os.ReadFile(filepath.Join(g.roots[g.newest].Dir, LockFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := fsutil.WriteFile(filepath.Join(g.dir, LockFileName), lock, 0o644); err != nil {
		return err
	}
	g.record(LockFileName)
	return nil
}

// terraform returns environment env's Terraform runner in dir.
func (g *mergeGate) terraform(env int) (Terraform, error) {
	if g.roots[env].Terraform == nil {
		return nil, fmt.Errorf("%s: no Terraform runner", g.roots[env].Env)
	}
	tf, err := g.roots[env].Terraform(g.dir)
	if err != nil {
		return nil, err
	}
	return withTimeout(tf, g.opts.CommandTimeout), nil
}

// record notes that g wrote name, a path in dir with forward slashes.
func (g *mergeGate) record(name string) {
	if !slices.Contains(g.written, name) {
		g.written = append(g.written, name)
	}
}

// write writes files into dir, by path in it with forward slashes, after
// removing the configuration and var files it wrote before; the lock
// file stays.
func (g *mergeGate) write(files map[string][]byte) error {
	for _, name := range slices.Clone(g.written) {
		if name == LockFileName {
			continue
		}
		if err := os.Remove(filepath.Join(g.dir, filepath.FromSlash(name))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		g.written = slices.DeleteFunc(g.written, func(n string) bool { return n == name })
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		path := filepath.Join(g.dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := fsutil.WriteFile(path, files[name], 0o644); err != nil {
			return err
		}
		g.record(name)
	}
	return nil
}

// clear removes what g wrote in dir, with Terraform's working directory,
// and dir itself if nothing else is left in it.
func (g *mergeGate) clear() error {
	var errs []error
	for _, name := range g.written {
		if err := os.Remove(filepath.Join(g.dir, filepath.FromSlash(name))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	g.written = nil
	errs = append(errs, g.removeOverride(), os.RemoveAll(filepath.Join(g.dir, ".terraform")))
	removeEmptyDirs(filepath.Join(g.dir, varsDirName))
	removeEmptyDirs(g.dir)
	return errors.Join(errs...)
}

// removeEmptyDirs removes dir and the directories under it that hold no
// file.
func removeEmptyDirs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			removeEmptyDirs(filepath.Join(dir, e.Name()))
		}
	}
	_ = os.Remove(dir) // fails, as it should, where something is left
}

// removeOverride removes the credentials override file, if any.
func (g *mergeGate) removeOverride() error {
	if err := os.Remove(filepath.Join(g.dir, credentialsOverrideFileName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// check runs the gate of the merged root m wrote with w once per
// environment: the whole gate with the first environment's credentials,
// and the plan, and the secrets check of the values it marks sensitive,
// with each other's.
func (g *mergeGate) check(ctx context.Context, m *stackMerge, w *rootWriter) ([]MergedEnvironment, error) {
	// The first environment the run can plan as runs the whole gate.
	first := slices.IndexFunc(g.roots, func(r MergeRoot) bool { return r.Terraform != nil })
	if first < 0 {
		return nil, errors.New("no environment of the stack to plan the merged root as")
	}
	tf, err := g.terraform(first)
	if err != nil {
		return nil, err
	}
	if err := initTerraform(ctx, tf, tfexec.Backend(false)); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	gateOpts := Options{Omit: g.opts.Omit, StateOnly: g.opts.StateOnly, Scanners: g.opts.Scanners, Adapters: g.opts.Adapters}
	envs := make([]MergedEnvironment, len(g.roots))
	for e := range g.roots {
		envs[e] = MergedEnvironment{Env: m.envs[e], Imports: g.imports(m, e), Addresses: g.addresses(m, e)}
		if g.roots[e].Terraform == nil {
			// Not imported this time, so without its credentials: it
			// keeps its part as it was, unplanned (see MergeRoot.Terraform).
			envs[e].Unplanned = true
			envs[e].Gate = Gate{{Name: CheckPlan, Passed: true, Skipped: true, Details: []string{"environment " + m.envs[e], "not planned: the import didn't have it; plan it with its credentials, with infraharvest verify --environment " + m.envs[e]}}}
			continue
		}
		envTF := tf
		if e != first {
			if envTF, err = g.terraform(e); err != nil {
				return nil, err
			}
		}
		plan := func() (Check, []string, error) { return g.plan(ctx, envTF, m, w, e) }
		if e == first {
			if envs[e].Gate, err = gateWith(ctx, envTF, g.dir, gateOpts, plan); err != nil {
				return nil, err
			}
			continue
		}
		planned, sensitive, err := plan()
		if err != nil {
			return nil, err
		}
		secrets, err := scanSecrets(g.dir, sensitive)
		if err != nil {
			return nil, err
		}
		envs[e].Gate = Gate{planned, secrets}
	}
	return envs, nil
}

// plan plans the merged root as environment env, with tf, its Terraform
// runner: with its var files, placeholder values for the secrets it sets,
// and the role its root's provider assumed (see
// credentialsOverrideFileName). The plan must import each of the
// environment's resources, and change nothing else but what the
// secrets' placeholders and the state-only arguments may (see
// planCheck).
func (g *mergeGate) plan(ctx context.Context, tf Terraform, m *stackMerge, w *rootWriter, env int) (Check, []string, error) {
	if err := g.writeOverride(env, w); err != nil {
		return Check{}, nil, err
	}
	defer func() { _ = g.removeOverride() }()
	abs, err := filepath.Abs(g.dir)
	if err != nil {
		return Check{}, nil, err
	}
	envDir := filepath.Join(abs, varsDirName, m.envs[env])
	vars := []tfexec.PlanOption{
		tfexec.VarFile(filepath.Join(abs, varsDirName, commonVarsFileName)),
		tfexec.VarFile(filepath.Join(envDir, envVarsFileName)),
		tfexec.VarFile(filepath.Join(envDir, importVarsFileName)),
	}
	secrets := w.sensitiveIn(env, g.roots[env].Secrets)
	p, _, diags, err := showPlanWithSummary(ctx, tf, append(vars, placeholders(secrets)...))
	if err != nil {
		return Check{}, nil, err
	}
	check := planCheck(p, diags, g.secretsIn(m, env), g.opts.StateOnly, len(g.imports(m, env)))
	check.Details = append([]string{"environment " + m.envs[env]}, check.Details...)
	return check, sensitiveValues(p), nil
}

// writeOverride writes the credentials override file for environment
// env: each of the merged root's provider blocks, the default one and
// those w writes an alias for, with the role env's root's provider
// assumed; none where it assumed none.
func (g *mergeGate) writeOverride(env int, w *rootWriter) error {
	role, ok := g.roots[env].Provider.Config["assume_role"]
	if !ok {
		return g.removeOverride()
	}
	f := hclwrite.NewEmptyFile()
	for i, alias := range append([]string{""}, w.aliases()...) {
		if i > 0 {
			f.Body().AppendNewline()
		}
		body := f.Body().AppendNewBlock("provider", []string{g.roots[env].Provider.Name}).Body()
		if alias != "" {
			body.SetAttributeValue("alias", cty.StringVal(alias))
		}
		if err := writeBody(body, map[string]interface{}{"assume_role": role}); err != nil {
			return err
		}
	}
	return fsutil.WriteFile(filepath.Join(g.dir, credentialsOverrideFileName), hclwrite.Format(f.Bytes()), 0o644)
}

// imports returns the import IDs of environment env's resources in the
// merged root, by address (see stackMerge.imports).
func (g *mergeGate) imports(m *stackMerge, env int) map[string]string {
	return m.imports(env)
}

// addresses returns, by its address in environment env's root, the
// address of each of its resources in the merged root.
func (g *mergeGate) addresses(m *stackMerge, env int) map[string]string {
	out := map[string]string{}
	for _, r := range m.resources {
		if member := r.members[env]; member != nil {
			out[member.block.address] = addressText(r.instance())
		}
	}
	return out
}

// secretsIn returns the secrets of environment env's root at their
// addresses in the merged root, whose arguments its plan may change: they
// have placeholder values.
func (g *mergeGate) secretsIn(m *stackMerge, env int) []Secret {
	var out []Secret
	for _, s := range g.roots[env].Secrets {
		member, ok := m.index[env][s.Address]
		if !ok || member.stack == nil {
			continue
		}
		out = append(out, Secret{Variable: s.Variable, Address: addressText(member.stack.instance()), Attribute: s.Attribute})
	}
	return out
}

// residueFor returns, of envs whose gates failed, the resources to make
// residue in the next round, by mergeKey, with why: those more than one
// environment shares whose plan changes them, in every environment that
// has them, and those of a for_each. It returns too why each failed gate
// failed.
func (g *mergeGate) residueFor(m *stackMerge, envs []MergedEnvironment) (map[string]string, []string) {
	byInstance := map[string]*stackResource{}
	for _, r := range m.resources {
		byInstance[addressText(r.instance())] = r
	}
	split := map[string]string{}
	var why []string
	for _, env := range envs {
		for _, c := range env.Gate {
			if c.Passed {
				continue
			}
			why = append(why, fmt.Sprintf("%s: %s: %s", env.Env, c.Name, strings.Join(c.Details, "; ")))
			if c.Name != CheckPlan {
				continue
			}
			for _, change := range c.Changes {
				r := moduleInstance(byInstance, change.Address)
				if r == nil || r.residue != "" || (r.coverage() < 2 && r.forEach == nil) {
					continue
				}
				reason := fmt.Sprintf("the merged root's plan in %s would %s it", env.Env, strings.Join(change.Actions, " and "))
				for e, member := range r.members {
					if member != nil {
						split[mergeKey(e, member.block.address)] = reason
					}
				}
			}
		}
	}
	// A failure other than a plan's changes, such as a standard the files
	// break, residue doesn't mend.
	for _, env := range envs {
		for _, c := range env.Gate {
			if !c.Passed && (c.Name != CheckPlan || len(c.Changes) == 0) {
				return nil, why
			}
		}
	}
	return split, why
}

// result is the result of m's merged root, which w wrote as files, whose
// environments envs passed their gates.
func (g *mergeGate) result(m *stackMerge, w *rootWriter, files map[string][]byte, envs []MergedEnvironment) *MergeResult {
	result := &MergeResult{Files: slices.Sorted(maps.Keys(files)), Environments: envs}
	for _, r := range m.resources {
		switch {
		case r.residue != "":
			for e, member := range r.members {
				if member != nil {
					result.Residue = append(result.Residue, MergeResidue{Env: m.envs[e], Address: member.block.address, MergedAddress: addressText(r.instance()), Reason: r.residue})
				}
			}
		case r.coverage() > 1:
			result.Shared++
		}
	}
	for _, v := range w.variables() {
		result.Variables = append(result.Variables, v.name)
	}
	for _, b := range w.copied {
		result.Variables = append(result.Variables, b.Labels()[0])
	}
	result.Variables = append(result.Variables, importIDsVariableName)
	sort.Strings(result.Variables)
	sort.Slice(result.Residue, func(i, j int) bool {
		if result.Residue[i].Env != result.Residue[j].Env {
			return result.Residue[i].Env < result.Residue[j].Env
		}
		return result.Residue[i].Address < result.Residue[j].Address
	})
	return result
}

// sensitiveIn returns the sensitive variables of the merged root that
// environment env sets, as secrets whose placeholder values its plans
// take (see placeholders): those of the model where the environment has
// what they are for, and those the roots declare, as rootSecrets, the
// secrets of env's root, name them.
func (w *rootWriter) sensitiveIn(env int, rootSecrets []Secret) []Secret {
	var out []Secret
	for _, v := range w.variables() {
		if _, ok := v.values[env]; ok && v.sensitive {
			out = append(out, Secret{Variable: v.name, ty: v.ty})
		}
	}
	for _, b := range w.copied {
		name := b.Labels()[0]
		attr := b.Body().GetAttribute("sensitive")
		if attr == nil {
			continue
		}
		if s, ok := literalValue(attr.Expr().BuildTokens(nil)); !ok || s.Type() != cty.Bool || !s.True() {
			continue
		}
		if !slices.ContainsFunc(rootSecrets, func(s Secret) bool { return s.Variable == name }) {
			continue
		}
		ty := cty.String
		if attr := b.Body().GetAttribute("type"); attr != nil {
			if expr, diags := hclsyntax.ParseExpression(attr.Expr().BuildTokens(nil).Bytes(), "", hcl.InitialPos); !diags.HasErrors() {
				if t, diags := typeexpr.TypeConstraint(expr); !diags.HasErrors() {
					ty = t
				}
			}
		}
		out = append(out, Secret{Variable: name, ty: ty})
	}
	return out
}
