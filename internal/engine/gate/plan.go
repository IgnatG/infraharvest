// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gate

import (
	"fmt"
	"slices"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// StateOnlyChange is what the plan check's details say of a resource that
// only updates state-only or secret arguments: the README tells people to
// expect those updates (see the engine's WriteReadme).
const StateOnlyChange = "only state-only or secret arguments change"

// PlanCheck passes if the plan imports expected resources and has no
// changes besides those imports, other than updates to the attributes
// secrets set, to what a secret's placeholder makes unknown, or to the
// stateOnly arguments of the resource's type. An expected below zero
// doesn't check the number of imports.
func PlanCheck(p *tfjson.Plan, diags []tfjson.Diagnostic, secrets []secret.Variable, stateOnly map[string][]string, expected int) Check {
	return PlanCheckWith(p, diags, secrets, stateOnly, expected, nil)
}

// goneChange is what the plan check's details say of a resource no longer
// in the cloud that the plan creates again (see PlanCheckWith).
const goneChange = "no longer in the cloud: the plan creates it again"

// PlanCheckWith is PlanCheck, where the plan may also create gone, the
// addresses of resources no longer in the cloud, whose import it leaves
// out (see the engine's MergeRoot.Gone).
func PlanCheckWith(p *tfjson.Plan, diags []tfjson.Diagnostic, secrets []secret.Variable, stateOnly map[string][]string, expected int, gone map[string]bool) Check {
	check := Check{Name: CheckPlan}
	if len(diags) > 0 || p == nil {
		for _, d := range diags {
			check.Details = append(check.Details, tfrun.FormatDiagnostic(d))
		}
		if len(diags) == 0 {
			check.Details = append(check.Details, "terraform plan reported no change summary")
		}
		return check
	}
	secretPaths := map[string][]string{}
	for _, s := range secrets {
		secretPaths[s.Address] = append(secretPaths[s.Address], tfrun.WithoutIndexes(s.Attribute))
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
		leaves := tfrun.ChangedLeaves(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown)
		change.Attributes = tfrun.TopLevel(leaves)
		// A placeholder secret value also makes what the provider computes
		// from it unknown, such as an SSM parameter's version.
		placeholderChanged := false
		for _, l := range leaves {
			if slices.Contains(secretPaths[rc.Address], tfrun.WithoutIndexes(l.Path)) {
				placeholderChanged = true
			}
		}
		explained := true
		for _, l := range leaves {
			switch {
			case slices.Contains(StateOnlyOf(stateOnly, rc.Type), tfrun.TopOf(l.Path)):
			case slices.Contains(secretPaths[rc.Address], tfrun.WithoutIndexes(l.Path)):
			case placeholderChanged && l.Unknown:
			default:
				explained = false
			}
		}
		if explained {
			check.Details = append(check.Details, fmt.Sprintf("%s: %s (%s)", rc.Address, StateOnlyChange, strings.Join(change.Attributes, ", ")))
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

// StateOnlyOf returns the arguments stateOnly names for typ, and under "*"
// for every type, as the catalogue lists them: the Google provider keeps
// every resource's labels and terraform_labels in state only after an
// import, for example.
func StateOnlyOf(stateOnly map[string][]string, typ string) []string {
	if len(stateOnly["*"]) == 0 {
		return stateOnly[typ]
	}
	return append(slices.Clone(stateOnly["*"]), stateOnly[typ]...)
}
