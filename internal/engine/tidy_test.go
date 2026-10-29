// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// tidier returns a driver of the root of ws for a test, comparing with
// baseline.
func tidier(tf tfrun.Terraform, ws *workspace, baseline Baseline) *tidying {
	return &tidying{ctx: context.Background(), tf: ws.terraform(tf), ws: ws, r: generating(Options{}), baseline: baseline}
}

// declared returns postProcess's pass called name, as the driver would run
// it, with the edit e instead of the one the pass starts, if e isn't nil.
func declared(t *testing.T, d *tidying, name string) tidyPass {
	t.Helper()
	p, err := declaredPass(d, name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// declaredPass is declared, for a helper without a test: it returns an error
// if postProcess has no pass called name.
func declaredPass(d *tidying, name string) (tidyPass, error) {
	for _, p := range d.passes() {
		if p.name == name {
			return p, nil
		}
	}
	return tidyPass{}, errors.New("no " + name + " pass")
}

// settleEdit settles e as pass name would be, with that pass's rounds and
// policy.
func settleEdit(t *testing.T, d *tidying, name string, e *tidyEdit) (bool, error) {
	t.Helper()
	return d.settle(declared(t, d, name), e)
}

// settleDefaults runs the defaults pass over generated.tf in dir, against a
// baseline of changes, and returns whether it kept an edit and the driver,
// whose baseline is the plan the pass settled on. The files are in dir as it
// left them.
func settleDefaults(tf tfrun.Terraform, dir string, changes map[string]string, rules defaultRules) (bool, *tidying, error) {
	ws, err := openWorkspace(dir)
	if err != nil {
		return false, nil, err
	}
	d := tidier(tf, ws, Baseline{changes: changes})
	e, err := stripDefaults(d.ctx, d.tf, ws, rules)
	if err != nil {
		return false, d, err
	}
	p, err := declaredPass(d, passDefaults)
	if err != nil {
		return false, d, err
	}
	kept, err := d.settle(p, e)
	return kept, d, errors.Join(err, ws.Flush())
}

// synthesizeRoot runs the curated modules pass over generated.tf in dir, and
// returns the clusters it declined, the baseline it rebased on, and the list
// lengths of its calls. The files are in dir as it left them.
func synthesizeRoot(tf tfrun.Terraform, dir string, opts Options, baseline Baseline) ([]ModuleCall, Baseline, map[string]int, error) {
	ws, err := openWorkspace(dir)
	if err != nil {
		return nil, Baseline{}, nil, err
	}
	d := tidier(tf, ws, baseline)
	d.r = generating(opts)
	e, err := d.synthesize()
	if err == nil && e != nil {
		var p tidyPass
		if p, err = declaredPass(d, passCuratedModules); err == nil {
			_, err = d.settle(p, e)
		}
	}
	return d.declined, d.baseline, d.lists, errors.Join(err, ws.Flush())
}

// An edit that fails after changing a file leaves nothing of it behind: the
// driver restores every file of the workspace, not those the edit names.
func TestSettleRestoresWhenTheEditFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
	ws := rootWorkspace(t, dir)
	tf := &fakeTerraform{dir: dir}
	failure := errors.New("halfway")
	e := &tidyEdit{edit: func(*tidyAttempt) (edited, error) {
		ws.Write(rootfile.GeneratedFileName, []byte("# changed\n"))
		ws.Write(rootfile.LocalsFileName, []byte("locals {}\n"))
		return edited{other: true}, failure
	}}

	kept, err := tidier(tf, ws, Baseline{}).settle(tidyPass{}, e)
	if kept || !errors.Is(err, failure) {
		t.Fatalf("got kept=%v err=%v, want the edit's error", kept, err)
	}

	if got := wsFile(t, ws, rootfile.GeneratedFileName); got != networkGenerated {
		t.Errorf("generated.tf not restored:\n%s", got)
	}
	if _, ok := ws.files[rootfile.LocalsFileName]; ok {
		t.Error("locals.tf not removed")
	}
	if len(tf.calls) != 0 {
		t.Errorf("planned after a failed edit: %v", tf.calls)
	}
}

// An edit is kept only if each resource plans the change it had, not only
// the same totals: one that hides another change in a resource that
// already updates, such as its labels after an import, is undone.
func TestSettleComparesEachResourcesChange(t *testing.T) {
	t.Parallel()
	baseline := tfrun.ChangeSummary{Change: 1}
	updatesTags := resourceChange("aws_subnet.a", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"tags": map[string]any{}}, map[string]any{"tags": map[string]any{"a": "b"}})
	changes := map[string]string{"aws_subnet.a": tfrun.ChangeSignature(updatesTags)}
	for name, tt := range map[string]struct {
		planned *tfjson.ResourceChange
		kept    bool
	}{
		"the same change": {updatesTags, true},
		"another change too": {resourceChange("aws_subnet.a", tfjson.Actions{tfjson.ActionUpdate},
			map[string]any{"tags": map[string]any{}, "cidr_block": "10.0.1.0/24"},
			map[string]any{"tags": map[string]any{"a": "b"}, "cidr_block": "10.0.9.0/24"}), false},
	} {
		dir := t.TempDir()
		writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
		ws := rootWorkspace(t, dir)
		tf := &fakeTerraform{dir: dir, plans: []fakePlan{{summary: baseline}}, showns: []*tfjson.Plan{{ResourceChanges: []*tfjson.ResourceChange{tt.planned}}}}

		kept, err := tidier(tf, ws, Baseline{summary: baseline, changes: changes}).settle(tidyPass{}, commentEdit(ws, "edited"))
		if err != nil || kept != tt.kept {
			t.Errorf("%s: got kept=%v err=%v, want kept=%v", name, kept, err, tt.kept)
		}
		if restored := wsFile(t, ws, rootfile.GeneratedFileName) == networkGenerated; restored == tt.kept {
			t.Errorf("%s: generated.tf restored=%v", name, restored)
		}
	}
}

// An edit that changes nothing is neither planned nor undone, and its other
// way is tried instead.
func TestSettleTriesTheOtherEditWhereNothingChanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
	ws := rootWorkspace(t, dir)
	tf := &fakeTerraform{dir: dir}
	e := &tidyEdit{edit: plainEdit(func() (bool, error) { return false, nil }), otherwise: func() (bool, error) { return appendComment(ws, "other") }}

	kept, err := tidier(tf, ws, Baseline{}).settle(tidyPass{}, e)

	if err != nil || !kept {
		t.Fatalf("got kept=%v err=%v, want the other edit kept", kept, err)
	}
	if got := wsFile(t, ws, rootfile.GeneratedFileName); got != networkGenerated+"# other\n" {
		t.Errorf("generated.tf:\n%s", got)
	}
	if got := strings.Join(tf.calls, ","); got != "plan,show" {
		t.Errorf("calls: %s, want one plan, of the other edit", got)
	}
}

// Where a plan rejects the edit, the other edit is tried on the files as they
// were, and judged the same way.
func TestSettleTriesTheOtherEditWhereThePlanRejects(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
	ws := rootWorkspace(t, dir)
	changed := fakePlan{summary: tfrun.ChangeSummary{Change: 1}}
	tf := &fakeTerraform{dir: dir, plans: []fakePlan{changed, {}}}
	e := &tidyEdit{edit: commentEdit(ws, "first").edit, otherwise: func() (bool, error) { return appendComment(ws, "second") }}

	kept, err := tidier(tf, ws, Baseline{}).settle(tidyPass{}, e)

	if err != nil || !kept {
		t.Fatalf("got kept=%v err=%v, want the other edit kept", kept, err)
	}
	if got := wsFile(t, ws, rootfile.GeneratedFileName); got != networkGenerated+"# second\n" {
		t.Errorf("generated.tf:\n%s", got)
	}
}

// resourceNames is a resource edit that appends a comment to generated.tf
// for each address it is given, but those skip has at a higher level than
// 0, and reports them.
func resourceNames(ws *workspace, addresses ...string) *tidyEdit {
	return &tidyEdit{edit: resourceEdit(func(skip map[string]int) (map[string]bool, error) {
		edited := map[string]bool{}
		for _, a := range addresses {
			if skip[a] > 0 {
				continue
			}
			if _, err := appendComment(ws, a); err != nil {
				return nil, err
			}
			edited[a] = true
		}
		return edited, nil
	})}
}

// A plan that differs for a resource the edit changed has the edit made again,
// on the files as they were, with less of it for that resource; one that
// differs for a resource it didn't change, or still differs, undoes the edit.
func TestSettleEditsAgainWithoutWhatThePlanBlames(t *testing.T) {
	t.Parallel()
	same := func(address string) *tfjson.ResourceChange {
		return resourceChange(address, tfjson.Actions{tfjson.ActionNoop}, nil, nil)
	}
	updates := func(address string) *tfjson.ResourceChange {
		return resourceChange(address, tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"a": 1}, map[string]any{"a": 2})
	}
	changes := map[string]string{"aws_subnet.a": tfrun.ChangeSignature(same("x")), "aws_subnet.b": tfrun.ChangeSignature(same("x")), "aws_subnet.c": tfrun.ChangeSignature(same("x"))}
	plan := func(rcs ...*tfjson.ResourceChange) *tfjson.Plan { return &tfjson.Plan{ResourceChanges: rcs} }
	for name, tt := range map[string]struct {
		plans []*tfjson.Plan
		kept  bool
		want  string // the comments left
		calls string
	}{
		"settles after leaving one out": {
			[]*tfjson.Plan{plan(updates("aws_subnet.a"), same("aws_subnet.b"), same("aws_subnet.c")), plan(same("aws_subnet.a"), same("aws_subnet.b"), same("aws_subnet.c"))},
			true, "# aws_subnet.b\n# aws_subnet.c\n", "plan,show,plan,show",
		},
		"nothing left to blame": {
			// The plan differs for a resource the edit didn't change.
			[]*tfjson.Plan{plan(same("aws_subnet.a"), same("aws_subnet.b"), same("aws_subnet.c"), updates("aws_subnet.d"))},
			false, "", "plan,show",
		},
		"still differs after the rounds": {
			[]*tfjson.Plan{
				plan(updates("aws_subnet.a"), same("aws_subnet.b"), same("aws_subnet.c")),
				plan(same("aws_subnet.a"), updates("aws_subnet.b"), same("aws_subnet.c")),
				plan(same("aws_subnet.a"), same("aws_subnet.b"), updates("aws_subnet.c")),
			},
			false, "", "plan,show,plan,show,plan,show",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
			ws := rootWorkspace(t, dir)
			// The totals of the plans are those of the resources that update.
			plans := make([]fakePlan, len(tt.plans))
			for i, p := range tt.plans {
				for _, rc := range p.ResourceChanges {
					if slices.Contains(rc.Change.Actions, tfjson.ActionUpdate) {
						plans[i].summary.Change++
					}
				}
			}
			tf := &fakeTerraform{dir: dir, plans: plans, showns: tt.plans}
			d := tidier(tf, ws, Baseline{changes: changes})
			pass := tidyPass{rounds: maxEditRounds}

			kept, err := d.settle(pass, resourceNames(ws, "aws_subnet.a", "aws_subnet.b", "aws_subnet.c"))

			if err != nil || kept != tt.kept {
				t.Fatalf("got kept=%v err=%v, want kept=%v", kept, err, tt.kept)
			}
			want := networkGenerated
			if tt.kept {
				want += tt.want
			}
			if got := wsFile(t, ws, rootfile.GeneratedFileName); got != want {
				t.Errorf("generated.tf:\n%s\nwant:\n%s", got, want)
			}
			if got := strings.Join(tf.calls, ","); got != tt.calls {
				t.Errorf("calls: got %s, want %s", got, tt.calls)
			}
		})
	}
}

// A review's own outcomes: it retries until the rounds end, falls back to the
// pass's unplanned edit, and rebases onto the plan it accepts.
func TestSettleFollowsTheReview(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		outcomes []tidyOutcome
		rounds   int
		kept     bool
		want     string // the files, after the comments
		calls    string
	}{
		"accepts the first plan":     {[]tidyOutcome{tidyAccept}, 3, true, "# edit 0\n", "plan,show"},
		"retries, then accepts":      {[]tidyOutcome{tidyRetry, tidyAccept}, 3, true, "# edit 1\n", "plan,show,plan,show"},
		"rejects":                    {[]tidyOutcome{tidyReject}, 3, false, "", "plan,show"},
		"falls back":                 {[]tidyOutcome{tidyFallBack}, 3, true, "# unplanned\n", "plan,show"},
		"retries out of rounds":      {[]tidyOutcome{tidyRetry, tidyRetry}, 2, true, "# unplanned\n", "plan,show,plan,show"},
		"rejects without fallback":   {[]tidyOutcome{tidyRetry, tidyReject}, 3, false, "", "plan,show,plan,show"},
		"retries just once in a row": {[]tidyOutcome{tidyRetry}, 1, true, "# unplanned\n", "plan,show"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
			ws := rootWorkspace(t, dir)
			tf := &fakeTerraform{dir: dir}
			e := &tidyEdit{
				edit: func(a *tidyAttempt) (edited, error) {
					_, err := appendComment(ws, "edit "+string(rune('0'+a.round)))
					return edited{other: true}, err
				},
				review: func(a *tidyAttempt, _ tidyReview) (tidyDecision, error) {
					return tidyDecision{outcome: tt.outcomes[a.round]}, nil
				},
			}
			if name != "rejects without fallback" {
				e.unplanned = func(*tidyAttempt) (bool, error) {
					ws.Write(rootfile.GeneratedFileName, []byte(networkGenerated+"# unplanned\n"))
					return true, nil
				}
			}

			kept, err := tidier(tf, ws, Baseline{}).settle(tidyPass{rounds: tt.rounds}, e)

			if err != nil || kept != tt.kept {
				t.Fatalf("got kept=%v err=%v, want kept=%v", kept, err, tt.kept)
			}
			if got, want := wsFile(t, ws, rootfile.GeneratedFileName), networkGenerated+tt.want; got != want {
				t.Errorf("generated.tf:\n%s\nwant:\n%s", got, want)
			}
			if got := strings.Join(tf.calls, ","); got != tt.calls {
				t.Errorf("calls: got %s, want %s", got, tt.calls)
			}
		})
	}
}

// A pass that rebases hands the passes after it the plan it accepted, or, where
// it settled with no plan to take, a new one; one that doesn't leaves the
// baseline as it was.
func TestSettleRebasesOnWhatItSettledOn(t *testing.T) {
	t.Parallel()
	moved := resourceChange("module.a.aws_subnet.this", tfjson.Actions{tfjson.ActionNoop}, nil, nil)
	accepted := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{moved}}
	acceptsOwnPlan := func(ws *workspace) *tidyEdit {
		e := commentEdit(ws, "edit")
		e.review = func(*tidyAttempt, tidyReview) (tidyDecision, error) {
			return tidyDecision{outcome: tidyAccept, plan: &shownPlan{accepted, &tfrun.ChangeSummary{Import: 7}}}, nil
		}
		return e
	}
	for name, tt := range map[string]struct {
		rebases bool
		edit    func(ws *workspace) *tidyEdit
		address string // what the baseline has afterwards
		imports int
		calls   string
	}{
		"onto the plan it accepted": {true, acceptsOwnPlan, "module.a.aws_subnet.this", 7, "plan,show"},
		"not at all":                {false, acceptsOwnPlan, "aws_subnet.a", 0, "plan,show"},
		"onto a new plan, with none": {true, func(ws *workspace) *tidyEdit {
			e := commentEdit(ws, "x")
			e.review = func(*tidyAttempt, tidyReview) (tidyDecision, error) { return tidyDecision{outcome: tidyFallBack}, nil }
			e.unplanned = func(*tidyAttempt) (bool, error) { return true, nil }
			return e
		}, "aws_subnet.a", 0, "plan,show,plan,show"},
		"onto the plan it was shown": {true, func(ws *workspace) *tidyEdit {
			e := commentEdit(ws, "x")
			e.review = func(*tidyAttempt, tidyReview) (tidyDecision, error) { return tidyDecision{outcome: tidyAccept}, nil }
			return e
		}, "aws_subnet.a", 0, "plan,show"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
			ws := rootWorkspace(t, dir)
			shown := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{resourceChange("aws_subnet.a", tfjson.Actions{tfjson.ActionNoop}, nil, nil)}}
			tf := &fakeTerraform{dir: dir, shown: shown, showns: []*tfjson.Plan{shown}}
			d := tidier(tf, ws, Baseline{changes: map[string]string{"aws_subnet.a": "no-op:"}})

			kept, err := d.settle(tidyPass{rebases: tt.rebases, stopped: "stopped"}, tt.edit(ws))

			if err != nil || !kept {
				t.Fatalf("got kept=%v err=%v", kept, err)
			}
			if d.baseline.Planned(tt.address) == "" {
				t.Errorf("the baseline lacks %s: %v", tt.address, d.baseline.changes)
			}
			if d.baseline.summary.Import != tt.imports {
				t.Errorf("imports: got %d, want %d", d.baseline.summary.Import, tt.imports)
			}
			if got := strings.Join(tf.calls, ","); got != tt.calls {
				t.Errorf("calls: got %s, want %s", got, tt.calls)
			}
		})
	}
}

// What a pass that changes the plan moved is compared by its new address
// afterwards, by the change it plans there and not only the totals: the
// passes after it undo an edit that changes a resource moved into a module.
func TestRunComparesWithTheBaselineARebasingPassLeft(t *testing.T) {
	t.Parallel()
	moved := func(attrs map[string]any) *tfjson.ResourceChange {
		return resourceChange("module.a.aws_subnet.this", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"tags": nil}, attrs)
	}
	for name, tt := range map[string]struct {
		after map[string]any
		kept  bool
	}{
		"the same change":    {map[string]any{"tags": "x"}, true},
		"another change too": {map[string]any{"tags": "x", "cidr_block": "10.0.9.0/24"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
			ws := rootWorkspace(t, dir)
			summary := tfrun.ChangeSummary{Change: 1}
			tf := &fakeTerraform{dir: dir,
				plans:  []fakePlan{{summary: summary}, {summary: summary}},
				showns: []*tfjson.Plan{{ResourceChanges: []*tfjson.ResourceChange{moved(map[string]any{"tags": "x"})}}, {ResourceChanges: []*tfjson.ResourceChange{moved(tt.after)}}}}
			passes := []tidyPass{
				{name: "moves", alone: true, rebases: true, stopped: "stopped", start: func() (*tidyEdit, error) { return commentEdit(ws, "moved"), nil }},
				{name: "after", after: []string{"moves"}, start: func() (*tidyEdit, error) { return commentEdit(ws, "after"), nil }},
			}
			// The baseline before the move has the resource at its old address.
			stale := Baseline{summary: summary, changes: map[string]string{"aws_subnet.a": "update:tags"}}

			d := tidier(tf, ws, stale)
			if err := d.run(passes); err != nil {
				t.Fatal(err)
			}

			want := networkGenerated + "# moved\n"
			if tt.kept {
				want += "# after\n"
			}
			if got := wsFile(t, ws, rootfile.GeneratedFileName); got != want {
				t.Errorf("generated.tf:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// A pass that rebases on a plan it must take, and finds the configuration no
// longer plans, fails with its own reason.
func TestSettleFailsWhereARebasingPassLeavesNothingToPlan(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
	ws := rootWorkspace(t, dir)
	tf := &fakeTerraform{dir: dir, plans: []fakePlan{{}, {diags: []tfjson.Diagnostic{{Severity: tfjson.DiagnosticSeverityError, Summary: "broken"}}}}}
	e := commentEdit(ws, "edit")
	e.unplanned = func(*tidyAttempt) (bool, error) { return true, nil }
	e.review = func(*tidyAttempt, tidyReview) (tidyDecision, error) { return tidyDecision{outcome: tidyFallBack}, nil }

	_, err := tidier(tf, ws, Baseline{}).settle(tidyPass{rebases: true, stopped: "the configuration stopped planning"}, e)

	if err == nil || err.Error() != "the configuration stopped planning" {
		t.Errorf("got %v, want the pass's reason", err)
	}
}

// One plan checks the edits of passes that keep it as it was; only when it
// differs are they checked one at a time, so that the pass the plan rejects is
// the only one undone.
func TestRunChecksPassesNextToEachOtherTogether(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		plans []fakePlan
		calls string
		want  []string // the comments kept
	}{
		"together":          {nil, "plan,show", []string{"one", "two"}},
		"one at a time":     {[]fakePlan{{summary: tfrun.ChangeSummary{Change: 1}}, {summary: tfrun.ChangeSummary{Change: 1}}}, "plan,show,plan,show,plan,show", []string{"two"}},
		"none of them kept": {[]fakePlan{{summary: tfrun.ChangeSummary{Change: 1}}, {summary: tfrun.ChangeSummary{Change: 1}}, {summary: tfrun.ChangeSummary{Change: 1}}}, "plan,show,plan,show,plan,show", nil},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
			ws := rootWorkspace(t, dir)
			tf := &fakeTerraform{dir: dir, plans: tt.plans}
			passes := []tidyPass{
				{name: "one", start: func() (*tidyEdit, error) { return commentEdit(ws, "one"), nil }},
				{name: "two", start: func() (*tidyEdit, error) { return commentEdit(ws, "two"), nil }},
			}

			if err := tidier(tf, ws, Baseline{}).run(passes); err != nil {
				t.Fatal(err)
			}

			if got := strings.Join(tf.calls, ","); got != tt.calls {
				t.Errorf("calls: got %s, want %s", got, tt.calls)
			}
			want := networkGenerated
			for _, c := range tt.want {
				want += "# " + c + "\n"
			}
			if got := wsFile(t, ws, rootfile.GeneratedFileName); got != want {
				t.Errorf("generated.tf: got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// Passes that change nothing need no plan, and one that isn't made by the
// run is skipped.
func TestRunWithoutChanges(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
	ws := rootWorkspace(t, dir)
	tf := &fakeTerraform{dir: dir}
	passes := []tidyPass{
		{name: "unchanged", start: func() (*tidyEdit, error) {
			return &tidyEdit{edit: plainEdit(func() (bool, error) { return false, nil })}, nil
		}},
		{name: "none", start: func() (*tidyEdit, error) {
			return &tidyEdit{edit: resourceEdit(func(map[string]int) (map[string]bool, error) { return nil, nil })}, nil
		}},
		{name: "not made", start: func() (*tidyEdit, error) { return nil, nil }},
	}

	if err := tidier(tf, ws, Baseline{}).run(passes); err != nil {
		t.Fatal(err)
	}

	if len(tf.calls) != 0 {
		t.Errorf("calls: %v, want none", tf.calls)
	}
	if got := wsFile(t, ws, rootfile.GeneratedFileName); got != networkGenerated {
		t.Errorf("generated.tf changed:\n%s", got)
	}
}

// A pass that plans on its own is a barrier: the passes before it are
// checked, and kept, before it plans, so that it compares with what they left.
func TestRunChecksThePassesBeforeOneThatPlansAlone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
	ws := rootWorkspace(t, dir)
	tf := &fakeTerraform{dir: dir}
	var seen string
	passes := []tidyPass{
		{name: "before", start: func() (*tidyEdit, error) { return commentEdit(ws, "before"), nil }},
		{name: "alone", alone: true, start: func() (*tidyEdit, error) {
			seen = wsFile(t, ws, rootfile.GeneratedFileName)
			return nil, nil
		}},
		{name: "after", start: func() (*tidyEdit, error) { return commentEdit(ws, "after"), nil }},
	}

	if err := tidier(tf, ws, Baseline{}).run(passes); err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(seen, "# before\n") || strings.Contains(seen, "# after") {
		t.Errorf("the pass that plans alone saw:\n%s", seen)
	}
	if got := strings.Join(tf.calls, ","); got != "plan,show,plan,show" {
		t.Errorf("calls: got %s, want a plan for the pass before and one for the pass after", got)
	}
}

// postProcess runs its passes in this order, every one after the passes it
// names, each of which is one of postProcess's.
func TestPassesRunAfterTheirPrerequisites(t *testing.T) {
	t.Parallel()
	passes := (&tidying{r: generating(Options{})}).passes()

	ordered, err := orderPasses(passes)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{passEffectiveTags, passDefaults, passReferences, passDataSources, passRootVariables, passLookups, passTagLift, passLiterals, passCuratedModules, passOutputLists, passProjects, passInherited, passLocalModules}
	var got, declaredOrder []string
	for _, p := range ordered {
		got = append(got, p.name)
	}
	for _, p := range passes {
		declaredOrder = append(declaredOrder, p.name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("passes:\ngot  %q\nwant %q", got, want)
	}
	if !reflect.DeepEqual(declaredOrder, want) {
		t.Errorf("declared in an order other than the one they run in:\ngot  %q\nwant %q", declaredOrder, want)
	}
}

// The passes that change the plan the passes after compare with, or judge it
// less than exactly, plan on their own.
func TestPassesThatChangeThePlanPlanAlone(t *testing.T) {
	t.Parallel()
	alone := map[string]bool{passEffectiveTags: true, passDefaults: true, passCuratedModules: true, passLocalModules: true}
	for _, p := range (&tidying{r: generating(Options{})}).passes() {
		if p.alone != alone[p.name] {
			t.Errorf("%s: alone=%v, want %v", p.name, p.alone, alone[p.name])
		}
	}
}

// The order the passes declare wins over the order they are listed in; a pass
// that names one there isn't, a cycle, and a pass that must plan alone and
// doesn't say so, are errors.
func TestOrderPasses(t *testing.T) {
	t.Parallel()
	tags := tidyPass{name: passTagLift}
	literals := tidyPass{name: passLiterals, after: []string{passTagLift}}
	names := func(passes []tidyPass) []string {
		var got []string
		for _, p := range passes {
			got = append(got, p.name)
		}
		return got
	}

	for name, tt := range map[string]struct {
		passes []tidyPass
		want   []string
	}{
		"in order":                          {[]tidyPass{tags, literals}, []string{passTagLift, passLiterals}},
		"listed out of order":               {[]tidyPass{literals, tags}, []string{passTagLift, passLiterals}},
		"unrelated passes keep their order": {[]tidyPass{{name: "b"}, {name: "a"}}, []string{"b", "a"}},
	} {
		got, err := orderPasses(tt.passes)
		if err != nil || !slices.Equal(names(got), tt.want) {
			t.Errorf("%s: got %v, %v, want %v", name, names(got), err, tt.want)
		}
	}
	for name, passes := range map[string][]tidyPass{
		"a pass there isn't": {literals},
		"a cycle":            {{name: "a", after: []string{"b"}}, {name: "b", after: []string{"a"}}},
		"a rebasing pass that doesn't plan alone":        {{name: "a", rebases: true}},
		"a pass that isn't exact and doesn't plan alone": {{name: "a", policy: FewerChanges}},
	} {
		if _, err := orderPasses(passes); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// settleLocalModules runs the local modules pass over dir, with modulesDir and
// refs, against baseline, and leaves the files in dir as it settled them.
func settleLocalModules(t *testing.T, tf tfrun.Terraform, dir, modulesDir string, baseline Baseline) (bool, error) {
	t.Helper()
	ws := rootWorkspace(t, dir)
	d := tidier(tf, ws, baseline)
	kept, err := settleEdit(t, d, passLocalModules, liftModules(d.ctx, d.tf, ws, modulesDir, &ModuleRefs{}))
	flushed(t, ws)
	return kept, err
}

// A rebase takes the imported values of the root's resources from the plan,
// but not those of a resource a pass moved into a module, which keeps the
// values of its root address.
func TestRebaseKeepsTheRootsValues(t *testing.T) {
	d := tidier(nil, memoryWorkspace(nil), Baseline{})
	d.values = map[string]map[string]any{
		"aws_s3_bucket.logs": {"bucket": "logs"},
		"aws_sqs_queue.jobs": {"name": "old"},
	}
	summary := tfrun.ChangeSummary{Import: 2}
	plan := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		resourceChange("module.logs.aws_s3_bucket.this", tfjson.Actions{tfjson.ActionNoop}, map[string]any{"bucket": "moved"}, nil),
		resourceChange("aws_sqs_queue.jobs", tfjson.Actions{tfjson.ActionNoop}, map[string]any{"name": "jobs"}, nil),
	}}
	if err := d.rebase(&shownPlan{plan, &summary}, "stopped"); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]any{
		"aws_s3_bucket.logs": {"bucket": "logs"},
		"aws_sqs_queue.jobs": {"name": "jobs"},
	}
	if !reflect.DeepEqual(d.values, want) {
		t.Errorf("values: got %v, want %v", d.values, want)
	}
	if got := d.baseline.Planned("module.logs.aws_s3_bucket.this"); got == "" {
		t.Error("the baseline has no change for the moved resource")
	}
}
