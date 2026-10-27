// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// decideDrift sets each change's decision, from accept (nil keeps them
// all), and the outcome of each change the next apply would revert: kept,
// or, accepted, a secret or a hand edit where sources, by change, say so.
// It returns the accepted changes the code can take by an edit.
func decideDrift(changes []DriftChange, sources []codeSource, accept func(DriftChange) bool) []*DriftChange {
	var accepted []*DriftChange
	for i := range changes {
		c := &changes[i]
		c.Decision = DriftKeep
		if accept != nil && accept(*c) {
			c.Decision = DriftAccept
		}
		if c.Outcome != DriftReverts {
			continue
		}
		src := sources[i]
		switch {
		case c.Decision == DriftKeep:
			c.Outcome = DriftKept
		case src.Secret:
			c.Outcome, c.Reason = DriftSecret, src.Reason
		case src.Edit == nil:
			c.Outcome, c.Reason = DriftManual, src.Reason
		default:
			accepted = append(accepted, c)
		}
	}
	return accepted
}

// settleDrift sets the outcome of each change failed has the source of,
// one the edits couldn't take, and returns the others of changes, and
// whether it unshared a change of shared tags (see unshare), whose
// members it returns in its place.
func settleDrift(changes []*DriftChange, failed map[*DriftChange]codeSource) ([]*DriftChange, bool) {
	var left []*DriftChange
	unshared := false
	for _, c := range changes {
		src, ok := failed[c]
		switch {
		case !ok:
			left = append(left, c)
		case c.shared != nil:
			left = append(left, unshare(c, src.Reason)...)
			unshared = true
		case src.Secret:
			c.Outcome, c.Reason = DriftSecret, src.Reason
		default:
			c.Outcome, c.Reason = DriftManual, src.Reason
		}
	}
	return left, unshared
}

// acceptDrift takes the cloud's values of accepted into the code of the
// root in dir, root by file name, through the staged copy in staging,
// which stf plans: the edits are made in the copy first, and planned
// without refreshing, with vars. Each resource an accepted change is of
// must then plan no change to it, and no other change than the plan of
// the code had, code, but on what is secret or kept only in state; every
// other resource must plan what code has it plan (see changeSignature),
// which an edit of a local others use would break. Then the edited files
// must pass the format, validate, standards, moves and secrets checks
// (see driftChecks). The edits the plan or a check blames are left out
// (see judgeDrift and checkBlame; one that blames no file blames them
// all), and the others planned and checked again, in at most as many
// rounds as there are edits (see verifier); a change of shared tags
// whose edit is blamed, or can't be made, is accepted at each resource
// it covers instead (see unshare). Only then are the edits made in the
// root's own files, which are restored if writing one fails. For a root
// environments share, root and the copy's files hold the environment's
// var file too, the only file edited (see variableResolver.env), and a change
// written gets its source there. Each accepted change ends written,
// rejected by the plan or a check, a hand edit or a secret.
func acceptDrift(ctx context.Context, stf DriftTerraform, dir, staging string, root map[string][]byte, accepted []*DriftChange, code *tfjson.Plan, vars []tfexec.PlanOption, editor *driftEditor, opts DriftOptions) error {
	schemas, err := stf.ProvidersSchema(ctx)
	if err != nil {
		return fmt.Errorf("terraform providers schema: %w", err)
	}
	editor.schemas = schemas
	staged, err := driftContents(staging, opts.Environment)
	if err != nil {
		return err
	}
	// The modules' files, which the copy has copies of (see stageModule).
	for name, content := range root {
		if isModuleFile(name) {
			staged[name] = content
		}
	}
	standards, err := checkStandards(staging, opts.Omit, ownModuleDirs(opts.Adapters))
	if err != nil {
		return err
	}
	moves, err := checkMoves(staging)
	if err != nil {
		return err
	}
	planOpts := append(slices.Clone(vars), tfexec.Refresh(false), tfexec.Lock(false))
	// Each round that fails rejects an edit, or has a change of shared
	// tags edited at each resource it covers instead: no round makes
	// progress without dropping an edit, so the rounds can't outnumber
	// the edits, those of the shared tags' members counted.
	rounds := 0
	for _, c := range accepted {
		rounds++
		if c.shared != nil {
			rounds += len(c.shared.members)
		}
	}
	pending := accepted
	written := map[string]bool{}
	var edited map[string][]byte
	var names []string
	var applied map[*DriftChange]codeSource
	passed, err := verifier[*DriftChange, driftRejection]{
		edit: func(context.Context) ([]*DriftChange, error) {
			// A change of shared tags whose edit can't be made has its
			// members edited before anything is planned.
			for unshared := true; unshared && len(pending) > 0; {
				edited = maps.Clone(staged)
				var failed map[*DriftChange]codeSource
				var err error
				names, applied, failed, err = editor.apply(edited, pending)
				if err != nil {
					return nil, err
				}
				pending, unshared = settleDrift(pending, failed)
			}
			if len(pending) == 0 {
				return nil, nil
			}
			for _, name := range names {
				written[name] = true
			}
			// The files an earlier round edited are as they were, but for
			// this round's edits.
			for _, name := range sortedKeys(written) {
				if err := fsutil.WriteFile(stagedPath(staging, name), edited[name], 0o600); err != nil {
					return nil, err
				}
			}
			return pending, nil
		},
		verify: func(ctx context.Context) (*verdict[*DriftChange, driftRejection], error) {
			p, _, diags, err := showPlanWithSummary(ctx, stf, planOpts)
			if err != nil {
				return nil, err
			}
			if rejected := judgeDrift(p, diags, code, pending, applied, opts.StateOnly, edited, editor.forms); len(rejected) > 0 {
				return planRejection(rejected), nil
			}
			failedCheck, files, err := driftChecks(ctx, stf, staging, staged, edited, names, p, standards, moves, editor.sensitive, opts)
			if err != nil || failedCheck == "" {
				return nil, err
			}
			failure := &verdict[*DriftChange, driftRejection]{
				blamed: map[*DriftChange]driftRejection{},
				why:    driftRejection{DriftRejectedByCheck, "the edited code fails " + failedCheck},
			}
			for _, c := range checkBlame(pending, applied, files) {
				failure.blamed[c] = failure.why
			}
			return failure, nil
		},
		drop: func(rejected map[*DriftChange]driftRejection) {
			var left []*DriftChange
			for _, c := range pending {
				r, ok := rejected[c]
				switch {
				case !ok:
					left = append(left, c)
				case c.shared != nil:
					// Each resource it covers is edited on its own instead.
					left = append(left, unshare(c, r.reason)...)
				default:
					c.Outcome, c.Reason = r.outcome, r.reason
				}
			}
			pending = left
		},
		// A check that can't tell which edited files fail it fails them
		// all.
		blameAll: true,
		rounds:   rounds,
	}.run(ctx)
	if err != nil {
		return err
	}
	if !passed {
		// Only the bound, which no round reaches, leaves changes pending.
		for _, c := range pending {
			c.Outcome, c.Reason = DriftRejectedByPlan, fmt.Sprintf("the edits weren't settled in %d plans", rounds)
		}
		return nil
	}

	out := maps.Clone(root)
	rootNames, applied, failed, err := editor.apply(out, pending)
	if err != nil {
		return err
	}
	if len(failed) > 0 {
		// The root's files are the staged copy's, but for its backend and
		// the sources of local modules: the same edits must apply.
		for _, c := range pending {
			c.Outcome, c.Reason = DriftRejectedByPlan, "the root's files differ from the copy the edits were planned in: edit them by hand"
		}
		return nil
	}
	if err := writeDriftFiles(dir, out, rootNames, opts.writeFile); err != nil {
		return err
	}
	var cfg *driftConfig
	if editor.env != "" {
		// A value added to the environment's var file is set there now.
		editor.syncModules(out)
		if cfg, err = parseDriftConfig(out); err != nil {
			return err
		}
	}
	for _, c := range pending {
		c.Outcome, c.Reason = DriftWritten, applied[c].Note
		if cfg != nil {
			c.Source = reportSource(cfg, editor.resolve(cfg, c), editor.forms)
		}
	}
	return nil
}

// writeDriftFiles writes the files names of contents into dir, with write
// (nil for fsutil.WriteFile); if one fails, it restores those it wrote.
func writeDriftFiles(dir string, contents map[string][]byte, names []string, write func(string, []byte, os.FileMode) error) error {
	if write == nil {
		write = fsutil.WriteFile
	}
	backup, err := backupFiles(dir, names...)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := write(filepath.Join(dir, name), contents[name], 0o644); err != nil {
			werr := fmt.Errorf("write the accepted changes into %s: %w", name, err)
			if rerr := backup.restore(); rerr != nil {
				return errors.Join(werr, fmt.Errorf("restore the root's files: %w", rerr))
			}
			return fmt.Errorf("%w; the root's files were restored", werr)
		}
	}
	return nil
}

// judgeDrift returns the changes of pending whose edits, applied as
// sources say, the plan of the edited code, p, or its errors, diags,
// blame, each with why: an error in the edited block; an accepted
// attribute the plan still changes; or another attribute of the resource
// the plan of the code, code, didn't change, but for what is secret or
// kept only in state, or a key of its own tags or labels that only the
// state records (see recordedTagLeaves); or another resource planning
// other than code has it, which the edits of locals, of shared tags and
// of module calls' arguments, which reach every resource the module
// makes from them, are blamed for first, then those of the resources it refers to, else
// all of them. A change of shared tags is judged on every resource it
// covers. Reasons have the values of forms redacted.
func judgeDrift(p *tfjson.Plan, diags []tfjson.Diagnostic, code *tfjson.Plan, pending []*DriftChange, sources map[*DriftChange]codeSource, stateOnly map[string][]string, contents map[string][]byte, forms []string) map[*DriftChange]string {
	rejected := map[*DriftChange]string{}
	reject := func(c *DriftChange, reason string) {
		if _, ok := rejected[c]; !ok {
			rejected[c] = redactText(reason, forms)
		}
	}
	if len(diags) > 0 || p == nil {
		for _, d := range diags {
			reason := "the plan of the edited code fails: " + strings.Join(strings.Fields(formatDiagnostic(d)), " ")
			blamed := false
			for _, c := range pending {
				if diagnosticOf(d, c, sources[c], contents) {
					reject(c, reason)
					blamed = true
				}
			}
			if !blamed {
				for _, c := range pending {
					reject(c, reason)
				}
			}
		}
		if len(diags) == 0 {
			for _, c := range pending {
				reject(c, "terraform plan reported no change summary")
			}
		}
		return rejected
	}
	baseline := map[string]*tfjson.ResourceChange{}
	if code != nil {
		for _, rc := range code.ResourceChanges {
			if rc.Mode == tfjson.ManagedResourceMode && rc.Change != nil {
				baseline[rc.Address] = rc
			}
		}
	}
	byAddress := map[string][]*DriftChange{}
	for _, c := range pending {
		if c.shared != nil {
			for _, a := range c.Covers {
				byAddress[a] = append(byAddress[a], c)
			}
			continue
		}
		byAddress[c.Address] = append(byAddress[c.Address], c)
	}
	var leaked []string
	for _, rc := range p.ResourceChanges {
		if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil || rc.Change.Importing != nil {
			continue
		}
		base := baseline[rc.Address]
		cs := byAddress[rc.Address]
		if len(cs) == 0 {
			if !sameAsBaseline(rc, base) {
				leaked = append(leaked, rc.Address)
			}
			continue
		}
		if rc.Change.Actions.NoOp() {
			continue
		}
		baseLeaves := map[string]bool{}
		if base != nil {
			for _, l := range planLeaves(base, stateOnly) {
				baseLeaves[l.path] = true
			}
		}
		recorded := recordedTagLeaves(rc)
		for _, l := range planLeaves(rc, stateOnly) {
			if l.sensitive || recorded[l.path] {
				continue
			}
			matched := false
			for _, c := range cs {
				if c.Attribute == l.path || within(l.path, c.Attribute) || within(c.Attribute, l.path) {
					reject(c, "the plan of the edited code still changes "+l.path)
					matched = true
				}
			}
			if !matched && !baseLeaves[l.path] {
				for _, c := range cs {
					reject(c, "the plan of the edited code changes "+l.path+" too")
				}
			}
		}
		if rc.Change.Actions.Replace() && (base == nil || !base.Change.Actions.Replace()) {
			for _, c := range cs {
				reject(c, "the plan of the edited code replaces the resource")
			}
		}
	}
	if len(leaked) == 0 {
		return rejected
	}
	reason := "the edit changes what the plan does to " + strings.Join(leaked, ", ") + " too"
	blamed := false
	for _, c := range pending {
		if src := sources[c]; src.Kind == sourceLocal || src.Kind == sourceSharedTags || src.Module != "" {
			reject(c, reason)
			blamed = true
		}
	}
	if !blamed {
		referred := referredTo(contents, leaked)
		for _, c := range pending {
			if referred[trimInstanceKey(c.Address)] {
				reject(c, reason)
				blamed = true
			}
		}
	}
	if !blamed {
		for _, c := range pending {
			reject(c, reason)
		}
	}
	return rejected
}

// sameAsBaseline reports whether rc plans what base, the plan of the
// code before the edits, has for the resource: nothing if base has none.
func sameAsBaseline(rc, base *tfjson.ResourceChange) bool {
	if base == nil {
		return rc.Change.Actions.NoOp() || rc.Change.Actions.Read()
	}
	return changeSignature(rc) == changeSignature(base)
}

// planLeaves returns the attributes a plan changes for rc, as drift
// counts them (see driftLeaves).
func planLeaves(rc *tfjson.ResourceChange, stateOnly map[string][]string) []valueLeaf {
	return driftLeaves(rc.Type, valueLeaves(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown, rc.Change.BeforeSensitive, rc.Change.AfterSensitive), stateOnly)
}

// diagnosticOf reports whether d is about the edit of c, made as src
// says in contents: in its resource, or in the file and block it edits.
func diagnosticOf(d tfjson.Diagnostic, c *DriftChange, src codeSource, contents map[string][]byte) bool {
	if c.shared != nil {
		if d.Address != "" {
			return slices.ContainsFunc(c.Covers, func(a string) bool { return trimInstanceKey(a) == trimInstanceKey(d.Address) })
		}
		return d.Range != nil && src.Edit != nil && filepath.Base(d.Range.Filename) == src.Edit.File
	}
	if d.Address != "" {
		return trimInstanceKey(d.Address) == trimInstanceKey(c.Address)
	}
	if d.Range == nil || src.Edit == nil || filepath.Base(d.Range.Filename) != src.Edit.File {
		return false
	}
	f, err := hclfile.Parse(src.Edit.File, contents[src.Edit.File])
	if err != nil {
		return true
	}
	if address := f.ResourceAt(d.Range.Start.Line); address != "" {
		return address == trimInstanceKey(c.Address)
	}
	for _, b := range f.Syntax.Blocks {
		switch {
		case !hclfile.Spans(b.Range(), d.Range.Start.Line):
		case b.Type == "locals":
			return src.Kind == sourceLocal
		case b.Type == "module" && len(b.Labels) == 1 && src.Module != "":
			calls, _, _, ok := splitModuleAddress(c.Address)
			return ok && calls[0].name == b.Labels[0]
		}
	}
	return false
}

// referredTo returns the resources the blocks of addresses refer to, in
// contents.
func referredTo(contents map[string][]byte, addresses []string) map[string]bool {
	referred := map[string]bool{}
	cfg, err := parseDriftConfig(contents)
	if err != nil {
		return referred
	}
	for _, address := range addresses {
		b, ok := cfg.blocks[trimInstanceKey(address)]
		if !ok {
			continue
		}
		for _, t := range allTraversals(b.b.Body) {
			if len(t) < 2 {
				continue
			}
			if step, ok := t[1].(hcl.TraverseAttr); ok {
				referred[t.RootName()+"."+step.Name] = true
			}
		}
	}
	return referred
}

// driftChecks runs the checks of the verification gate that judge edits
// on the staged copy in staging, whose files original edited into edited,
// those names: G1 format, of the edited files; G2 validate; G4 standards
// and G8 moves, failing on what they find that they didn't before, as
// standards and moves; and G6 secrets of the edited files, with the
// values p and sensitive have that no file may hold. It returns the
// check that fails, with what it found, and the edited files it fails
// on, none where it can't tell; or "".
func driftChecks(ctx context.Context, stf DriftTerraform, staging string, original, edited map[string][]byte, names []string, p *tfjson.Plan, standards, moves Check, sensitive []string, opts DriftOptions) (string, []string, error) {
	gate, _, err := gatePolicy{
		checks:    []string{CheckFormat, CheckValidate, CheckStandards, CheckMoves, CheckSecrets},
		sensitive: append(sensitiveValues(p), sensitive...),
		baseline:  Gate{standards, moves},
		diff:      true,
		edited:    names,
		before:    original,
		after:     edited,
		omit:      opts.Omit,
		own:       ownModuleDirs(opts.Adapters),
	}.run(ctx, stf, staging)
	if err != nil {
		return "", nil, err
	}
	for _, c := range gate {
		if !c.Passed {
			return c.Name + ": " + strings.Join(c.Details, "; "), c.files, nil
		}
	}
	return "", nil, nil
}

// checkBlame returns the changes of pending whose edits, made as sources
// say, are in files, the edited files a check fails on.
func checkBlame(pending []*DriftChange, sources map[*DriftChange]codeSource, files []string) []*DriftChange {
	var blamed []*DriftChange
	for _, c := range pending {
		src := sources[c]
		if src.Edit == nil {
			continue
		}
		for _, edit := range append([]hclfile.Edit{*src.Edit}, src.Also...) {
			if slices.Contains(files, edit.File) {
				blamed = append(blamed, c)
				break
			}
		}
	}
	return blamed
}

// A driftRejection is why the plan or a check rejects an accepted
// change: the outcome it ends with, and the reason.
type driftRejection struct {
	outcome, reason string
}

// planRejection is the verdict of a plan of edited code that rejects the
// changes of rejected, each for its reason (see judgeDrift). A verifier
// that blames all of a round's edits where none of them is blamed rejects
// them by the plan too.
func planRejection(rejected map[*DriftChange]string) *verdict[*DriftChange, driftRejection] {
	failure := &verdict[*DriftChange, driftRejection]{
		blamed: map[*DriftChange]driftRejection{},
		why:    driftRejection{DriftRejectedByPlan, "the plan of the edited code doesn't take the edits"},
	}
	for c, reason := range rejected {
		failure.blamed[c] = driftRejection{DriftRejectedByPlan, reason}
	}
	return failure
}

// mergedTagAttributes are, by a resource's own tags or labels, the
// attributes that hold them together with the provider's: what reaches
// the cloud.
var mergedTagAttributes = map[string][]string{
	"tags":   {"tags_all"},
	"labels": {"terraform_labels", "effective_labels"},
}

// recordedTagLeaves returns the paths of the keys of a resource's own
// tags or labels that rc, a planned change, changes while the attributes
// holding them with the provider's, such as tags_all, keep the key as it
// was: what reaches the cloud stays the same, and only the state records
// the change. A key of the provider's default tags a resource gets its
// own value of, or that the provider's then gives it, plans so without
// refreshing.
func recordedTagLeaves(rc *tfjson.ResourceChange) map[string]bool {
	before, _ := rc.Change.Before.(map[string]any)
	after, _ := rc.Change.After.(map[string]any)
	unknown, _ := rc.Change.AfterUnknown.(map[string]any)
	paths := map[string]bool{}
	for own, mergedAll := range mergedTagAttributes {
		var merged []string
		for _, m := range mergedAll {
			_, b := before[m]
			_, a := after[m]
			if b || a {
				merged = append(merged, m)
			}
		}
		if len(merged) == 0 {
			continue
		}
		ob, _ := before[own].(map[string]any)
		oa, _ := after[own].(map[string]any)
		keys := map[string]bool{}
		for k := range ob {
			keys[k] = true
		}
		for k := range oa {
			keys[k] = true
		}
		for k := range keys {
			vb, okb := ob[k]
			va, oka := oa[k]
			if okb == oka && reflect.DeepEqual(vb, va) {
				continue
			}
			kept := true
			for _, m := range merged {
				if marked(unknown[m]) || marked(maskKey(unknown[m], k)) {
					kept = false
					break
				}
				mb, _ := before[m].(map[string]any)
				ma, _ := after[m].(map[string]any)
				vb, okb := mb[k]
				va, oka := ma[k]
				if okb != oka || !reflect.DeepEqual(vb, va) {
					kept = false
					break
				}
			}
			if kept {
				paths[own+"."+k] = true
			}
		}
	}
	return paths
}
