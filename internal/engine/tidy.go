// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// tidyFiles are the files of a root that postProcess's passes may change.
// The driver backs up all of them before a pass and puts them back where it
// undoes the pass, so that a pass can't change one and leave it behind: a
// pass that writes another file of the root adds it here.
var tidyFiles = []string{
	rootfile.GeneratedFileName,
	rootfile.ImportsFileName,
	rootfile.ProvidersFileName,
	rootfile.VersionsFileName,
	rootfile.LocalsFileName,
	rootfile.DataFileName,
	hclfile.VariablesFileName,
}

// maxEditRounds bounds how often a pass that makes less of itself for the
// resources a plan blames it for edits and plans (see resourceEdit).
const maxEditRounds = 3

// untilSettled is the rounds of a pass that ends when its review says so.
const untilSettled = math.MaxInt

// tidyPass is one of postProcess's passes over the configuration, as
// declared: where it runs, how its plan is judged, and how it starts. The
// tidying driver runs it (see tidying.settle): it backs up the files, makes
// the pass's edit, plans, judges the plan against the baseline and, as the
// pass says, accepts it, edits again or undoes it.
type tidyPass struct {
	name string
	// after names the passes this one must run after. The driver orders the
	// passes by them (see orderPasses), the order they are declared in
	// settling the rest.
	after []string
	// alone says the pass plans on its own. Without it, the edits of the
	// passes next to each other are checked together by one plan, and only
	// if that plan differs, each against the plan before it. A pass that
	// judges its plan any way but Exact, or rebases, must be alone.
	alone bool
	// policy is how closely the plan of its edit must match the baseline.
	policy Policy
	// rounds bounds how often the pass edits and plans: once if it is 0.
	rounds int
	// rebases says the plan the pass settles on is the plan the passes after
	// it compare with, as when it changes what the plan shows. stopped is the
	// error should the configuration then not plan, where there is no plan to
	// take: the pass settled without one (see tidyEdit.unplanned).
	rebases bool
	stopped string
	// start returns the pass's edit for this run, or nil where the run makes
	// none, such as the data sources of a run without external resources.
	start func() (*tidyEdit, error)
}

// tidyEdit is a pass for the driver to run: its edit, and what to do with
// the plan of it. Only edit is needed; the rest is for the passes that
// retry or settle in ways of their own.
type tidyEdit struct {
	// edit makes the pass's edit of the files in dir, as the driver left
	// them, and says what it changed. An edit that changes nothing leaves the
	// files as they were.
	edit func(a *tidyAttempt) (edited, error)
	// review, if set, decides what to make of the plan of an edit. Without
	// it, the plan is accepted if it is as the policy requires; otherwise the
	// resources it differs for, that edit changed, are skipped in the next
	// round (see tidyAttempt.skip), and without such a round, the edit is
	// undone.
	review func(a *tidyAttempt, r tidyReview) (tidyDecision, error)
	// otherwise, if set, is the edit to try, as a pass of one round, when the
	// plan rejects edit or it changes nothing.
	otherwise func() (bool, error)
	// unplanned, if set, is the edit to make instead where the review falls
	// back, or the rounds end without the plan accepting one. The files are
	// as before the pass, and no plan checks what it leaves: the passes after
	// it plan again.
	unplanned func(a *tidyAttempt) (bool, error)
	// undo, if set, is called wherever the driver puts the files back, for
	// what else the edit changed.
	undo func() error
	// finish, if set, is called once the pass is settled, with whether it
	// kept an edit; it may leave the files as the plan the pass settled on
	// has them (see tidyDecision.plan).
	finish func(kept bool) error
}

// edited is what an edit changed: the resources, by address, that it
// changed, which a plan that differs for them blames it for, or other if it
// changed files without naming resources.
type edited struct {
	resources map[string]bool
	other     bool
}

func (e edited) any() bool {
	return e.other || len(e.resources) > 0
}

// plainEdit is the edit of a function that only says whether it changed
// anything.
func plainEdit(edit func() (bool, error)) func(*tidyAttempt) (edited, error) {
	return func(*tidyAttempt) (edited, error) {
		changed, err := edit()
		return edited{other: changed}, err
	}
}

// resourceEdit is the edit of a function that makes less of itself the
// resources a plan blames it for: those skip has at a higher level (see
// tidyAttempt.skip) get less, as it says.
func resourceEdit(edit func(skip map[string]int) (map[string]bool, error)) func(*tidyAttempt) (edited, error) {
	return func(a *tidyAttempt) (edited, error) {
		resources, err := edit(a.skip)
		return edited{resources: resources}, err
	}
}

// tidyAttempt is one try of an edit, which a pass's functions are given.
type tidyAttempt struct {
	// round counts the tries from 0, out of rounds.
	round, rounds int
	// skip counts, by address, how often a plan blamed the edit for a
	// resource, for the edit to make less of itself for it.
	skip map[string]int
	// restore puts the files back as they were before the pass, for an edit
	// that edits more than once on its own.
	restore func() error
}

// last reports whether no round is left after this one.
func (a *tidyAttempt) last() bool {
	return a.round+1 >= a.rounds
}

// tidyReview is a plan of an edit, as the driver judged it.
type tidyReview struct {
	plan     shownPlan
	diags    []tfjson.Diagnostic
	verdict  Verdict
	baseline Baseline
	made     edited
}

// tidyOutcome is what a review makes of a plan.
type tidyOutcome int

const (
	// tidyAccept keeps the edit.
	tidyAccept tidyOutcome = iota
	// tidyRetry edits again, on the files as they were, if a round is left.
	tidyRetry
	// tidyReject undoes the edit, after which its otherwise is tried.
	tidyReject
	// tidyFallBack undoes the edit and makes the pass's unplanned one.
	tidyFallBack
)

// tidyDecision is a review's outcome, and, for tidyAccept, the plan it
// accepts where that isn't the one just shown, as the plan of the calls
// synthesize kept is.
type tidyDecision struct {
	outcome tidyOutcome
	plan    *shownPlan
}

// tidying is the driver of postProcess's passes. It owns the plans, the
// backups, the rounds and the order: a pass is an edit and how its plan is
// judged. It also holds what the passes share: the plan they compare with, as
// the passes before them left it, and what the curated modules leave for the
// passes after them.
type tidying struct {
	ctx  context.Context
	tf   tfrun.Terraform
	dir  string
	r    *run
	vars []tfexec.PlanOption

	baseline Baseline
	values   map[string]map[string]any

	// declined are the clusters the curated modules didn't take, with why,
	// and lists the lengths of the list outputs of the calls it made.
	declined []ModuleCall
	lists    map[string]int
}

// run runs passes, in the order their declarations say (see orderPasses). A
// pass the run doesn't make does nothing. It checks the edits of the passes
// next to each other together, and those of a pass that plans on its own by
// themselves, before it.
func (t *tidying) run(passes []tidyPass) error {
	ordered, err := orderPasses(passes)
	if err != nil {
		return err
	}
	type queued struct {
		pass tidyPass
		edit *tidyEdit
	}
	var batch []queued
	flush := func() error {
		defer func() { batch = nil }()
		if len(batch) > 1 {
			edits := make([]*tidyEdit, len(batch))
			for i, q := range batch {
				edits[i] = q.edit
			}
			settled, err := t.settleTogether(edits)
			if err != nil || settled {
				return err
			}
		}
		for _, q := range batch {
			if _, err := t.settle(q.pass, q.edit); err != nil {
				return err
			}
		}
		return nil
	}
	for _, p := range ordered {
		if p.alone {
			if err := flush(); err != nil {
				return err
			}
		}
		e, err := p.start()
		if err != nil {
			return err
		}
		switch {
		case e == nil:
		case p.alone:
			if _, err := t.settle(p, e); err != nil {
				return err
			}
		default:
			batch = append(batch, queued{p, e})
		}
	}
	return flush()
}

// orderPasses returns passes in an order that runs each after the passes it
// names, and otherwise in the order they are declared in. It reports a pass
// that names one there isn't, a cycle, and a pass that must plan on its own
// and doesn't say so.
func orderPasses(passes []tidyPass) ([]tidyPass, error) {
	at := map[string]int{}
	for i, p := range passes {
		if p.policy != Exact || p.rebases {
			if !p.alone {
				return nil, fmt.Errorf("the %s pass compares its plan other than exactly, or changes the plan the passes after it compare with, so it must plan alone", p.name)
			}
		}
		at[p.name] = i
	}
	for _, p := range passes {
		for _, name := range p.after {
			if _, ok := at[name]; !ok {
				return nil, fmt.Errorf("the %s pass runs after the %s pass, which there isn't", p.name, name)
			}
		}
	}
	done := map[string]bool{}
	ordered := make([]tidyPass, 0, len(passes))
	for len(ordered) < len(passes) {
		progress := false
		for _, p := range passes {
			if done[p.name] || !allDone(p.after, done) {
				continue
			}
			done[p.name] = true
			ordered = append(ordered, p)
			progress = true
			break
		}
		if !progress {
			return nil, errors.New("the passes run after each other in a cycle")
		}
	}
	return ordered, nil
}

func allDone(names []string, done map[string]bool) bool {
	for _, name := range names {
		if !done[name] {
			return false
		}
	}
	return true
}

// plan plans with the run's variables and returns the plan, or the errors if
// the configuration doesn't plan.
func (t *tidying) plan() (shownPlan, []tfjson.Diagnostic, error) {
	p, summary, diags, err := tfrun.ShowPlan(t.ctx, t.tf, t.vars)
	return shownPlan{p, summary}, diags, err
}

// settleTogether makes the edit of every pass, in order, each on the files as
// the ones before it left them, and keeps them all if the configuration then
// plans Exact to the baseline. Every plan with import blocks reads every
// imported resource again, so the passes that change nothing the plan shows
// need one plan between them. It reports whether that settled them: it kept
// them, or none of them changed anything. Otherwise, as when one edit fails,
// it leaves the files as they were, for the passes to be settled one at a
// time.
func (t *tidying) settleTogether(edits []*tidyEdit) (bool, error) {
	backup, err := fsutil.BackupFiles(t.dir, tidyFiles...)
	if err != nil {
		return false, err
	}
	a := &tidyAttempt{rounds: 1, skip: map[string]int{}, restore: backup.Restore}
	changed := false
	for _, e := range edits {
		made, err := e.edit(a)
		if err != nil {
			// The edit may fail only on top of another's: let the passes
			// one at a time tell.
			return false, backup.Restore()
		}
		changed = changed || made.any()
	}
	if !changed {
		return true, backup.Restore()
	}
	shown, diags, err := t.plan()
	if err != nil {
		return false, errors.Join(err, backup.Restore())
	}
	if t.baseline.Judge(shown, diags, Exact).Accepted() {
		return true, nil
	}
	return false, backup.Restore()
}

// settle makes the edit of pass p and keeps it only if its plan is as the
// pass's policy requires of the baseline, or as its review accepts, with the
// rounds the pass has to edit again on what the plans blame it for. It undoes
// what it doesn't keep, and a pass that rebases hands the baseline the plan it
// settled on. It reports whether it kept an edit.
func (t *tidying) settle(p tidyPass, e *tidyEdit) (bool, error) {
	kept, plan, err := t.attempts(p, e)
	if err != nil {
		return false, err
	}
	if e.finish != nil {
		if err := e.finish(kept); err != nil {
			return false, err
		}
	}
	if kept && p.rebases {
		if err := t.rebase(plan, p.stopped); err != nil {
			return false, err
		}
	}
	return kept, nil
}

// attempts makes the rounds of settle, and returns the plan of the edit it
// kept, or nil if it kept one no plan checked.
func (t *tidying) attempts(p tidyPass, e *tidyEdit) (bool, *shownPlan, error) {
	backup, err := fsutil.BackupFiles(t.dir, tidyFiles...)
	if err != nil {
		return false, nil, err
	}
	undo := func() error {
		err := backup.Restore()
		if e.undo != nil {
			err = errors.Join(e.undo(), err)
		}
		return err
	}
	a := &tidyAttempt{rounds: max(p.rounds, 1), skip: map[string]int{}, restore: undo}
	// end is what ended the rounds: a review's outcome, or tidyRetry for the
	// plan of the last round asking for another.
	end, unchanged := tidyReject, false
rounds:
	for a.round = 0; a.round < a.rounds; a.round++ {
		if a.round > 0 {
			if err := undo(); err != nil {
				return false, nil, err
			}
		}
		made, err := e.edit(a)
		if err != nil {
			return false, nil, errors.Join(err, undo())
		}
		if !made.any() {
			// It leaves the files as they were, as the round's start did.
			end, unchanged = tidyReject, true
			break
		}
		shown, diags, err := t.plan()
		if err != nil {
			return false, nil, errors.Join(err, undo())
		}
		d, err := t.review(a, e, tidyReview{plan: shown, diags: diags, verdict: t.baseline.Judge(shown, diags, p.policy), baseline: t.baseline, made: made})
		if err != nil {
			return false, nil, errors.Join(err, undo())
		}
		switch d.outcome {
		case tidyAccept:
			if d.plan == nil {
				d.plan = &shown
			}
			return true, d.plan, nil
		case tidyRetry:
			end = tidyRetry
		default:
			end = d.outcome
			break rounds
		}
	}
	if !unchanged {
		if err := undo(); err != nil {
			return false, nil, err
		}
	}
	if e.unplanned != nil && (end == tidyFallBack || end == tidyRetry) {
		changed, err := e.unplanned(a)
		if err != nil {
			return false, nil, errors.Join(err, undo())
		}
		return changed, nil, nil
	}
	if e.otherwise != nil {
		once := p
		once.rounds = 1
		return t.attempts(once, &tidyEdit{edit: plainEdit(e.otherwise)})
	}
	return false, nil, nil
}

// review decides what to make of a plan of an edit, by the pass's review or,
// without one, by the default: accept what the plan accepts, and where it
// doesn't, edit again without what it blames the edit for.
func (t *tidying) review(a *tidyAttempt, e *tidyEdit, r tidyReview) (tidyDecision, error) {
	if e.review != nil {
		return e.review(a, r)
	}
	if r.verdict.Accepted() {
		return tidyDecision{outcome: tidyAccept}, nil
	}
	if a.last() {
		return tidyDecision{outcome: tidyReject}, nil
	}
	blamed, err := blame(t.dir, r.diags, r.verdict)
	if err != nil {
		return tidyDecision{}, err
	}
	// What blames a resource the edit didn't change tells nothing of it.
	again := false
	for address := range blamed {
		if r.made.resources[address] {
			a.skip[address]++
			again = true
		}
	}
	if !again {
		return tidyDecision{outcome: tidyReject}, nil
	}
	return tidyDecision{outcome: tidyRetry}, nil
}

// rebase makes the baseline of plan, a plan of the configuration as the
// passes left it, what the passes after compare with, and takes the imported
// values it has of the root's resources: a resource moved into a module
// keeps the values of its root address, which are what it was imported
// with. Without a plan, or one the pass has none of, it plans: stopped
// is the error should the configuration not plan.
func (t *tidying) rebase(plan *shownPlan, stopped string) error {
	if plan == nil || plan.plan == nil || plan.summary == nil {
		shown, _, err := t.plan()
		if err != nil {
			return err
		}
		if shown.plan == nil {
			return errors.New(stopped)
		}
		plan = &shown
	}
	t.baseline = t.baseline.Rebase(*plan)
	for address, values := range valuesOf(plan.plan) {
		if _, ok := t.values[address]; ok {
			t.values[address] = values
		}
	}
	return nil
}
