// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

// stateOnlyChange is what the plan check's details say of a resource that
// only updates state-only or secret arguments: the README tells people to
// expect those updates (see WriteReadme).
const stateOnlyChange = "only state-only or secret arguments change"

// planCheck passes if the plan imports expected resources and has no
// changes besides those imports, other than updates to the attributes
// secrets set, to what a secret's placeholder makes unknown, or to the
// stateOnly arguments of the resource's type. An expected below zero
// doesn't check the number of imports.
func planCheck(p *tfjson.Plan, diags []tfjson.Diagnostic, secrets []Secret, stateOnly map[string][]string, expected int) Check {
	return planCheckWith(p, diags, secrets, stateOnly, expected, nil)
}

// goneChange is what the plan check's details say of a resource no longer
// in the cloud that the plan creates again (see planCheckWith).
const goneChange = "no longer in the cloud: the plan creates it again"

// planCheckWith is planCheck, where the plan may also create gone, the
// addresses of resources no longer in the cloud, whose import it leaves
// out (see MergeRoot.Gone).
func planCheckWith(p *tfjson.Plan, diags []tfjson.Diagnostic, secrets []Secret, stateOnly map[string][]string, expected int, gone map[string]bool) Check {
	check := Check{Name: CheckPlan}
	if len(diags) > 0 || p == nil {
		for _, d := range diags {
			check.Details = append(check.Details, formatDiagnostic(d))
		}
		if len(diags) == 0 {
			check.Details = append(check.Details, "terraform plan reported no change summary")
		}
		return check
	}
	secretPaths := map[string][]string{}
	for _, s := range secrets {
		secretPaths[s.Address] = append(secretPaths[s.Address], withoutIndexes(s.Attribute))
	}
	check.Passed = true
	importing := 0
	for _, rc := range p.ResourceChanges {
		if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil {
			continue
		}
		if rc.Change.Importing != nil {
			importing++
		}
		if rc.Change.Actions.NoOp() || rc.Change.Actions.Read() {
			continue
		}
		change := PlannedChange{Address: rc.Address}
		for _, a := range rc.Change.Actions {
			change.Actions = append(change.Actions, string(a))
		}
		if gone[rc.Address] && rc.Change.Actions.Create() {
			check.Details = append(check.Details, fmt.Sprintf("%s: %s", rc.Address, goneChange))
			continue
		}
		if !rc.Change.Actions.Update() {
			check.Passed = false
			check.Changes = append(check.Changes, change)
			continue
		}
		leaves := changedLeaves(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown)
		change.Attributes = topLevel(leaves)
		// A placeholder secret value also makes what the provider computes
		// from it unknown, such as an SSM parameter's version.
		placeholderChanged := false
		for _, l := range leaves {
			if slices.Contains(secretPaths[rc.Address], withoutIndexes(l.path)) {
				placeholderChanged = true
			}
		}
		explained := true
		for _, l := range leaves {
			switch {
			case slices.Contains(stateOnlyOf(stateOnly, rc.Type), topOf(l.path)):
			case slices.Contains(secretPaths[rc.Address], withoutIndexes(l.path)):
			case placeholderChanged && l.unknown:
			default:
				explained = false
			}
		}
		if explained {
			check.Details = append(check.Details, fmt.Sprintf("%s: %s (%s)", rc.Address, stateOnlyChange, strings.Join(change.Attributes, ", ")))
			continue
		}
		check.Passed = false
		check.Changes = append(check.Changes, change)
	}
	for _, c := range check.Changes {
		detail := fmt.Sprintf("%s: %s", c.Address, strings.Join(c.Actions, ", "))
		if len(c.Attributes) > 0 {
			detail += " (" + strings.Join(c.Attributes, ", ") + ")"
		}
		check.Details = append(check.Details, detail)
	}
	if expected >= 0 && importing != expected {
		check.Passed = false
		check.Details = append(check.Details, fmt.Sprintf("the plan imports %d resources, the configuration has %d import blocks", importing, expected))
	}
	return check
}

// stateOnlyOf returns the arguments stateOnly names for typ, and under "*"
// for every type, as the catalogue lists them: the Google provider keeps
// every resource's labels and terraform_labels in state only after an
// import, for example.
func stateOnlyOf(stateOnly map[string][]string, typ string) []string {
	if len(stateOnly["*"]) == 0 {
		return stateOnly[typ]
	}
	return append(slices.Clone(stateOnly["*"]), stateOnly[typ]...)
}

// leaf is one attribute of a resource a plan changes, by its path, such as
// user[0].password; unknown says its value is known only after apply.
type leaf struct {
	path    string
	unknown bool
}

// changedLeaves returns the attributes that differ between before and
// after, down to nested blocks, or that will only be known after apply,
// sorted by path.
func changedLeaves(before, after, afterUnknown any) []leaf {
	var leaves []leaf
	diffLeaves("", before, after, afterUnknown, &leaves)
	sort.Slice(leaves, func(i, j int) bool { return leaves[i].path < leaves[j].path })
	return leaves
}

func diffLeaves(path string, before, after, unknown any, out *[]leaf) {
	if u, ok := unknown.(bool); ok && u {
		*out = append(*out, leaf{path: path, unknown: true})
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
		for k := range bm {
			keys[k] = true
		}
		for k := range am {
			keys[k] = true
		}
		for k := range um {
			keys[k] = true
		}
		for k := range keys {
			sub := k
			if path != "" {
				sub = path + "." + k
			}
			diffLeaves(sub, bm[k], am[k], um[k], out)
		}
	case (blok || before == nil) && (alok || after == nil) && (blok || alok || ulok):
		for i := 0; i < max(len(bl), len(al), len(ul)); i++ {
			diffLeaves(fmt.Sprintf("%s[%d]", path, i), at(bl, i), at(al, i), at(ul, i), out)
		}
	}
	if len(*out) == n && !reflect.DeepEqual(before, after) {
		*out = append(*out, leaf{path: path})
	}
}

func at(list []any, i int) any {
	if i < len(list) {
		return list[i]
	}
	return nil
}

// topOf returns the top-level attribute of a path: user for
// user[0].password.
func topOf(path string) string {
	top, _, _ := strings.Cut(path, ".")
	top, _, _ = strings.Cut(top, "[")
	return top
}

// topLevel returns the top-level attributes of leaves, each once, sorted.
func topLevel(leaves []leaf) []string {
	var names []string
	for _, l := range leaves {
		names = append(names, topOf(l.path))
	}
	sort.Strings(names)
	return slices.Compact(names)
}

var indexes = regexp.MustCompile(`\[[^\]]*\]`)

// withoutIndexes drops the indexes from a path, so that the same attribute
// matches whichever element of a set it is in: user[0].password becomes
// user.password.
func withoutIndexes(path string) string {
	return indexes.ReplaceAllString(path, "")
}

// changedAttributes returns the top-level attributes that differ between
// before and after, or that will only be known after apply, sorted.
func changedAttributes(before, after, afterUnknown any) []string {
	return topLevel(changedLeaves(before, after, afterUnknown))
}

// showPlanWithSummary plans with vars into a plan file and returns it as
// JSON with its change summary, or the errors if the configuration doesn't
// plan. The plan file holds secret values: it lives in a private temporary
// directory, removed before showPlanWithSummary returns.
func showPlanWithSummary(ctx context.Context, tf Terraform, vars []tfexec.PlanOption) (*tfjson.Plan, *changeSummary, []tfjson.Diagnostic, error) {
	tmp, err := os.MkdirTemp("", "infraharvest-plan-")
	if err != nil {
		return nil, nil, nil, err
	}
	defer os.RemoveAll(tmp)
	planFile := filepath.Join(tmp, "plan")
	opts := append(append([]tfexec.PlanOption(nil), vars...), tfexec.Out(planFile))
	diags, summary, err := plan(ctx, tf, opts...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("terraform plan: %w", err)
	}
	if len(diags) > 0 || summary == nil {
		return nil, nil, diags, nil
	}
	// The directory is private already; this also keeps the file private
	// where the directory's permissions aren't inherited.
	if _, err := os.Stat(planFile); err == nil {
		_ = os.Chmod(planFile, 0o600)
	}
	p, err := tf.ShowPlanFile(ctx, planFile)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("terraform show: %w", err)
	}
	return p, summary, nil, nil
}
