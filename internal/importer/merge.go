// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/version"
)

// stackLayout returns the layout of the root the environments of a stack
// merge into, from pattern, the layout of their roots: the provider and
// region, as {out}/{provider}/{region}/ for the layout of roots per
// environment, else pattern without the segments that hold {env},
// {account}, {subscription} or {project}, which tell the environments'
// roots apart.
func stackLayout(pattern string) string {
	if pattern == envLayout {
		return "{out}/{provider}/{region}/"
	}
	var kept []string
	for _, segment := range strings.Split(pattern, "/") {
		if !slices.ContainsFunc([]string{"{env}", "{account}", "{subscription}", "{project}"}, func(p string) bool { return strings.Contains(segment, p) }) {
			kept = append(kept, segment)
		}
	}
	return strings.Join(kept, "/")
}

// memberPlace is where a root of an environment goes when its stack
// merges: its environment, and the merged root's directory.
type memberPlace struct {
	env, stack string
}

// mergePlaces returns, by root, where each root the call generates of
// one of the configuration file's environments goes when its stack merges
// (see stackLayout): none for the roots kept where earlier imports
// generated them (kept), and for what no environment's tags place.
func mergePlaces(resourcesByService map[string][]harvest.Resource, envOf func(harvest.Resource) string, dirOf, stackOf func(string, harvest.Resource) string, kept *keptRoots, byDir map[string][]engine.Import) map[string]memberPlace {
	places := map[string]memberPlace{}
	for service, resources := range resourcesByService {
		for _, r := range resources {
			env := envOf(r)
			if env == "" || env == sharedEnv {
				continue
			}
			dir := dirOf(service, r)
			if _, ok := byDir[dir]; !ok {
				continue
			}
			if kept != nil {
				if _, isKept := kept.kept[dir]; isKept {
					continue
				}
			}
			places[dir] = memberPlace{env: env, stack: stackOf(service, r)}
		}
	}
	return places
}

// mergeMember is a root an import generated, or kept from an earlier one
// with --resume, of one environment of a stack, which the merge phase
// merges with the other environments' (see MergeEnvironments).
type mergeMember struct {
	dir   string // the root's directory
	stack string // the merged root's
	env   string
	// tokens are the words that stand for the environment in names.
	tokens []string
	// scope is where its resources were listed (see discoveryScope),
	// account its account, subscription or project, region its region
	// and group its Azure resource group, if any.
	scope, account, region, group string
	// provider plans the merged root as the environment, with its
	// credentials (see terraformEnv), and engineProvider is the root's
	// provider configuration and version constraint.
	provider       harvest.Provider
	engineProvider engine.Provider
	fingerprint    string
	result         *engine.Result
	// opts are the root's engine options, of which the merged root's gate
	// takes what it checks with.
	opts               engine.Options
	execPath, cacheDir string
	// premerged says its files are set aside (see premergeDir), as its
	// stack merged before, and isolated are the addresses of what an
	// incremental import added to it since (see engine.MergeRoot.Isolated).
	premerged bool
	isolated  []string
	// restored says the import didn't have the member's environment: it
	// merges again from its set-aside root, unplanned, as the import has
	// none of its credentials (see restoredMembers).
	restored bool
}

// recordMember records that the call generated the root in dir, from
// fingerprint fp, with result and opts, for the merge phase, where the
// root is of one of the configuration file's environments.
func (c *listedCall) recordMember(dir, fp string, result *engine.Result, opts engine.Options) {
	c.recordMemberWith(dir, fp, result, opts, nil)
}

// recordMemberWith is recordMember, with isolated the addresses of what
// an incremental import added to the root, which its stack merged
// before.
func (c *listedCall) recordMemberWith(dir, fp string, result *engine.Result, opts engine.Options, isolated []string) {
	place, ok := c.places[dir]
	if !ok || result == nil {
		return
	}
	set, err := premerged(c.options.Out, dir)
	if err != nil {
		log.Printf("%s: %v; its stack isn't merged", dir, err)
		return
	}
	region := c.region
	if r, ok := c.root.provider.Config["region"].(string); ok && r != "" && c.provider.Name() == "aws" {
		region = r
	}
	c.run.members = append(c.run.members, mergeMember{
		dir:            dir,
		stack:          place.stack,
		env:            place.env,
		tokens:         c.run.environments.EnvironmentTokens(place.env),
		scope:          c.scope,
		account:        scopeAccount(c.scope),
		region:         region,
		group:          c.options.ResourceGroup,
		provider:       c.provider,
		engineProvider: c.root.provider,
		fingerprint:    fp,
		result:         result,
		opts:           opts,
		execPath:       c.execPath,
		cacheDir:       c.cacheDir,
		premerged:      set,
		isolated:       isolated,
	})
}

// MergeEnvironments merges the roots of each stack of the run's
// environments that every scope of the command generated (see
// mergeMember): the stack's roots of two environments or more become one
// root they share, in the directory stackLayout names, checked by its gate
// once per environment with that environment's credentials (see
// engine.MergeRoots). A stack whose roots don't merge so that every
// environment's plan stays the same keeps them, and the report says why.
// The roots of a merged stack are set aside under the output's
// checkpoints (see premergeDir). It returns an error only where the run
// can't go on, such as when it is interrupted.
func MergeEnvironments(ctx context.Context) error {
	run := activeRun
	if run == nil || len(run.members) == 0 || !run.options.MergeEnvironments {
		return nil
	}
	byStack := map[string][]mergeMember{}
	for _, m := range run.members {
		byStack[m.stack] = append(byStack[m.stack], m)
	}
	for _, stack := range slices.Sorted(maps.Keys(byStack)) {
		members := byStack[stack]
		sort.Slice(members, func(i, j int) bool {
			if members[i].env != members[j].env {
				return members[i].env < members[j].env
			}
			return members[i].dir < members[j].dir
		})
		if err := run.mergeStack(ctx, stack, members); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("%s: %v", stack, err)
			run.recordFailure(run.options, fmt.Errorf("merging %s: %w", relativePath(run.options.Out, stack), err))
		}
	}
	return nil
}

// mergeRefusal returns why members, the roots of the stack whose merged
// root is stack, aren't merged, if they aren't: an environment has
// several of them, the merged root would be one of them or hold one, or
// its directory holds a root no merge wrote, or one of them has local
// state, which merging would leave where no root is.
func mergeRefusal(out, stack string, members []mergeMember) string {
	byEnv := map[string][]string{}
	for _, m := range members {
		byEnv[m.env] = append(byEnv[m.env], relativePath(out, m.dir))
		if inside(stack, m.dir) {
			return fmt.Sprintf("the layout puts %s in the merged root's directory, %s", relativePath(out, m.dir), relativePath(out, stack))
		}
	}
	for _, env := range slices.Sorted(maps.Keys(byEnv)) {
		if len(byEnv[env]) > 1 {
			return fmt.Sprintf("%s has %d roots in the stack (%s): merging takes one root of each environment", env, len(byEnv[env]), strings.Join(byEnv[env], ", "))
		}
	}
	for _, m := range members {
		if reason := localStateRefusal(out, m.dir); reason != "" {
			return reason
		}
	}
	names, err := tfFiles(stack)
	if err == nil && len(names) > 0 {
		if c, _ := readMergeCheckpoint(out, stack); c == nil {
			return fmt.Sprintf("%s already holds %s, which no merge by infraharvest wrote", relativePath(out, stack), strings.Join(names, ", "))
		}
	}
	return ""
}

// localStateRefusal returns why the root in dir, under out, isn't merged,
// if it holds local state, such as the terraform.tfstate of a root
// applied without a backend (see engine.LocalStateFile): merging sets the
// root aside, and its state stays where it is, with no root to apply it.
func localStateRefusal(out, dir string) string {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		return fmt.Sprintf("%s can't be read: %v", relativePath(out, dir), err)
	}
	var state []string
	for _, e := range entries {
		if engine.LocalStateFile(e.Name()) {
			state = append(state, e.Name())
		}
	}
	if len(state) == 0 {
		return ""
	}
	return fmt.Sprintf("%s has local state (%s), which merging would leave where no root is: migrate it to a backend first, with a backend block and terraform init -migrate-state, and move the local files out of the root once the backend has the state", relativePath(out, dir), strings.Join(state, ", "))
}

// inside reports whether dir is parent or a directory under it.
func inside(parent, dir string) bool {
	rel, err := filepath.Rel(parent, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// fillMerge records result, of engine.MergeRoots, in merge.
func fillMerge(merge *report.Merge, result *engine.MergeResult) {
	merge.Merged = result.Reason == ""
	merge.Reason = result.Reason
	merge.Shared = result.Shared
	merge.Variables = result.Variables
	for _, res := range result.Residue {
		merge.Residue = append(merge.Residue, report.MergeResidue{Env: res.Env, Address: res.Address, MergedAddress: res.MergedAddress, Reason: res.Reason})
	}
	for _, env := range result.Environments {
		if env.Unplanned {
			merge.Unplanned = append(merge.Unplanned, env.Env)
		}
		for _, c := range env.Gate {
			merge.Checks = append(merge.Checks, report.Check{Name: c.Name, Passed: c.Passed, Skipped: c.Skipped, Details: append([]string{env.Env}, c.Details...)})
		}
	}
}

// recordMerge records merge, the outcome of the stack of members, in the
// report: each member's directory names the merged root, once merged.
func (r *Run) recordMerge(merge report.Merge, members []mergeMember) {
	r.report.Merges = append(r.report.Merges, merge)
	if !merge.Merged {
		return
	}
	paths := map[string]bool{}
	for _, m := range members {
		paths[relativePath(r.options.Out, m.dir)] = true
	}
	for i, d := range r.report.Directories {
		if paths[d.Path] {
			r.report.Directories[i].MergedInto = merge.Path
		}
	}
}

// premergeDirName holds, under the output's checkpoints, the roots of
// each merged stack as they were generated (see premergeDir).
const premergeDirName = "premerge"

// premergeDir is where the root in dir, under out, is kept once its stack
// merges: at its path relative to out under the checkpoints, so that a
// later run can merge the stack again from it, without generating it
// again.
func premergeDir(out, dir string) string {
	return filepath.Join(out, CheckpointDir, premergeDirName, filepath.FromSlash(relativePath(out, dir)))
}

// copyRoot copies the files directly in the root at from, but Terraform's
// working directory and local state (see engine.LocalStateFile), into
// to, which it empties first: the state stays where Terraform keeps it.
func copyRoot(from, to string) error {
	if err := os.RemoveAll(to); err != nil {
		return err
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || engine.LocalStateFile(e.Name()) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(from, e.Name()))
		if err != nil {
			return err
		}
		if err := fsutil.WriteFile(filepath.Join(to, e.Name()), content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// removeRoot removes the root in dir, which its stack's merged root takes
// the place of: the files directly in it, but its local state, which
// stays where Terraform keeps it, and Terraform's working directory, then
// dir and the directories above it, up to out, that hold nothing else.
func removeRoot(out, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		switch {
		case e.Type().IsRegular() && !engine.LocalStateFile(e.Name()):
			if err := os.Remove(path); err != nil {
				return err
			}
		case e.IsDir() && e.Name() == ".terraform":
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		}
	}
	for d := dir; inside(out, d) && filepath.Clean(d) != filepath.Clean(out); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			break // not empty
		}
	}
	return nil
}

// mergeCheckpoint records what the merge phase did with a stack: what it
// merged, its fingerprint (see mergeFingerprint), and the outcome.
type mergeCheckpoint struct {
	Fingerprint string                  `json:"fingerprint"`
	Members     []mergeCheckpointMember `json:"members"`
	Merge       report.Merge            `json:"merge"`
	Files       []string                `json:"files,omitempty"`
	// Imports are, by environment, the import IDs of its resources by
	// their addresses in the merged root.
	Imports map[string]map[string]string `json:"imports,omitempty"`
}

// mergeCheckpointMember is one root a stack's merge took.
type mergeCheckpointMember struct {
	Env         string `json:"env"`
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
	// Scope, Account, Region and Group are the member's (see
	// mergeMember), for an import that doesn't have its environment to
	// merge the stack again from its set-aside root (see restoredMembers).
	Scope   string `json:"scope,omitempty"`
	Account string `json:"account,omitempty"`
	Region  string `json:"region,omitempty"`
	Group   string `json:"group,omitempty"`
}

// checkpointMembers returns members as a merge checkpoint records them.
func checkpointMembers(out string, members []mergeMember) []mergeCheckpointMember {
	recorded := make([]mergeCheckpointMember, len(members))
	for i, m := range members {
		recorded[i] = mergeCheckpointMember{Env: m.env, Path: relativePath(out, m.dir), Fingerprint: m.fingerprint, Scope: m.scope, Account: m.account, Region: m.region, Group: m.group}
	}
	return recorded
}

// mergeFingerprint identifies what a stack's merge merges: infraharvest's
// version, and each member's environment, tokens, path and fingerprint.
func mergeFingerprint(out string, members []mergeMember) (string, error) {
	type member struct {
		Env, Path, Fingerprint string
		Tokens                 []string
	}
	recorded := make([]member, len(members))
	for i, m := range members {
		recorded[i] = member{Env: m.env, Path: relativePath(out, m.dir), Fingerprint: m.fingerprint, Tokens: m.tokens}
	}
	content, err := json.Marshal(struct {
		Version string
		Members []member
	}{version.Version, recorded})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

// mergeCheckpointPath is where the checkpoint of the merged root in stack
// is.
func mergeCheckpointPath(out, stack string) string {
	return filepath.Join(out, CheckpointDir, "merge", filepath.FromSlash(relativePath(out, stack))+".json")
}

// writeMergeCheckpoint writes c as the checkpoint of the merged root in
// stack.
func writeMergeCheckpoint(out, stack string, c mergeCheckpoint) error {
	path := mergeCheckpointPath(out, stack)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFile(path, append(content, '\n'), 0o644)
}

// readMergeCheckpoint returns the checkpoint of the merged root in stack;
// nil if there is none.
func readMergeCheckpoint(out, stack string) (*mergeCheckpoint, error) {
	path := mergeCheckpointPath(out, stack)
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c mergeCheckpoint
	if err := json.Unmarshal(content, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// scopeRegion returns the region of a scope as discoveryScope names it
// (aws/123456789012/eu-west-2), or "" if it names none.
func scopeRegion(scope string) string {
	parts := strings.SplitN(scope, "/", 3)
	if len(parts) != 3 {
		return ""
	}
	return parts[2]
}
