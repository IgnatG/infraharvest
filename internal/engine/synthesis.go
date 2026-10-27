// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
)

// ModuleCall is a module call Generate made, or a cluster of resources it
// tried to move into a module and didn't.
type ModuleCall struct {
	// Name is the call's name; Source and Version are the module's.
	Name    string
	Source  string
	Version string
	// Resources are the addresses the resources had in the root.
	Resources []string
	// Declined, if set, is why the resources stayed where they were.
	Declined string
}

// trial is a cluster an adapter mapped, written as a module call to try.
type trial struct {
	adapter *adapters.Adapter
	name    string
	cluster adapters.Cluster
	call    *adapters.Call
	// literals are the values of the members' attributes the module has no
	// output for, which references to them become, by reference, as
	// aws_sqs_queue.jobs.name: a module call matters more than they do.
	literals map[string]string
	// rejected is why the plan didn't accept the call.
	rejected string
	// provider, if set, is the provider configuration all the members use,
	// such as aws.without_default_tags, which the call passes the module.
	provider hclwrite.Tokens
}

func (t *trial) members() []string {
	addresses := []string{t.cluster.Anchor.Address()}
	for _, m := range t.cluster.Members {
		addresses = append(addresses, m.Address())
	}
	return addresses
}

// moduleAddress is where a member lands, as Terraform names it in plans.
func (t *trial) moduleAddress(member string) string {
	return "module." + t.name + "." + t.call.Addresses[member]
}

// synthesize moves clusters of resources into calls of curated modules:
// for each anchor resource, the first adapter that maps its cluster. It
// writes every call, installs the modules and plans, and takes back the
// calls whose resources would plan differently than baseline, which has
// each resource's planned change (see changeSignature), until the plan
// matches; when the plan fails in a way it can't pin on a call, it plans
// half of the calls at a time to find the calls that fail. A module may set the arguments opts.StateOnly names, which
// import can't. Calls are named other than the modules r's policy takes.
// values are the imported values, by address: references to what a call
// takes and has no output for become the values again (see
// trial.literals). It returns the clusters it didn't move, with why, and
// the plan's change summary with the calls it kept, and the lengths of
// their list outputs (see outputLists).
func synthesize(ctx context.Context, tf Terraform, dir string, r *run, values map[string]map[string]any, baseline changeSummary, changes map[string]string, vars []tfexec.PlanOption) ([]ModuleCall, changeSummary, map[string]int, error) {
	opts := r.opts
	backup, err := backupFiles(dir, GeneratedFileName, ImportsFileName, ProvidersFileName, VersionsFileName)
	if err != nil {
		return nil, baseline, nil, err
	}
	trials, declined, err := mapClusters(dir, r.policy.adapters, r.policy.taken, values)
	if err != nil || len(trials) == 0 {
		return declined, baseline, nil, err
	}
	reject := func(reason string, ts ...*trial) {
		for _, t := range ts {
			if t.rejected == "" {
				t.rejected = reason
			}
		}
	}
	if err := writeCalls(dir, trials, opts.ModuleProviders); err != nil {
		return nil, baseline, nil, errors.Join(err, backup.restore())
	}
	if err := initTerraform(ctx, tf); err != nil {
		// Typically: the registry can't be reached.
		reject(fmt.Sprintf("terraform init: %v", err), trials...)
	}
	// written are the calls the root has now. kept are the calls a plan
	// accepted together, and keptSummary is that plan's summary.
	written := trials
	var kept []*trial
	var keptSummary changeSummary
	write := func(ts []*trial) error {
		if slices.Equal(ts, written) {
			return nil
		}
		if err := backup.restore(); err != nil {
			return err
		}
		written = ts
		return writeCalls(dir, ts, opts.ModuleProviders)
	}
	// pending are the groups of calls still to plan with kept, in turn. A
	// group whose failure the plan can't pin on one of its calls is halved
	// and its halves are planned in turn, the other held back meanwhile,
	// until only the calls that fail are declined: one plan when the calls
	// plan clean, and a plan or two for each halving for each call that
	// doesn't. Every plan keeps a group, declines a call or halves a group,
	// so the loop ends.
	pending := []*callGroup{{trials: activeTrials(trials)}}
	var passed *callGroup
	for len(pending) > 0 {
		g := pending[0]
		pending = pending[1:]
		g.trials = activeTrials(g.trials)
		if len(g.trials) == 0 {
			continue
		}
		// A group known to fail with what is kept (see callGroup) is
		// halved, or declined, without planning it again.
		reason := g.fails
		if g.failsAfter == nil || g.failsAfter != passed {
			tried := inOrder(trials, kept, g.trials)
			if err := write(tried); err != nil {
				return nil, baseline, nil, errors.Join(err, backup.restore())
			}
			p, summary, diags, err := showPlanWithSummary(ctx, tf, vars)
			if err != nil {
				return nil, baseline, nil, errors.Join(err, backup.restore())
			}
			ok, why := judge(tried, p, summary, diags, baseline, changes, opts.StateOnly)
			switch {
			case ok:
				kept, keptSummary, passed = tried, *summary, g
				continue
			case len(activeTrials(tried)) < len(tried):
				// The plan took calls back: what is left plans again, with
				// the group if the plan took back one of kept.
				retry := &callGroup{trials: g.trials}
				if len(activeTrials(kept)) < len(kept) {
					retry.trials, kept = tried, nil
				}
				pending = append([]*callGroup{retry}, pending...)
				continue
			case why == "":
				// Nothing to take back (the plan reported neither errors
				// nor a result): planning again would show the same.
				reject("the plan reported no result", g.trials...)
				for _, held := range pending {
					reject("the plan reported no result", held.trials...)
				}
				pending = nil
				continue
			}
			reason = why
		}
		if len(g.trials) == 1 {
			reject(reason, g.trials...)
			continue
		}
		first := &callGroup{trials: g.trials[:len(g.trials)/2]}
		second := &callGroup{trials: g.trials[len(g.trials)/2:], fails: reason, failsAfter: first}
		pending = append([]*callGroup{first, second}, pending...)
	}
	if len(kept) == 0 {
		if err := backup.restore(); err != nil {
			return nil, baseline, nil, err
		}
		return append(declined, rejections(trials)...), baseline, nil, nil
	}
	// The last plan may have tried calls declined since: the root has the
	// calls the plan of kept accepted, which planning again would show.
	if err := write(kept); err != nil {
		return nil, baseline, nil, errors.Join(err, backup.restore())
	}
	if err := pinModuleProviders(dir, opts.ModuleProviders); err != nil {
		return nil, baseline, nil, err
	}
	return append(declined, rejections(trials)...), keptSummary, outputLists(kept), nil
}

// callGroup is calls synthesize plans together with the calls it kept.
type callGroup struct {
	trials []*trial
	// fails, if set, is why the plan failed with this group and the group
	// failsAfter together, the halves of a group. When the next plan keeps
	// failsAfter whole, what is kept with this group is what failed.
	fails      string
	failsAfter *callGroup
}

// inOrder returns the trials of trials in any of sets, in their order in
// trials, as the root calls them.
func inOrder(trials []*trial, sets ...[]*trial) []*trial {
	in := map[*trial]bool{}
	for _, set := range sets {
		for _, t := range set {
			in[t] = true
		}
	}
	var ordered []*trial
	for _, t := range trials {
		if in[t] {
			ordered = append(ordered, t)
		}
	}
	return ordered
}

func activeTrials(trials []*trial) []*trial {
	var active []*trial
	for _, t := range trials {
		if t.rejected == "" {
			active = append(active, t)
		}
	}
	return active
}

// judge rejects the trials the plan shows a problem with, and reports
// whether there was none: the plan has no errors, every member plans as
// it did in the root, or only also updates arguments stateOnly names for
// its type, the modules create nothing else, and the totals match
// baseline, with those updates and the imports the calls add. When the
// plan fails with nothing to tell the calls apart by, an error about none
// of them or totals that don't match, it also returns why, and rejects
// none for it: the caller plans them apart.
func judge(active []*trial, p *tfjson.Plan, summary *changeSummary, diags []tfjson.Diagnostic, baseline changeSummary, changes map[string]string, stateOnly map[string][]string) (bool, string) {
	if len(diags) > 0 || p == nil {
		unattributed := ""
		for _, d := range diags {
			t := trialOf(active, d)
			if t == nil {
				if unattributed == "" {
					unattributed = "plan error: " + diagnosticMessage(d)
				}
				continue
			}
			if t.rejected == "" {
				t.rejected = "plan error: " + diagnosticMessage(d)
			}
		}
		return false, unattributed
	}
	ok := true
	expected := baseline
	for _, t := range active {
		expected.Import += len(t.call.Imports)
	}
	for _, rc := range p.ResourceChanges {
		if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil {
			continue
		}
		for _, t := range active {
			if !strings.HasPrefix(rc.Address, "module."+t.name+".") {
				continue
			}
			member := ""
			for _, m := range t.members() {
				if t.moduleAddress(m) == rc.Address {
					member = m
				}
			}
			switch {
			case member == "":
				if !rc.Change.Actions.NoOp() && !rc.Change.Actions.Read() {
					t.rejected = fmt.Sprintf("the module would also %s %s", strings.Join(actionNames(rc.Change.Actions), " and "), strings.TrimPrefix(rc.Address, "module."+t.name+"."))
					ok = false
				}
			case changeSignature(rc) != changes[member]:
				updates, same := sameButStateOnly(changeSignature(rc), changes[member], stateOnlyOf(stateOnly, rc.Type))
				if !same {
					t.rejected = fmt.Sprintf("%s would plan differently: %s instead of %s", member, describeSignature(changeSignature(rc)), describeSignature(changes[member]))
					ok = false
				}
				if updates {
					expected.Change++
				}
			}
		}
	}
	if ok && (summary == nil || *summary != expected) {
		// A change outside the modules: nothing to tell the calls apart by.
		return false, "the plan changed outside the module calls"
	}
	return ok, ""
}

// trialOf returns the trial a diagnostic is about, or nil.
func trialOf(active []*trial, d tfjson.Diagnostic) *trial {
	for _, t := range active {
		prefix := "module." + t.name
		if strings.HasPrefix(d.Address, prefix+".") || d.Address == prefix {
			return t
		}
		if d.Range != nil {
			file := filepath.ToSlash(d.Range.Filename)
			if strings.Contains(file, ".terraform/modules/"+t.name+"/") {
				return t
			}
		}
		if strings.Contains(d.Summary+" "+d.Detail, prefix+".") || strings.Contains(d.Summary+" "+d.Detail, prefix+"\"") {
			return t
		}
	}
	return nil
}

// changeSignature describes a resource's planned change: its actions and
// the attributes that change.
func changeSignature(rc *tfjson.ResourceChange) string {
	return strings.Join(actionNames(rc.Change.Actions), ",") + ":" + strings.Join(changedAttributes(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown), ",")
}

// sameButStateOnly reports whether the change signatures in and root
// differ only in arguments of stateOnly, which import can't set and a
// module may, and whether that makes an import without changes in root an
// update in.
func sameButStateOnly(in, root string, stateOnly []string) (updates, same bool) {
	inActions, inAttributes, _ := strings.Cut(in, ":")
	rootActions, rootAttributes, _ := strings.Cut(root, ":")
	withoutStateOnly := func(attributes string) []string {
		var kept []string
		for _, a := range strings.Split(attributes, ",") {
			if a != "" && !slices.Contains(stateOnly, a) {
				kept = append(kept, a)
			}
		}
		return kept
	}
	if !slices.Equal(withoutStateOnly(inAttributes), withoutStateOnly(rootAttributes)) {
		return false, false
	}
	update := string(tfjson.ActionUpdate)
	noOp := string(tfjson.ActionNoop)
	switch {
	case inActions == rootActions:
		return false, true
	case inActions == update && rootActions == noOp:
		return true, true
	}
	return false, false
}

func describeSignature(s string) string {
	actions, attributes, _ := strings.Cut(s, ":")
	if actions == "" {
		return "nothing"
	}
	if attributes == "" {
		return actions
	}
	return actions + " (" + attributes + ")"
}

func actionNames(actions tfjson.Actions) []string {
	names := make([]string, 0, len(actions))
	for _, a := range actions {
		names = append(names, string(a))
	}
	return names
}

// rejections lists the trials the plan rejected.
func rejections(trials []*trial) []ModuleCall {
	var calls []ModuleCall
	for _, t := range trials {
		if t.rejected == "" {
			continue
		}
		calls = append(calls, ModuleCall{Source: t.adapter.Source, Version: t.adapter.Version, Resources: t.members(), Declined: t.rejected})
	}
	return calls
}

// mapClusters builds each adapter's clusters in generated.tf, anchors in
// address order, and maps them. A resource joins the first cluster whose
// anchor it refers to, if that cluster has no member of its type yet or
// the type may repeat. It returns the clusters the adapters mapped and the
// ones they declined.
func mapClusters(dir string, list []adapters.Adapter, taken Names, values map[string]map[string]any) ([]*trial, []ModuleCall, error) {
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, nil, err
	}
	resources := generated.resources()
	// Every anchor and every trial asks about the same references: read
	// them once.
	refs := indexReferences(resources)
	clustered := map[string]bool{}
	used := map[string]bool{}
	for _, b := range generated.syntax.Blocks {
		if b.Type == "module" && len(b.Labels) == 1 {
			used[b.Labels[0]] = true
		}
	}
	for name := range taken {
		if call, ok := strings.CutPrefix(name, "module."); ok {
			used[call] = true
		}
	}
	var trials []*trial
	var declined []ModuleCall
	for ai := range list {
		a := &list[ai]
		grouped := map[string]bool{}
		for _, anchor := range resources {
			if clustered[anchor.address] || resourceTypeOf(anchor.address) != a.Anchor {
				continue
			}
			c := adapters.Cluster{Anchor: resourceOf(anchor)}
			// refers reports whether r joins the cluster as one of Members:
			// by referring to the anchor, or, for the anchor's type in a
			// group, by having its key.
			refers := func(r resourceBlock) bool {
				return refs.refers[r.address][anchor.address]
			}
			if a.Group != nil {
				key, ok := a.Group(c.Anchor)
				if !ok || grouped[key] {
					continue
				}
				// A group is tried once: when it is declined, its other
				// resources don't anchor it again.
				grouped[key] = true
				refers = func(r resourceBlock) bool {
					if resourceTypeOf(r.address) != a.Anchor {
						return refs.refers[r.address][anchor.address]
					}
					k, ok := a.Group(resourceOf(r))
					return ok && k == key
				}
			}
			types := map[string]bool{}
			in := []resourceBlock{anchor}
			// join adds r to the cluster if it is of one of kinds, linked to
			// it, and in no cluster yet.
			join := func(r resourceBlock, kinds []string, linked func(resourceBlock) bool) bool {
				typ := resourceTypeOf(r.address)
				if clustered[r.address] || slices.ContainsFunc(in, func(b resourceBlock) bool { return b.address == r.address }) ||
					(types[typ] && !slices.Contains(a.Repeated, typ)) || !slices.Contains(kinds, typ) || !linked(r) {
					return false
				}
				types[typ] = true
				in = append(in, r)
				c.Members = append(c.Members, resourceOf(r))
				return true
			}
			// Without a group, only what refers to the anchor can join.
			candidates := resources
			if a.Group == nil {
				candidates = refs.referrers(anchor.address, resources)
			}
			for _, r := range candidates {
				join(r, a.Members, refers)
			}
			// Then the members that join through the others, until none does.
			for joined := len(a.Through) > 0; joined; {
				joined = false
				for _, r := range resources {
					if join(r, a.Through, func(r resourceBlock) bool { return refs.linked(r.address, in) }) {
						joined = true
					}
				}
			}
			slices.SortFunc(c.Members, func(x, y adapters.Resource) int { return strings.Compare(x.Address(), y.Address()) })
			// Members that use another configuration of the provider are
			// mapped without it, and the call passes it to the module.
			provider, setting := sharedProvider(c)
			setProvider(setting, nil)
			call, err := a.Map(c)
			t := &trial{adapter: a, cluster: c, call: call, provider: provider}
			if err != nil {
				setProvider(setting, provider)
			}
			if adapters.IsDecline(err) {
				declined = append(declined, ModuleCall{Source: a.Source, Version: a.Version, Resources: t.members(), Declined: err.Error()})
				continue
			}
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", a.Source, err)
			}
			t.cluster.Members = slices.DeleteFunc(slices.Clone(c.Members), func(m adapters.Resource) bool { return slices.Contains(call.Left, m.Address()) })
			// What the call leaves in the root keeps its configuration.
			setProvider(slices.DeleteFunc(slices.Clone(setting), func(r adapters.Resource) bool { return !slices.Contains(call.Left, r.Address()) }), provider)
			if reason := unfollowable(t, refs, values); reason != "" {
				setProvider(setting, provider)
				declined = append(declined, ModuleCall{Source: a.Source, Version: a.Version, Resources: t.members(), Declined: reason})
				continue
			}
			name := callName(anchor.address)
			if call.Name != "" {
				name = call.Name
			}
			t.name = uniqueName(name, used)
			for _, m := range t.members() {
				clustered[m] = true
			}
			trials = append(trials, t)
		}
	}
	return trials, declined, nil
}

// clusterReferences has the references between the resources of a root,
// read once: mapClusters asks about them for every anchor and every
// trial, and walking every resource's body each time made building
// clusters quadratic in the resources of a root.
type clusterReferences struct {
	// refers has, by address, the resources each resource refers to.
	refers map[string]map[string]bool
	// incoming has, by address, the references to each resource, in the
	// order of the resources they are in.
	incoming map[string][]reference
	// position has each resource's place in the root, by address.
	position map[string]int
}

// reference is a traversal in the resource at from.
type reference struct {
	from      string
	traversal hcl.Traversal
}

// indexReferences reads the references of resources (see
// referencedAddresses and allTraversals).
func indexReferences(resources []resourceBlock) clusterReferences {
	idx := clusterReferences{refers: map[string]map[string]bool{}, incoming: map[string][]reference{}, position: map[string]int{}}
	for i, r := range resources {
		idx.position[r.address] = i
		refers := map[string]bool{}
		for _, tr := range allTraversals(r.syntax.Body) {
			target := resourceAddress(tr)
			if target == "" {
				continue
			}
			refers[target] = true
			idx.incoming[target] = append(idx.incoming[target], reference{from: r.address, traversal: tr})
		}
		idx.refers[r.address] = refers
	}
	return idx
}

// referrers returns the resources of resources, those of the root in
// their order, that refer to the resource at address, in that order.
func (idx clusterReferences) referrers(address string, resources []resourceBlock) []resourceBlock {
	var referring []resourceBlock
	// incoming has the references of each resource next to each other.
	for _, ref := range idx.incoming[address] {
		r := resources[idx.position[ref.from]]
		if len(referring) == 0 || referring[len(referring)-1].address != r.address {
			referring = append(referring, r)
		}
	}
	return referring
}

// linked reports whether the resource at address refers to one of
// cluster, or one of cluster refers to it.
func (idx clusterReferences) linked(address string, cluster []resourceBlock) bool {
	for _, b := range cluster {
		if idx.refers[address][b.address] || idx.refers[b.address][address] {
			return true
		}
	}
	return false
}

func resourceOf(r resourceBlock) adapters.Resource {
	typ, name, _ := strings.Cut(r.address, ".")
	return adapters.Resource{Type: typ, Name: name, Body: r.write.Body()}
}

func uniqueName(base string, used map[string]bool) string {
	name := base
	for n := 2; used[name]; n++ {
		name = fmt.Sprintf("%s_%d", base, n)
	}
	used[name] = true
	return name
}

// unfollowable returns why references to a trial's members, from the
// resources that stay and from the call's own arguments, can't all become
// references to the module's outputs or, for a string the module has no
// output for, its imported value in values (see trial.literals), or "".
// refs has the references of the root's resources.
func unfollowable(t *trial, refs clusterReferences, values map[string]map[string]any) string {
	members := t.members()
	var traversals []hcl.Traversal
	// Only the references to members matter: those from the resources
	// that stay, in the order of the resources.
	var incoming []reference
	for _, m := range members {
		for _, ref := range refs.incoming[m] {
			if !slices.Contains(members, ref.from) {
				incoming = append(incoming, ref)
			}
		}
	}
	slices.SortStableFunc(incoming, func(x, y reference) int { return refs.position[x.from] - refs.position[y.from] })
	for _, ref := range incoming {
		traversals = append(traversals, ref.traversal)
	}
	for _, arg := range t.call.Arguments {
		expr, diags := hclsyntax.ParseExpression(arg.Value.Bytes(), "", hcl.InitialPos)
		if diags.HasErrors() {
			return fmt.Sprintf("argument %s doesn't parse: %v", arg.Name, diags)
		}
		traversals = append(traversals, expr.Variables()...)
	}
	for _, tr := range traversals {
		target := resourceAddress(tr)
		if !slices.Contains(members, target) {
			continue
		}
		if len(tr) < 3 {
			return fmt.Sprintf("a reference to the whole of %s", target)
		}
		attr, ok := tr[2].(hcl.TraverseAttr)
		if ok && t.call.Outputs[target][attr.Name] != "" {
			continue
		}
		// A reference to a string the module has no output for becomes
		// the string again.
		if v, isString := values[target][attr.Name].(string); ok && isString && len(tr) == 3 {
			if t.literals == nil {
				t.literals = map[string]string{}
			}
			t.literals[target+"."+attr.Name] = v
			continue
		}
		return fmt.Sprintf("the module has no output for a reference to %s", hclwrite.TokensForTraversal(tr[:3]).Bytes())
	}
	return ""
}

// writeCalls replaces the trials' resources in generated.tf with their
// module calls, makes references to them use the modules' outputs,
// imports into the modules, and configures the other providers the
// modules use, of moduleProviders.
func writeCalls(dir string, trials []*trial, moduleProviders map[string]Provider) error {
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return err
	}
	body := generated.edit().Body()
	moved := map[string]bool{}
	importTo := map[string]string{}
	var renames []referenceRename
	for _, t := range trials {
		for _, m := range t.members() {
			moved[m] = true
			importTo[m] = t.moduleAddress(m)
			typ, name, _ := strings.Cut(m, ".")
			for attr, output := range t.call.Outputs[m] {
				replacement, err := outputTokens(t.name, output)
				if err != nil {
					return fmt.Errorf("%s: output for %s.%s: %w", t.adapter.Source, m, attr, err)
				}
				renames = append(renames, referenceRename{[]string{typ, name, attr}, replacement})
			}
		}
		for reference, v := range t.literals {
			renames = append(renames, referenceRename{strings.Split(reference, "."), hclwrite.TokensForValue(cty.StringVal(v))})
		}
	}
	byStart := indexRenames(len(renames), func(i int) []string { return renames[i].search })
	for _, r := range generated.resources() {
		if moved[r.address] {
			body.RemoveBlock(r.write)
			continue
		}
		replaceAllReferences(r.write.Body(), renames, byStart)
	}
	for _, t := range trials {
		block := hclwrite.NewBlock("module", []string{t.name})
		source, err := callSource(dir, t.adapter.Source)
		if err != nil {
			return err
		}
		block.Body().SetAttributeValue("source", cty.StringVal(source))
		// Git and local modules have none: a git source pins its ref.
		if t.adapter.Version != "" {
			block.Body().SetAttributeValue("version", cty.StringVal(t.adapter.Version))
		}
		if t.provider != nil {
			local := strings.TrimSpace(string(t.provider.Bytes()))
			local, _, _ = strings.Cut(local, ".")
			block.Body().SetAttributeRaw("providers", hclwrite.TokensForObject([]hclwrite.ObjectAttrTokens{{Name: hclwrite.TokensForIdentifier(local), Value: t.provider}}))
		}
		block.Body().AppendNewline()
		for _, arg := range t.call.Arguments {
			// As a person would call the module: without its defaults.
			if t.adapter.IsDefault(arg.Name, arg.Value) {
				continue
			}
			block.Body().SetAttributeRaw(arg.Name, arg.Value)
		}
		replaceAllReferences(block.Body(), renames, byStart)
		body.AppendNewline()
		body.AppendBlock(block)
	}
	if err := generated.save(); err != nil {
		return err
	}
	if err := writeModuleProviders(dir, trials, moduleProviders); err != nil {
		return err
	}
	var added []moduleImport
	for _, t := range trials {
		for _, imp := range t.call.Imports {
			added = append(added, moduleImport{to: "module." + t.name + "." + imp.To, Import: imp})
		}
	}
	return retargetImports(dir, importTo, added)
}

// callSource is how a root in dir calls a module from source: a module in
// a directory, at an absolute path, by its path relative to dir, as
// Terraform requires; any other source as it is.
func callSource(dir, source string) (string, error) {
	if !filepath.IsAbs(source) {
		return source, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(abs, source)
	if err != nil {
		return "", fmt.Errorf("module %s: %w", source, err)
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, "../") {
		rel = "./" + rel
	}
	return rel, nil
}

// moduleImport is an import a module call adds, into to.
type moduleImport struct {
	adapters.Import
	to string
}

// retargetImports points the import blocks into the given resources at
// their new addresses, such as module.logs.aws_s3_bucket.this[0], and adds
// the imports of added, with IDs that start with the import ID of the
// resource each names.
func retargetImports(dir string, to map[string]string, added []moduleImport) error {
	imports, err := loadHCL(filepath.Join(dir, ImportsFileName))
	if err != nil {
		return err
	}
	ids := map[string]string{}
	for _, imp := range imports.imports() {
		ids[imp.to] = imp.id
		address, ok := to[imp.to]
		if !ok {
			continue
		}
		traversal, diags := hclsyntax.ParseTraversalAbs([]byte(address), "", hcl.InitialPos)
		if diags.HasErrors() {
			return fmt.Errorf("import into %s: %w", address, diags)
		}
		imp.write.Body().SetAttributeTraversal("to", traversal)
	}
	body := imports.edit().Body()
	for _, imp := range added {
		id := ids[imp.Of]
		if id == "" {
			return fmt.Errorf("import into %s: %s has no import ID", imp.to, imp.Of)
		}
		traversal, diags := hclsyntax.ParseTraversalAbs([]byte(imp.to), "", hcl.InitialPos)
		if diags.HasErrors() {
			return fmt.Errorf("import into %s: %w", imp.to, diags)
		}
		body.AppendNewline()
		block := body.AppendNewBlock("import", nil).Body()
		block.SetAttributeTraversal("to", traversal)
		block.SetAttributeValue("id", cty.StringVal(id+imp.Suffix))
	}
	return imports.save()
}

// importTargets returns where each import block of dir imports into, by
// resource type and import ID: resources of different types can share an
// ID, such as a bucket and its versioning.
func importTargets(dir string) (map[string]string, error) {
	imports, err := loadHCL(filepath.Join(dir, ImportsFileName))
	if err != nil {
		return nil, err
	}
	return importTargetsIn(imports), nil
}

// importTargetsIn returns where each import block of f with a literal ID
// imports into, by resource type and import ID.
func importTargetsIn(f *hclFile) map[string]string {
	targets := map[string]string{}
	for _, b := range f.syntax.Blocks {
		to, ok := b.Body.Attributes["to"]
		id, hasID := b.Body.Attributes["id"]
		if b.Type != "import" || !ok || !hasID {
			continue
		}
		v, diags := id.Expr.Value(nil)
		if diags.HasErrors() || v.Type() != cty.String || !v.IsKnown() || v.IsNull() {
			continue
		}
		address := strings.TrimSpace(string(to.Expr.Range().SliceBytes(f.src)))
		targets[AddressType(address)+"\x00"+v.AsString()] = address
	}
	return targets
}

// movedAddresses returns the new address of each resource whose import
// block now imports elsewhere than in before (see importTargets), by its
// old address.
func movedAddresses(dir string, before map[string]string) (map[string]string, error) {
	after, err := importTargets(dir)
	if err != nil {
		return nil, err
	}
	moved := map[string]string{}
	for key, from := range before {
		if to, ok := after[key]; ok && to != from {
			moved[from] = to
		}
	}
	return moved, nil
}

// moduleCalls lists the module calls in generated.tf, with the resources
// moved into each, followed by the clusters declined.
func moduleCalls(dir string, moved map[string]string, declined []ModuleCall) ([]ModuleCall, error) {
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, err
	}
	var calls []ModuleCall
	for _, b := range generated.syntax.Blocks {
		if b.Type != "module" || len(b.Labels) != 1 {
			continue
		}
		call := ModuleCall{Name: b.Labels[0], Source: stringAttribute(b.Body, "source"), Version: stringAttribute(b.Body, "version")}
		for from, to := range moved {
			if strings.HasPrefix(to, "module."+call.Name+".") {
				call.Resources = append(call.Resources, from)
			}
		}
		sort.Strings(call.Resources)
		calls = append(calls, call)
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].Name < calls[j].Name })
	return append(calls, declined...), nil
}

// stringAttribute returns an attribute's literal string value, or "".
func stringAttribute(body *hclsyntax.Body, name string) string {
	attr, ok := body.Attributes[name]
	if !ok {
		return ""
	}
	v, diags := attr.Expr.Value(nil)
	if diags.HasErrors() || v.Type() != cty.String || !v.IsKnown() || v.IsNull() {
		return ""
	}
	return v.AsString()
}

// outputTokens returns the reference to a module output: output is an
// output's name, or a path into it, such as subnets["eu/app"].self_link
// (see adapters.Call.Outputs).
func outputTokens(call, output string) (hclwrite.Tokens, error) {
	traversal, diags := hclsyntax.ParseTraversalAbs([]byte("module."+call+"."+output), "", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, diags
	}
	return hclwrite.TokensForTraversal(traversal), nil
}

// referenceRename makes the references that begin with the steps of search
// begin with replacement instead (see replaceReferences).
type referenceRename struct {
	search      []string
	replacement hclwrite.Tokens
}

// replaceAllReferences applies renames to body and its nested blocks, in
// order: those byStart, their index (see indexRenames), finds in body.
func replaceAllReferences(body *hclwrite.Body, renames []referenceRename, byStart renameIndex) {
	for _, i := range byStart.in(body) {
		replaceReferences(body, renames[i].search, renames[i].replacement)
	}
}

// renameIndex is a list of renames by the first two steps of what they
// search for, aws_vpc.main for aws_vpc.main.id, so that a body is handed
// only the renames of the references it has: a root's renames are for
// every resource moved into a module, and each resource refers to a few.
// The renames whose search has fewer steps are under "".
type renameIndex map[string][]int

// indexRenames indexes n renames, whose searches search returns.
func indexRenames(n int, search func(i int) []string) renameIndex {
	idx := renameIndex{}
	for i := range n {
		key := ""
		if s := search(i); len(s) >= 2 {
			key = s[0] + "." + s[1]
		}
		idx[key] = append(idx[key], i)
	}
	return idx
}

// in returns the indexes, in order, of the renames that may have something
// to replace in body and its nested blocks: those whose search begins with
// two steps a reference in body begins with, an identifier that doesn't
// follow a dot, a dot and an identifier, as spliceTraversal and
// RenameVariablePrefix match them. A rename's replacement never begins
// with what another searches for, a resource type, so the references
// body has before the renames are those it has between them.
func (idx renameIndex) in(body *hclwrite.Body) []int {
	found := slices.Clone(idx[""])
	var visit func(body *hclwrite.Body)
	seen := map[string]bool{}
	visit = func(body *hclwrite.Body) {
		for _, attr := range body.Attributes() {
			tokens := attr.Expr().BuildTokens(nil)
			for i := 0; i+2 < len(tokens); i++ {
				if tokens[i].Type != hclsyntax.TokenIdent || (i > 0 && tokens[i-1].Type == hclsyntax.TokenDot) ||
					tokens[i+1].Type != hclsyntax.TokenDot || tokens[i+2].Type != hclsyntax.TokenIdent {
					continue
				}
				key := string(tokens[i].Bytes) + "." + string(tokens[i+2].Bytes)
				if !seen[key] {
					seen[key] = true
					found = append(found, idx[key]...)
				}
			}
		}
		for _, b := range body.Blocks() {
			visit(b.Body())
		}
	}
	visit(body)
	slices.Sort(found)
	return found
}

// replaceReferences replaces, in body and its nested blocks, the start of
// every reference that begins with the steps of search, such as
// aws_s3_bucket.logs.arn, with replacement.
func replaceReferences(body *hclwrite.Body, search []string, replacement hclwrite.Tokens) {
	for name, attr := range body.Attributes() {
		tokens := attr.Expr().BuildTokens(nil)
		if replaced, ok := spliceTraversal(tokens, search, replacement); ok {
			body.SetAttributeRaw(name, replaced)
		}
	}
	for _, b := range body.Blocks() {
		replaceReferences(b.Body(), search, replacement)
	}
}

// spliceTraversal replaces each run of tokens that spells the steps of
// search, a.b.c, at the start of a traversal, with replacement. It reports
// whether it replaced any.
func spliceTraversal(tokens hclwrite.Tokens, search []string, replacement hclwrite.Tokens) (hclwrite.Tokens, bool) {
	n := 2*len(search) - 1
	var out hclwrite.Tokens
	replaced := false
	for i := 0; i < len(tokens); i++ {
		if i+n <= len(tokens) && (i == 0 || tokens[i-1].Type != hclsyntax.TokenDot) && spells(tokens[i:i+n], search) {
			for j, tok := range replacement {
				copied := *tok
				if j == 0 {
					copied.SpacesBefore = tokens[i].SpacesBefore
				}
				out = append(out, &copied)
			}
			replaced = true
			i += n - 1
			continue
		}
		out = append(out, tokens[i])
	}
	return out, replaced
}

// spells reports whether tokens are the identifiers of steps, joined by
// dots.
func spells(tokens hclwrite.Tokens, steps []string) bool {
	for i, step := range steps {
		ident := tokens[2*i]
		if ident.Type != hclsyntax.TokenIdent || string(ident.Bytes) != step {
			return false
		}
		if i > 0 && tokens[2*i-1].Type != hclsyntax.TokenDot {
			return false
		}
	}
	return true
}

// writeModuleProviders adds to providers.tf the blocks, and to
// versions.tf the sources, of the providers of configured that the
// trials' modules use (see adapters.Adapter.Providers), unless the root
// has them. Its version is left to the modules' constraints.
func writeModuleProviders(dir string, trials []*trial, configured map[string]Provider) error {
	var names []string
	for _, t := range trials {
		for _, name := range t.adapter.Providers {
			if _, ok := configured[name]; ok && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return requireProviders(dir, names, configured)
}

// requireProviders adds to providers.tf the blocks, and to versions.tf
// the sources, of the providers of configured that names names, unless
// the root has them: those of a module call (see writeModuleProviders)
// or of resources whose type another provider implies (see
// resourceProviders). It sets no version: pinModuleProviders pins each
// to the release init selected.
func requireProviders(dir string, names []string, configured map[string]Provider) error {
	if len(names) == 0 {
		return nil
	}
	names = slices.Sorted(slices.Values(names))
	providers, err := loadHCL(filepath.Join(dir, ProvidersFileName))
	if err != nil {
		return err
	}
	versions, err := loadHCL(filepath.Join(dir, VersionsFileName))
	if err != nil {
		return err
	}
	var required *hclwrite.Body
	for _, b := range versions.edit().Body().Blocks() {
		if b.Type() != "terraform" {
			continue
		}
		for _, nested := range b.Body().Blocks() {
			if nested.Type() == "required_providers" {
				required = nested.Body()
			}
		}
	}
	if required == nil {
		return fmt.Errorf("%s has no required_providers", VersionsFileName)
	}
	for _, name := range names {
		p := configured[name]
		if required.GetAttribute(name) == nil {
			required.SetAttributeValue(name, cty.ObjectVal(map[string]cty.Value{"source": cty.StringVal(p.Source)}))
		}
		if providers.edit().Body().FirstMatchingBlock("provider", []string{name}) != nil {
			continue
		}
		config, _ := withoutCredentials(p.Config)
		providers.edit().Body().AppendNewline()
		if err := writeBody(providers.edit().Body().AppendNewBlock("provider", []string{name}).Body(), config); err != nil {
			return fmt.Errorf("provider %s: %w", name, err)
		}
	}
	if err := providers.save(); err != nil {
		return err
	}
	return versions.save()
}

// pinModuleProviders pins each provider of configured that versions.tf
// requires without a version to the major and minor version the lock file
// selected for it, as the root's own provider is pinned.
func pinModuleProviders(dir string, configured map[string]Provider) error {
	if len(configured) == 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, LockFileName)); err != nil {
		return nil
	}
	lock, err := loadHCL(filepath.Join(dir, LockFileName))
	if err != nil {
		return err
	}
	versions, err := loadHCL(filepath.Join(dir, VersionsFileName))
	if err != nil {
		return err
	}
	names := make([]string, 0, len(configured))
	for name := range configured {
		names = append(names, name)
	}
	sort.Strings(names)
	changed := false
	for _, b := range versions.edit().Body().Blocks() {
		if b.Type() != "terraform" {
			continue
		}
		for _, required := range b.Body().Blocks() {
			if required.Type() != "required_providers" {
				continue
			}
			for _, name := range names {
				attr := required.Body().GetAttribute(name)
				if attr == nil {
					continue
				}
				requirement, ok := literalObject(attr.Expr().BuildTokens(nil))
				if !ok || requirement.Type().HasAttribute("version") {
					continue
				}
				selected := lockedVersion(lock, configured[name].Source)
				if selected == nil {
					continue
				}
				required.Body().SetAttributeValue(name, cty.ObjectVal(map[string]cty.Value{
					"source":  cty.StringVal(configured[name].Source),
					"version": cty.StringVal(ProviderConstraint(selected)),
				}))
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	return versions.save()
}

// lockedVersion returns the version the lock file selected of the
// provider at source, such as hashicorp/google-beta, on whichever registry.
func lockedVersion(lock *hclFile, source string) *version.Version {
	suffix := "/" + strings.ToLower(source)
	for _, b := range lock.syntax.Blocks {
		if b.Type != "provider" || len(b.Labels) != 1 || !strings.HasSuffix(strings.ToLower(b.Labels[0]), suffix) {
			continue
		}
		if v, err := version.NewVersion(stringAttribute(b.Body, "version")); err == nil {
			return v
		}
	}
	return nil
}

// literalObject returns the value of an object expression without
// references, such as a provider requirement.
func literalObject(tokens hclwrite.Tokens) (cty.Value, bool) {
	expr, diags := hclsyntax.ParseExpression(tokens.Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() {
		return cty.NilVal, false
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() || !v.Type().IsObjectType() {
		return cty.NilVal, false
	}
	return v, true
}

// sharedProvider returns the provider meta-argument that the resources of
// a cluster which set one share, such as aws.without_default_tags (see
// applyTagLift), and those resources; nil if none sets one, or two set
// different ones. The call passes it to the module, so the others use it
// too: those that take no tags, such as a bucket's versioning, plan the
// same through either configuration; one the provider tags would lose
// the default tags, which the plan then rejects.
func sharedProvider(c adapters.Cluster) (hclwrite.Tokens, []adapters.Resource) {
	var shared hclwrite.Tokens
	var setting []adapters.Resource
	for _, r := range append([]adapters.Resource{c.Anchor}, c.Members...) {
		attr := r.Body.GetAttribute("provider")
		if attr == nil {
			continue
		}
		tokens := attr.Expr().BuildTokens(nil)
		if shared != nil && strings.TrimSpace(string(tokens.Bytes())) != strings.TrimSpace(string(shared.Bytes())) {
			return nil, nil
		}
		shared = tokens
		setting = append(setting, r)
	}
	return shared, setting
}

// setProvider sets the provider meta-argument of resources to provider, or
// removes it for nil.
func setProvider(resources []adapters.Resource, provider hclwrite.Tokens) {
	for _, r := range resources {
		if provider == nil {
			r.Body.RemoveAttribute("provider")
		} else {
			r.Body.SetAttributeRaw("provider", provider)
		}
	}
}
