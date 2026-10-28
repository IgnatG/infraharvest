// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"slices"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// Baseline is the plan postProcess's passes compare their edits with: the
// totals of its change summary, the change each resource plans (see
// tfrun.ChangeSignature), and the arguments a resource of each type may
// update in state without that being a difference (Options.StateOnly). It is
// what a plan must still look like after an edit, as Policy says. A pass
// that changes what the plan looks like, such as one that moves resources
// into modules, hands the plan it settled on to Rebase, so that the passes
// after it compare with that.
type Baseline struct {
	summary tfrun.ChangeSummary
	// changes are the managed resources' planned changes, by address.
	changes   map[string]string
	stateOnly map[string][]string
}

// Policy says how closely a plan must match a Baseline.
type Policy int

const (
	// Exact is for an edit that must change nothing the plan shows: the same
	// totals, and every resource the baseline has planning the change it
	// had, the same actions on the same attributes. Totals alone would let
	// an edit hide a real change in a resource that already has an expected
	// update, such as a label-only one after an import.
	Exact Policy = iota
	// FewerChanges is for an edit that may only remove changes, such as
	// leaving out a provider-side setting an import left unset: the same
	// adds, removes and imports, no more updates, and no resource planning
	// anything it didn't before (see noWorse).
	FewerChanges
	// StateOnly is for an edit that may also update, in state, arguments the
	// baseline's state-only rules name for the type, such as the labels a
	// module sets that an import can't: as Exact, but a resource may plan
	// those, and an import without changes becomes an update by them (see
	// sameButStateOnly), which the totals count.
	StateOnly
)

// Verdict is how a plan compares with a Baseline.
type Verdict struct {
	// Failed says the plan had errors, or no result: there is nothing to
	// compare.
	Failed bool
	// Totals says the plan's change summary is what the policy allows.
	Totals bool
	// Changed are the managed resources the baseline has that plan a change
	// the policy doesn't allow.
	Changed []*tfjson.ResourceChange
	// Updates counts the resources that plan an update where the baseline
	// has none, which the policy allows (see StateOnly).
	Updates int
}

// Accepted reports whether the plan is what the policy allows: it has a
// result, with the totals and every resource's change as the policy requires.
func (v Verdict) Accepted() bool {
	return !v.Failed && v.Totals && len(v.Changed) == 0
}

// newBaseline returns the baseline of p, a plan with a change summary.
func newBaseline(p shownPlan, stateOnly map[string][]string) Baseline {
	return Baseline{stateOnly: stateOnly}.Rebase(p)
}

// Rebase returns the baseline of p, a plan with a change summary, with the
// rules b has: its totals and the change of every resource it plans,
// wherever the resource is, so that a resource moved into a module is
// compared by its module address, as the plan has it.
func (b Baseline) Rebase(p shownPlan) Baseline {
	rebased := Baseline{summary: *p.summary, changes: map[string]string{}, stateOnly: b.stateOnly}
	for _, rc := range p.plan.ResourceChanges {
		if rc.Mode == tfjson.ManagedResourceMode && rc.Change != nil {
			rebased.changes[rc.Address] = tfrun.ChangeSignature(rc)
		}
	}
	return rebased
}

// Judge compares plan, which Terraform showed with diags as its errors, with
// the baseline, as policy allows. A resource the baseline doesn't have, such
// as one moved into a module call since, is judged by the totals alone.
func (b Baseline) Judge(plan shownPlan, diags []tfjson.Diagnostic, policy Policy) Verdict {
	if len(diags) > 0 || plan.plan == nil || plan.summary == nil {
		return Verdict{Failed: true}
	}
	var v Verdict
	for _, rc := range plan.plan.ResourceChanges {
		if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil {
			continue
		}
		if _, ok := b.changes[rc.Address]; !ok {
			continue
		}
		tolerated, update := b.Tolerates(rc, rc.Address, policy)
		switch {
		case !tolerated:
			v.Changed = append(v.Changed, rc)
		case update:
			v.Updates++
		}
	}
	got := *plan.summary
	switch policy {
	case FewerChanges:
		v.Totals = got.Add == b.summary.Add && got.Remove == b.summary.Remove && got.Import == b.summary.Import && got.Change <= b.summary.Change
	case StateOnly:
		v.Totals = got == b.Expected(0, v.Updates)
	default:
		v.Totals = got == b.summary
	}
	return v
}

// Tolerates reports whether rc, a change a plan has, is one policy allows for
// the resource the baseline has at address, which differs from rc.Address
// where the plan has the resource elsewhere, as in a module call. A baseline
// without address has no change for it, which only the same, empty change
// matches. update says the change is an update that policy tolerates where
// the baseline has none.
func (b Baseline) Tolerates(rc *tfjson.ResourceChange, address string, policy Policy) (tolerated, update bool) {
	signature, want := tfrun.ChangeSignature(rc), b.changes[address]
	if signature == want {
		return true, false
	}
	switch policy {
	case FewerChanges:
		return noWorse(signature, want), false
	case StateOnly:
		return sameButStateOnly(signature, want, gate.StateOnlyOf(b.stateOnly, rc.Type))
	}
	return false, false
}

// Planned returns the change signature the baseline has for the resource at
// address (see tfrun.ChangeSignature), or "" if it has none.
func (b Baseline) Planned(address string) string {
	return b.changes[address]
}

// Expected returns the baseline's totals with imports more imports, such as
// those the module calls add, and updates more updates, such as those a
// StateOnly policy tolerates.
func (b Baseline) Expected(imports, updates int) tfrun.ChangeSummary {
	expected := b.summary
	expected.Import += imports
	expected.Change += updates
	return expected
}

// sameButStateOnly reports whether the change signatures in and root
// differ only in arguments of stateOnly, which import can't set and a
// module may, and whether that makes an import without changes in root an
// update in.
func sameButStateOnly(in, root string, stateOnly []string) (same, updates bool) {
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
		return true, false
	case inActions == update && rootActions == noOp:
		return true, true
	}
	return false, false
}

// noWorse reports whether a resource's planned change, now, does no more
// than it did before: the same actions, and no attribute that didn't
// change before. Leaving out an argument may only remove changes, such as
// a provider-side setting the import left unset.
func noWorse(now, before string) bool {
	if now == before {
		return true
	}
	nowActions, nowAttributes, _ := strings.Cut(now, ":")
	beforeActions, beforeAttributes, _ := strings.Cut(before, ":")
	if nowActions != beforeActions && nowActions != string(tfjson.ActionNoop) {
		return false
	}
	before2 := strings.Split(beforeAttributes, ",")
	for _, a := range strings.Split(nowAttributes, ",") {
		if a != "" && !slices.Contains(before2, a) {
			return false
		}
	}
	return true
}
