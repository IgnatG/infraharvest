// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package e2e

import (
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/IgnatG/infraharvest/internal/report"
)

// planBudget bounds the Terraform plans generating one directory may run:
// each plan imports, and so reads from the cloud, every resource of the
// root again, so a step that plans more than it did makes imports slower
// and dearer. Steps bounds the plans of each step by its name in
// report.json; a step it doesn't list may run none. Total bounds all of
// them, shows excluded, which read nothing from the cloud.
type planBudget struct {
	Steps map[string]int
	Total int
}

// rootPlanBudget is what a root the estates of the end-to-end tests
// generate may plan, with the headroom a few more plans need: the plans
// of an estate of these few resources, not the most any estate could
// need.
var rootPlanBudget = planBudget{
	Steps: map[string]int{
		// The plan that generates the configuration, and the one that
		// imports again what a resource's other import ID falls back on.
		"generate": 2,
		// The one plan after edits that come before anything reads the
		// configuration, such as sorting its resources.
		"prepare": 1,
		// Plans after repairing the configuration, once, and leaving out
		// what still has errors, for each of the three rounds of planning
		// until it is valid.
		"repair": 4,
		// The one import into a state snapshot (import-once, on by default;
		// INFRAHARVEST_IMPORT_ONCE=0 switches it off).
		"snapshot": 1,
		// The plans that make secret attributes read from variables.
		"secrets": 2,
		// The post-processing: its passes plan together where they can,
		// and again, once each, where a plan blames them for a change
		// (the references, data sources, lookups, defaults and tags).
		"tidy": 20,
		// The plans that lay out the root's files.
		"finish": 3,
		// The plans of the AWS --cfn-stack refinement, before the gate.
		"refine": 2,
		// The verification gate's plan, and one more for the gate that
		// plans again.
		"gate": 2,
	},
	Total: 30,
}

// mergedPlanBudget is what a root whose stack environments merge may
// plan: the root's, and the merged root's gate plans as the environment,
// once for each of the three rounds of the merge.
var mergedPlanBudget = planBudget{
	Steps: withStep(rootPlanBudget.Steps, "merge", 3),
	Total: rootPlanBudget.Total + 3,
}

// withStep returns steps with step's budget added.
func withStep(steps map[string]int, step string, plans int) map[string]int {
	steps = maps.Clone(steps)
	steps[step] = plans
	return steps
}

// planBudgetFor returns the budget of a directory: a directory with
// plans of the merge is of a stack environments merge.
func planBudgetFor(d report.Directory) planBudget {
	if slices.ContainsFunc(d.Plans, func(p report.Plans) bool { return p.Step == "merge" }) {
		return mergedPlanBudget
	}
	return rootPlanBudget
}

// overBudget returns what the plans of dir run over its budget: a step
// that ran more plans than it may, a step the budget doesn't know, and
// more plans in all than it may.
func overBudget(d report.Directory, b planBudget) []string {
	var over []string
	total := 0
	for _, p := range d.Plans {
		total += p.Plans
		limit, ok := b.Steps[p.Step]
		switch {
		case !ok && p.Plans > 0:
			over = append(over, fmt.Sprintf("step %q ran %d plans, which the budget has no step for", p.Step, p.Plans))
		case p.Plans > limit:
			over = append(over, fmt.Sprintf("step %q ran %d plans, at most %d", p.Step, p.Plans, limit))
		}
	}
	if total > b.Total {
		over = append(over, fmt.Sprintf("%d plans in all, at most %d", total, b.Total))
	}
	return over
}

// checkPlanBudget fails the test for each directory of the report that
// ran more plans than its budget allows.
func checkPlanBudget(t *testing.T, what string, coverage report.Report) {
	t.Helper()
	for _, d := range coverage.Directories {
		for _, o := range overBudget(d, planBudgetFor(d)) {
			t.Errorf("%s: %s: %s (plans %+v)", what, d.Path, o, d.Plans)
		}
	}
}

func TestOverBudget(t *testing.T) {
	t.Parallel()
	steps := func(ps ...report.Plans) report.Directory { return report.Directory{Path: "aws/x", Plans: ps} }
	for _, tc := range []struct {
		name string
		dir  report.Directory
		want int
	}{
		{"no plans recorded", steps(), 0},
		{"a typical root", steps(report.Plans{Step: "generate", Plans: 1}, report.Plans{Step: "tidy", Plans: 1, Shows: 1}, report.Plans{Step: "gate", Plans: 1, Shows: 1}), 0},
		{"a step at its budget", steps(report.Plans{Step: "generate", Plans: 2}), 0},
		{"a step over its budget", steps(report.Plans{Step: "generate", Plans: 3}), 1},
		{"a step the budget doesn't know", steps(report.Plans{Step: "extra", Plans: 1}), 1},
		{"a step that ran none and shows only", steps(report.Plans{Step: "extra", Shows: 2}), 0},
		{"a merge in a root", steps(report.Plans{Step: "merge", Plans: 1}), 0},
		{"more in all than the total", steps(report.Plans{Step: "generate", Plans: 2}, report.Plans{Step: "repair", Plans: 4}, report.Plans{Step: "tidy", Plans: 20}, report.Plans{Step: "finish", Plans: 3}, report.Plans{Step: "gate", Plans: 2}), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := overBudget(tc.dir, planBudgetFor(tc.dir)); len(got) != tc.want {
				t.Errorf("got %v, want %d findings", got, tc.want)
			}
		})
	}
	merged := steps(report.Plans{Step: "generate", Plans: 1}, report.Plans{Step: "merge", Plans: 3})
	if got := overBudget(merged, planBudgetFor(merged)); len(got) != 0 {
		t.Errorf("a merged root within its budget: %v", got)
	}
	merged.Plans[1].Plans = 4
	if got := overBudget(merged, planBudgetFor(merged)); len(got) != 1 {
		t.Errorf("a merge over its rounds: %v", got)
	}
}
