// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/managed"
	"github.com/IgnatG/infraharvest/internal/report"
)

// A root environments share keeps what its merge did under the output's
// checkpoints (see mergeCheckpoint), which the output's .gitignore leaves
// out: a fresh checkout of the output, such as the GitHub Action's, has
// the merged root without them. Such a root is told from its own files
// (see engine.MergedEnvironments), and an import with --incremental,
// --resume or --regenerate merges its stack again from the environments'
// roots it generates afresh, as if a checkpoint made from those files
// recorded the merge (see adoptedCheckpoint).

// mergedState is the environment env of the root environments share in
// stack.
type mergedState struct {
	stack, env string
}

// mergedRootDirs returns the directories under out that hold a root
// environments share (see engine.MergedEnvironments), sorted: outside
// the checkpoints, Terraform's working directories and local workspaces.
func mergedRootDirs(out string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			return nil
		}
		switch d.Name() {
		case CheckpointDir, ".terraform", "terraform.tfstate.d", ".git", "vars":
			if path != out {
				return filepath.SkipDir
			}
		}
		envs, err := engine.MergedEnvironments(path)
		if err != nil || len(envs) == 0 {
			return err
		}
		if names, err := tfFiles(path); err != nil || len(names) == 0 {
			return err
		}
		dirs = append(dirs, path)
		return nil
	})
	sort.Strings(dirs)
	return dirs, err
}

// mergedOwnStates returns, by state location (see stateLocation), the
// environment of a root environments share under out whose own state
// each is (see engine.MergedStateLocations): what such a state has is
// the merged root's, not another state's. With unrecorded, only the
// roots no merge checkpoint records count (see adoptedCheckpoint).
func mergedOwnStates(out string, unrecorded bool) (map[string]mergedState, error) {
	dirs, err := mergedRootDirs(out)
	if err != nil {
		return nil, err
	}
	own := map[string]mergedState{}
	for _, dir := range dirs {
		if unrecorded {
			if c, _ := readMergeCheckpoint(out, dir); c != nil {
				continue
			}
		}
		envs, err := engine.MergedEnvironments(dir)
		if err != nil {
			return nil, err
		}
		for _, env := range envs {
			locations, err := engine.MergedStateLocations(dir, env)
			if err != nil {
				return nil, err
			}
			for _, location := range locations {
				own[stateLocation(location)] = mergedState{stack: dir, env: env}
			}
		}
	}
	return own, nil
}

// loadedState returns every managed resource of the state --managed-state
// named, as excludeManaged loaded it; none without it.
func (s *shared) loadedState() managed.Resources {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := managed.Resources{}
	for _, key := range slices.Sorted(maps.Keys(s.managed)) {
		maps.Copy(all, s.managed[key])
	}
	return all
}

// adoptedCheckpoint returns, for the root environments share in stack
// that no merge checkpoint records, a checkpoint made from its own files
// for members, the roots of its stack the import generated: its
// environments, from vars/<env>/; the files a merge writes (see
// engine.MergedRootFiles); and each environment's import IDs by address,
// from vars/<env>/imports.tfvars, or, once that is deleted after the
// first apply, from the environment's own state, which --managed-state
// read (see stateImports). It returns nil if stack holds no such root,
// and why the stack can't merge again, if it can't: the import lacks one
// of its environments, whose root no checkpoint kept, or nothing says
// what the merged root has of one.
func (r *Run) adoptedCheckpoint(stack string, members []mergeMember) (*mergeCheckpoint, string, error) {
	out := r.options.Out
	envs, err := engine.MergedEnvironments(stack)
	if err != nil || len(envs) == 0 {
		return nil, "", err
	}
	if names, err := tfFiles(stack); err != nil || len(names) == 0 {
		return nil, "", err
	}
	files, err := engine.MergedRootFiles(stack)
	if err != nil {
		return nil, "", err
	}
	c := &mergeCheckpoint{
		Merge:   report.Merge{Path: relativePath(out, stack), Merged: true, Environments: envs},
		Files:   files,
		Imports: map[string]map[string]string{},
	}
	byEnv := map[string]mergeMember{}
	for _, m := range members {
		if _, ok := byEnv[m.env]; !ok {
			byEnv[m.env] = m
		}
	}
	var missing, unknown []string
	for _, env := range envs {
		m, ok := byEnv[env]
		if !ok {
			missing = append(missing, env)
			continue
		}
		c.Members = append(c.Members, mergeCheckpointMember{Env: env, Path: relativePath(out, m.dir)})
		ids, err := engine.MergedImportIDs(stack, env)
		if err != nil {
			return nil, "", err
		}
		if ids == nil {
			if ids, err = r.stateImports(stack, env, m.result); err != nil {
				return nil, "", err
			}
		}
		if len(ids) == 0 {
			unknown = append(unknown, env)
			continue
		}
		c.Imports[env] = ids
	}
	unrecorded := fmt.Sprintf("no checkpoint under %s records its merge, as in a fresh checkout of the output", CheckpointDir)
	switch {
	case len(missing) > 0:
		return c, fmt.Sprintf("%s, and the import doesn't have %s, which the merged root has too: it stays as it was; import every environment it has (%s) to add to it", unrecorded, strings.Join(missing, ", "), strings.Join(envs, ", ")), nil
	case len(unknown) > 0:
		return c, fmt.Sprintf("%s, and neither vars/<env>/imports.tfvars nor --managed-state says what the merged root has of %s: it stays as it was; pass --managed-state with each environment's state, such as backend", unrecorded, strings.Join(unknown, ", ")), nil
	}
	return c, "", nil
}

// stateImports returns the import IDs, by address in the root
// environments share in stack, of what result, the root of environment
// env the import generated, has that env's own state has (see
// engine.MergedStateLocations), as --managed-state read it; none without
// it.
func (r *Run) stateImports(stack, env string, result *engine.Result) (map[string]string, error) {
	if result == nil || len(r.options.ManagedState) == 0 {
		return nil, nil
	}
	locations, err := engine.MergedStateLocations(stack, env)
	if err != nil {
		return nil, err
	}
	own := map[string]bool{}
	for _, l := range locations {
		own[stateLocation(l)] = true
	}
	state := r.shared.loadedState()
	ids := map[string]string{}
	for _, res := range resultResources(result) {
		found, ok := state.Find(res.Type, res.ID)
		if ok && found.Address != "" && own[stateLocation(found.Where)] {
			ids[found.Address] = res.ID
		}
	}
	return ids, nil
}

// resultResources returns what result, of a root, imports, by type and
// import ID, with the address of the block of the root each is in: a
// resource's, or a module call's.
func resultResources(result *engine.Result) []engine.Managed {
	var resources []engine.Managed
	if len(result.Addresses) > 0 {
		for _, a := range result.Addresses {
			resources = append(resources, engine.Managed{Type: a.Type, ID: a.ID, Address: blockAddress(a.Address)})
		}
		return resources
	}
	for _, imp := range result.Imported {
		resources = append(resources, engine.Managed{Type: imp.Type, ID: imp.ID, Address: imp.Type + "." + imp.Name})
	}
	return resources
}

// blockAddress returns the address of the block of a root an instance
// address is of: module.logs for module.logs.aws_s3_bucket.this[0], and
// aws_sqs_queue.orders for aws_sqs_queue.orders["a"].
func blockAddress(address string) string {
	if rest, ok := strings.CutPrefix(address, "module."); ok {
		name, _, _ := strings.Cut(rest, ".")
		name, _, _ = strings.Cut(name, "[")
		return "module." + name
	}
	address, _, _ = strings.Cut(address, "[")
	return address
}

// newSinceMerge returns the addresses of the blocks of result, the root
// of an environment the import generated afresh, none of whose resources
// ids, the import IDs the merged root has of the environment by address,
// has: what is new since the stack merged, which the merge keeps apart
// (see mergeMember.isolated), as an incremental import does.
func newSinceMerge(result *engine.Result, ids map[string]string) []string {
	had := map[engine.External]bool{}
	for address, id := range ids {
		had[engine.External{Type: engine.AddressType(address), ID: id}] = true
	}
	held := map[string]bool{}
	seen := map[string]bool{}
	for _, res := range resultResources(result) {
		seen[res.Address] = true
		if had[engine.External{Type: res.Type, ID: res.ID}] {
			held[res.Address] = true
		}
	}
	var isolated []string
	for address := range seen {
		if !held[address] {
			isolated = append(isolated, address)
		}
	}
	sort.Strings(isolated)
	return isolated
}

// leftOutOfMerge returns, of before, the import IDs of each environment's
// resources in a merged root by address, those now, merged again, lacks,
// as "<env>: <address>", sorted.
func leftOutOfMerge(before, now map[string]map[string]string) []string {
	var gone []string
	for env, ids := range before {
		has := map[string]bool{}
		for _, id := range now[env] {
			has[id] = true
		}
		for address, id := range ids {
			if !has[id] {
				gone = append(gone, env+": "+address)
			}
		}
	}
	sort.Strings(gone)
	return gone
}

// maxKeptApart is how many times a merged root told from its files
// merges again with more of what would get another address kept apart
// (see movedApart).
const maxKeptApart = 3

// movedApart returns, by address, the blocks of m, a root of its
// environment, not yet kept apart, that hold a resource now, the import
// IDs of the environment's resources by address in the stack merged
// again, has at another address than before, those of the merged root
// told from its files, had it, each with the label it had there: the
// earlier merge kept them apart, as what an incremental import added,
// so they are kept apart again, with the labels it gave them. A resource
// at an address of shared, which other environments have too, is left
// out: the earlier merge matched it with theirs, and kept apart it would
// get another address all the same.
func movedApart(m mergeMember, before, now map[string]string, shared map[string]bool) map[string]string {
	if m.result == nil || len(before) == 0 {
		return nil
	}
	at := map[string]string{}
	for address, id := range now {
		at[id] = address
	}
	moved := map[engine.External]string{}
	for address, id := range before {
		if to, ok := at[id]; ok && to != address && !shared[address] {
			moved[engine.External{Type: engine.AddressType(address), ID: id}] = address
		}
	}
	more := map[string]string{}
	for _, res := range resultResources(m.result) {
		had, ok := moved[engine.External{Type: res.Type, ID: res.ID}]
		if !ok || slices.Contains(m.isolated, res.Address) {
			continue
		}
		if _, seen := more[res.Address]; !seen {
			more[res.Address] = mergedLabel(had)
		}
	}
	return more
}

// mergedLabel returns the label of the block an address in a merged root
// is of: shop_audit for aws_sqs_queue.shop_audit[0], and logs for
// module.logs[0].aws_s3_bucket.this.
func mergedLabel(address string) string {
	block := blockAddress(address)
	if name, ok := strings.CutPrefix(block, "module."); ok {
		return name
	}
	_, label, _ := strings.Cut(block, ".")
	return label
}

// sharedAddresses returns the addresses of imports, the import IDs of
// each environment's resources in a merged root by address, that more
// than one environment has: what the merge matched across them.
func sharedAddresses(imports map[string]map[string]string) map[string]bool {
	count := map[string]int{}
	for _, ids := range imports {
		for address := range ids {
			count[address]++
		}
	}
	shared := map[string]bool{}
	for address, n := range count {
		if n > 1 {
			shared[address] = true
		}
	}
	return shared
}
