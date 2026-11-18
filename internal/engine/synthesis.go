// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

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

// synthesize is the pass that moves clusters of resources into calls of
// curated modules: for each anchor resource, the first adapter that maps its
// cluster. Its edit writes the calls, installs the modules and plans, and
// its review takes back the calls whose resources would plan differently than
// the baseline has them (see Baseline.Tolerates), until the plan matches; when
// the plan fails in a way it can't pin on a call, it plans half of the calls
// at a time to find the calls that fail. A module may set the arguments the
// baseline's state-only rules name, which import can't (see StateOnly). Calls
// are named other than the names the run takes.
// The imported values, by address, are t.values: references to what a call
// takes and has no output for become the values again (see trial.literals).
// It leaves the clusters it didn't move, with why, in t.declined, and the
// lengths of the list outputs of the calls it kept in t.lists (see
// outputLists); the pass rebases onto the plan with the calls it kept, for the
// passes after to compare with. It returns no edit where the run has no
// adapters or they map no cluster.
func (t *tidying) synthesize() (*tidyEdit, error) {
	opts, ws := t.r.Opts, t.ws
	if len(opts.Adapters) == 0 {
		return nil, nil
	}
	trials, declined, err := mapClusters(ws, opts.Adapters, t.r.Taken, t.values)
	if err != nil || len(trials) == 0 {
		t.declined = declined
		return nil, err
	}
	write := func(ts []*trial) error {
		return writeCalls(ws, ts, opts.ModuleProviders, opts.DefaultTags)
	}
	// written are the calls the root has now. kept are the calls a plan
	// accepted together, and keptPlan is that plan.
	var written, kept []*trial
	var keptPlan shownPlan
	// pending are the groups of calls still to plan with kept, in turn. A
	// group whose failure the plan can't pin on one of its calls is halved
	// and its halves are planned in turn, the other held back meanwhile,
	// until only the calls that fail are declined: one plan when the calls
	// plan clean, and a plan or two for each halving for each call that
	// doesn't. Every plan keeps a group, declines a call or halves a group,
	// so the rounds end.
	var pending []*callGroup
	var passed, current *callGroup
	// split halves a group whose calls fail for reason, or declines its call.
	split := func(g *callGroup, reason string) {
		if len(g.trials) == 1 {
			rejectTrials(reason, g.trials...)
			return
		}
		first := &callGroup{trials: g.trials[:len(g.trials)/2]}
		second := &callGroup{trials: g.trials[len(g.trials)/2:], fails: reason, failsAfter: first}
		pending = append([]*callGroup{first, second}, pending...)
	}
	// next returns the calls to plan next: the next group's, with what is
	// kept. A group known to fail with what is kept (see callGroup) is
	// halved, or declined, without planning it again. It returns none when
	// no group is left.
	next := func() []*trial {
		for len(pending) > 0 {
			g := pending[0]
			pending = pending[1:]
			g.trials = activeTrials(g.trials)
			if len(g.trials) == 0 {
				continue
			}
			if g.failsAfter == nil || g.failsAfter != passed {
				current = g
				return inOrder(trials, kept, g.trials)
			}
			split(g, g.fails)
		}
		return nil
	}
	var tried []*trial
	return &tidyEdit{
		edit: func(a *tidyAttempt) (edited, error) {
			if a.round > 0 {
				// The files are as the pass began.
				written = tried
				return edited{other: true}, write(tried)
			}
			if err := write(trials); err != nil {
				return edited{}, err
			}
			written = trials
			if err := tfrun.Init(t.ctx, t.tf); err != nil {
				// Typically: the registry can't be reached.
				rejectTrials(fmt.Sprintf("terraform init: %v", err), trials...)
			}
			pending = []*callGroup{{trials: activeTrials(trials)}}
			if tried = next(); tried == nil {
				return edited{}, a.restore()
			}
			if !slices.Equal(tried, written) {
				if err := a.restore(); err != nil {
					return edited{}, err
				}
				written = tried
				if err := write(tried); err != nil {
					return edited{}, err
				}
			}
			return edited{other: true}, nil
		},
		review: func(a *tidyAttempt, r tidyReview) (tidyDecision, error) {
			ok, why := judge(tried, r.plan, r.diags, r.baseline)
			switch {
			case ok:
				kept, keptPlan, passed = tried, r.plan, current
			case len(activeTrials(tried)) < len(tried):
				// The plan took calls back: what is left plans again, with
				// the group if the plan took back one of kept.
				retry := &callGroup{trials: current.trials}
				if len(activeTrials(kept)) < len(kept) {
					retry.trials, kept = tried, nil
				}
				pending = append([]*callGroup{retry}, pending...)
			case why == "":
				// Nothing to take back (the plan reported neither errors
				// nor a result): planning again would show the same.
				rejectTrials("the plan reported no result", current.trials...)
				for _, held := range pending {
					rejectTrials("the plan reported no result", held.trials...)
				}
				pending = nil
			default:
				split(current, why)
			}
			if tried = next(); tried != nil {
				return tidyDecision{outcome: tidyRetry}, nil
			}
			if len(kept) == 0 {
				return tidyDecision{outcome: tidyReject}, nil
			}
			// The last plan may have tried calls declined since: the root has
			// the calls the plan of kept accepted, which planning again would
			// show.
			if !slices.Equal(kept, written) {
				if err := a.restore(); err != nil {
					return tidyDecision{}, err
				}
				if err := write(kept); err != nil {
					return tidyDecision{}, err
				}
			}
			if err := pinModuleProviders(ws, opts.ModuleProviders); err != nil {
				return tidyDecision{}, err
			}
			return tidyDecision{outcome: tidyAccept, plan: &keptPlan}, nil
		},
		finish: func(bool) error {
			t.declined = slices.Concat(declined, rejections(trials))
			if len(kept) > 0 {
				t.lists = outputLists(kept)
			}
			return nil
		},
	}, nil
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
// it did in the root, or only also updates arguments baseline's state-only
// rules name for its type (see StateOnly), the modules create nothing else,
// and the totals match baseline, with those updates and the imports the
// calls add. A resource outside the modules is judged by the totals alone.
// When the plan fails with nothing to tell the calls apart by, an error
// about none of them or totals that don't match, it also returns why, and
// rejects none for it: the caller plans them apart.
func judge(active []*trial, plan shownPlan, diags []tfjson.Diagnostic, baseline Baseline) (bool, string) {
	p, summary := plan.plan, plan.summary
	if len(diags) > 0 || p == nil {
		unattributed := ""
		for _, d := range diags {
			t := trialOf(active, d)
			if t == nil {
				if unattributed == "" {
					unattributed = "plan error: " + tfrun.DiagnosticMessage(d)
				}
				continue
			}
			if t.rejected == "" {
				t.rejected = "plan error: " + tfrun.DiagnosticMessage(d)
			}
		}
		return false, unattributed
	}
	ok := true
	imports, updates := 0, 0
	for _, t := range active {
		imports += len(t.call.Imports)
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
			switch member {
			case "":
				if !rc.Change.Actions.NoOp() && !rc.Change.Actions.Read() {
					t.rejected = fmt.Sprintf("the module would also %s %s", strings.Join(tfrun.ActionNames(rc.Change.Actions), " and "), strings.TrimPrefix(rc.Address, "module."+t.name+"."))
					ok = false
				}
			default:
				tolerated, update := baseline.Tolerates(rc, member, StateOnly)
				if !tolerated {
					t.rejected = fmt.Sprintf("%s would plan differently: %s instead of %s", member, describeSignature(tfrun.ChangeSignature(rc)), describeSignature(baseline.Planned(member)))
					ok = false
				}
				if update {
					updates++
				}
			}
		}
	}
	if ok && (summary == nil || *summary != baseline.Expected(imports, updates)) {
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

// rejectTrials declines the calls of ts that aren't declined yet, for reason.
func rejectTrials(reason string, ts ...*trial) {
	for _, t := range ts {
		if t.rejected == "" {
			t.rejected = reason
		}
	}
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
