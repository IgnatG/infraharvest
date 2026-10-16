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
	// rejected is why the plan didn't accept the call.
	rejected string
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
// matches. A module may set the arguments opts.StateOnly names, which
// import can't. Calls are named other than the modules in opts.Taken. It
// returns the clusters it didn't move, with why, and the plan's change
// summary with the calls it kept.
func synthesize(ctx context.Context, tf Terraform, dir string, opts Options, baseline changeSummary, changes map[string]string, vars []tfexec.PlanOption) ([]ModuleCall, changeSummary, error) {
	backup, err := backupFiles(dir, GeneratedFileName, ImportsFileName, ProvidersFileName, VersionsFileName)
	if err != nil {
		return nil, baseline, err
	}
	trials, declined, err := mapClusters(dir, opts.Adapters, opts.Taken)
	if err != nil || len(trials) == 0 {
		return declined, baseline, err
	}
	reject := func(reason string, ts ...*trial) {
		for _, t := range ts {
			if t.rejected == "" {
				t.rejected = reason
			}
		}
	}
	if err := writeCalls(dir, trials, opts.ModuleProviders); err != nil {
		return nil, baseline, errors.Join(err, backup.restore())
	}
	if err := initTerraform(ctx, tf); err != nil {
		// Typically: the registry can't be reached.
		reject(fmt.Sprintf("terraform init: %v", err), trials...)
	}
	for round := 0; round <= len(trials); round++ {
		active := activeTrials(trials)
		if len(active) == 0 {
			break
		}
		if round > 0 {
			if err := backup.restore(); err != nil {
				return nil, baseline, err
			}
			if err := writeCalls(dir, active, opts.ModuleProviders); err != nil {
				return nil, baseline, errors.Join(err, backup.restore())
			}
		}
		p, summary, diags, err := showPlanWithSummary(ctx, tf, vars)
		if err != nil {
			return nil, baseline, errors.Join(err, backup.restore())
		}
		if !judge(active, p, summary, diags, baseline, changes, opts.StateOnly) {
			if len(activeTrials(trials)) == len(active) {
				// Nothing to take back (the plan reported neither errors
				// nor a result): planning again would show the same.
				reject("the plan reported no result", active...)
				break
			}
			continue
		}
		if err := pinModuleProviders(dir, opts.ModuleProviders); err != nil {
			return nil, baseline, err
		}
		return append(declined, rejections(trials)...), *summary, nil
	}
	reject("the plan didn't settle", trials...)
	if err := backup.restore(); err != nil {
		return nil, baseline, err
	}
	return append(declined, rejections(trials)...), baseline, nil
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
// baseline, with those updates and the imports the calls add.
func judge(active []*trial, p *tfjson.Plan, summary *changeSummary, diags []tfjson.Diagnostic, baseline changeSummary, changes map[string]string, stateOnly map[string][]string) bool {
	if len(diags) > 0 || p == nil {
		for _, d := range diags {
			t := trialOf(active, d)
			if t == nil {
				// Nothing to tell the calls apart by: take them all back.
				for _, t := range active {
					t.rejected = "plan error: " + diagnosticMessage(d)
				}
				return false
			}
			if t.rejected == "" {
				t.rejected = "plan error: " + diagnosticMessage(d)
			}
		}
		return false
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
				updates, same := sameButStateOnly(changeSignature(rc), changes[member], stateOnly[rc.Type])
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
		for _, t := range active {
			t.rejected = "the plan changed outside the module calls"
		}
		return false
	}
	return ok
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
func mapClusters(dir string, list []adapters.Adapter, taken Names) ([]*trial, []ModuleCall, error) {
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return nil, nil, err
	}
	resources := generated.resources()
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
				return slices.Contains(referencedAddresses(r.syntax.Body), anchor.address)
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
						return slices.Contains(referencedAddresses(r.syntax.Body), anchor.address)
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
			for _, r := range resources {
				join(r, a.Members, refers)
			}
			// Then the members that join through the others, until none does.
			for joined := len(a.Through) > 0; joined; {
				joined = false
				for _, r := range resources {
					if join(r, a.Through, func(r resourceBlock) bool { return linkedTo(r, in) }) {
						joined = true
					}
				}
			}
			slices.SortFunc(c.Members, func(x, y adapters.Resource) int { return strings.Compare(x.Address(), y.Address()) })
			call, err := a.Map(c)
			t := &trial{adapter: a, cluster: c, call: call}
			if adapters.IsDecline(err) {
				declined = append(declined, ModuleCall{Source: a.Source, Version: a.Version, Resources: t.members(), Declined: err.Error()})
				continue
			}
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", a.Source, err)
			}
			t.cluster.Members = slices.DeleteFunc(slices.Clone(c.Members), func(m adapters.Resource) bool { return slices.Contains(call.Left, m.Address()) })
			if reason := unfollowable(t, resources); reason != "" {
				declined = append(declined, ModuleCall{Source: a.Source, Version: a.Version, Resources: t.members(), Declined: reason})
				continue
			}
			name := anchor.address[strings.IndexByte(anchor.address, '.')+1:]
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

// linkedTo reports whether r refers to one of cluster, or one of cluster
// refers to r.
func linkedTo(r resourceBlock, cluster []resourceBlock) bool {
	refs := referencedAddresses(r.syntax.Body)
	for _, b := range cluster {
		if slices.Contains(refs, b.address) || slices.Contains(referencedAddresses(b.syntax.Body), r.address) {
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
// references to the module's outputs, or "".
func unfollowable(t *trial, resources []resourceBlock) string {
	members := t.members()
	var traversals []hcl.Traversal
	for _, r := range resources {
		if !slices.Contains(members, r.address) {
			traversals = append(traversals, allTraversals(r.syntax.Body)...)
		}
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
		if !ok || t.call.Outputs[target][attr.Name] == "" {
			return fmt.Sprintf("the module has no output for a reference to %s", hclwrite.TokensForTraversal(tr[:3]).Bytes())
		}
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
	body := generated.file.Body()
	moved := map[string]bool{}
	importTo := map[string]string{}
	type rename struct {
		search      []string
		replacement hclwrite.Tokens
	}
	var renames []rename
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
				renames = append(renames, rename{[]string{typ, name, attr}, replacement})
			}
		}
	}
	for _, r := range generated.resources() {
		if moved[r.address] {
			body.RemoveBlock(r.write)
			continue
		}
		for _, rn := range renames {
			replaceReferences(r.write.Body(), rn.search, rn.replacement)
		}
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
		block.Body().AppendNewline()
		for _, arg := range t.call.Arguments {
			block.Body().SetAttributeRaw(arg.Name, arg.Value)
		}
		for _, rn := range renames {
			replaceReferences(block.Body(), rn.search, rn.replacement)
		}
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
	body := imports.file.Body()
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
		targets[addressType(address)+"\x00"+v.AsString()] = address
	}
	return targets
}

// addressType returns the resource type of an address such as
// aws_s3_bucket.logs or module.logs.aws_s3_bucket.this[0].
func addressType(address string) string {
	parts := strings.Split(address, ".")
	for len(parts) > 2 && parts[0] == "module" {
		parts = parts[2:]
	}
	return parts[0]
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
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	providers, err := loadHCL(filepath.Join(dir, ProvidersFileName))
	if err != nil {
		return err
	}
	versions, err := loadHCL(filepath.Join(dir, VersionsFileName))
	if err != nil {
		return err
	}
	var required *hclwrite.Body
	for _, b := range versions.file.Body().Blocks() {
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
		if providers.file.Body().FirstMatchingBlock("provider", []string{name}) != nil {
			continue
		}
		config, _ := WithoutCredentials(p.Config)
		providers.file.Body().AppendNewline()
		if err := writeBody(providers.file.Body().AppendNewBlock("provider", []string{name}).Body(), config); err != nil {
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
	for _, b := range versions.file.Body().Blocks() {
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
