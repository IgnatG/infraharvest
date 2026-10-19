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

	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// decideDrift sets each change's decision, from accept (nil keeps them
// all), and the outcome of each change the next apply would revert: kept,
// or, accepted, a secret or a hand edit where sources, by change, say so.
// It returns the accepted changes the code can take by an edit.
func decideDrift(changes []DriftChange, sources []Source, accept func(DriftChange) bool) []*DriftChange {
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
// one the edits couldn't take, and returns the others of changes.
func settleDrift(changes []*DriftChange, failed map[*DriftChange]Source) []*DriftChange {
	var left []*DriftChange
	for _, c := range changes {
		src, ok := failed[c]
		switch {
		case !ok:
			left = append(left, c)
		case src.Secret:
			c.Outcome, c.Reason = DriftSecret, src.Reason
		default:
			c.Outcome, c.Reason = DriftManual, src.Reason
		}
	}
	return left
}

// acceptDrift takes the cloud's values of accepted into the code of the
// root in dir, root by file name, through the staged copy in staging,
// which stf plans: the edits are made in the copy first, and planned
// without refreshing, with vars. Each resource an accepted change is of
// must then plan no change to it, and no other change than the plan of
// the code had, code, but on what is secret or kept only in state; every
// other resource must plan what code has it plan (see changeSignature),
// which an edit of a local others use would break. The edits the plan
// blames are left out (see judgeDrift), and the others planned again.
// Then the edited files must pass the format, validate, standards,
// moves and secrets checks. Only then are the edits made in the root's
// own files, which are restored if writing one fails. Each accepted
// change ends written, rejected by the plan, a hand edit or a secret.
func acceptDrift(ctx context.Context, stf DriftTerraform, dir, staging string, root map[string][]byte, accepted []*DriftChange, code *tfjson.Plan, vars []tfexec.PlanOption, editor *driftEditor, opts DriftOptions) error {
	schemas, err := stf.ProvidersSchema(ctx)
	if err != nil {
		return fmt.Errorf("terraform providers schema: %w", err)
	}
	editor.schemas = schemas
	staged, err := tfContents(staging)
	if err != nil {
		return err
	}
	standards, err := checkStandards(staging, opts.Omit, nil)
	if err != nil {
		return err
	}
	moves, err := checkMoves(staging)
	if err != nil {
		return err
	}
	planOpts := append(slices.Clone(vars), tfexec.Refresh(false), tfexec.Lock(false))
	pending := accepted
	written := map[string]bool{}
	var edited map[string][]byte
	var names []string
	var p *tfjson.Plan
	for len(pending) > 0 {
		edited = maps.Clone(staged)
		var applied, failed map[*DriftChange]Source
		names, applied, failed, err = editor.apply(edited, pending)
		if err != nil {
			return err
		}
		if pending = settleDrift(pending, failed); len(pending) == 0 {
			break
		}
		for _, name := range names {
			written[name] = true
		}
		// The files an earlier round edited are as they were, but for this
		// round's edits.
		for _, name := range sortedKeys(written) {
			if err := fsutil.WriteFile(filepath.Join(staging, name), edited[name], 0o600); err != nil {
				return err
			}
		}
		var diags []tfjson.Diagnostic
		p, _, diags, err = showPlanWithSummary(ctx, stf, planOpts)
		if err != nil {
			return err
		}
		rejected := judgeDrift(p, diags, code, pending, applied, opts.StateOnly, edited, editor.forms)
		if len(rejected) == 0 {
			break
		}
		var left []*DriftChange
		for _, c := range pending {
			if reason, ok := rejected[c]; ok {
				c.Outcome, c.Reason = DriftRejectedByPlan, reason
				continue
			}
			left = append(left, c)
		}
		pending = left
	}
	if len(pending) == 0 {
		return nil
	}
	failedCheck, err := driftChecks(ctx, stf, staging, staged, edited, names, p, standards, moves, editor.sensitive, opts)
	if err != nil {
		return err
	}
	if failedCheck != "" {
		for _, c := range pending {
			c.Outcome, c.Reason = DriftRejectedByPlan, "the edited code fails "+failedCheck
		}
		return nil
	}

	out := maps.Clone(root)
	rootNames, _, failed, err := editor.apply(out, pending)
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
	for _, c := range pending {
		c.Outcome, c.Reason = DriftWritten, ""
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
// other than code has it, which the edits of locals are blamed for first, then those of the
// resources it refers to, else all of them. Reasons have the values of
// forms redacted.
func judgeDrift(p *tfjson.Plan, diags []tfjson.Diagnostic, code *tfjson.Plan, pending []*DriftChange, sources map[*DriftChange]Source, stateOnly map[string][]string, contents map[string][]byte, forms []string) map[*DriftChange]string {
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
		if sources[c].Kind == SourceLocal {
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
func diagnosticOf(d tfjson.Diagnostic, c *DriftChange, src Source, contents map[string][]byte) bool {
	if d.Address != "" {
		return trimInstanceKey(d.Address) == trimInstanceKey(c.Address)
	}
	if d.Range == nil || src.Edit == nil || filepath.Base(d.Range.Filename) != src.Edit.File {
		return false
	}
	f, err := parseHCL(src.Edit.File, contents[src.Edit.File])
	if err != nil {
		return true
	}
	if address := f.resourceAt(d.Range.Start.Line); address != "" {
		return address == trimInstanceKey(c.Address)
	}
	for _, b := range f.syntax.Blocks {
		if b.Type == "locals" && spans(b.Range(), d.Range.Start.Line) {
			return src.Kind == SourceLocal
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
// check that fails, with what it found, or "".
func driftChecks(ctx context.Context, stf DriftTerraform, staging string, original, edited map[string][]byte, names []string, p *tfjson.Plan, standards, moves Check, sensitive []string, opts DriftOptions) (string, error) {
	formatted, files, err := stf.FormatCheck(ctx)
	if err != nil {
		return "", fmt.Errorf("terraform fmt: %w", err)
	}
	if !formatted {
		var unformatted []string
		for _, f := range files {
			if slices.Contains(names, filepath.Base(f)) {
				unformatted = append(unformatted, filepath.Base(f))
			}
		}
		if len(unformatted) > 0 {
			return CheckFormat + ": " + strings.Join(unformatted, ", ") + " isn't formatted", nil
		}
	}
	validation, err := stf.Validate(ctx)
	if err != nil {
		return "", fmt.Errorf("terraform validate: %w", err)
	}
	if !validation.Valid {
		var details []string
		for _, d := range errorDiagnostics(validation.Diagnostics) {
			details = append(details, formatDiagnostic(d))
		}
		return CheckValidate + ": " + strings.Join(details, "; "), nil
	}
	nowStandards, err := checkStandards(staging, opts.Omit, nil)
	if err != nil {
		return "", err
	}
	if added := addedDetails(standards, nowStandards); len(added) > 0 {
		return CheckStandards + ": " + strings.Join(added, "; "), nil
	}
	nowMoves, err := checkMoves(staging)
	if err != nil {
		return "", err
	}
	if added := addedDetails(moves, nowMoves); len(added) > 0 {
		return CheckMoves + ": " + strings.Join(added, "; "), nil
	}
	values := newValueMatcher(append(sensitiveValues(p), sensitive...))
	for _, name := range names {
		var before, after Check
		scanFile(&before, values, name, string(original[name]))
		scanFile(&after, values, name, string(edited[name]))
		if len(addedDetails(before, after)) > 0 {
			// What it found is a secret's whereabouts, not for a report.
			return CheckSecrets + ": " + name + " would hold a secret", nil
		}
	}
	return "", nil
}

// addedDetails returns the details of after that before doesn't have.
func addedDetails(before, after Check) []string {
	had := map[string]int{}
	for _, d := range before.Details {
		had[d]++
	}
	var added []string
	for _, d := range after.Details {
		if had[d] > 0 {
			had[d]--
			continue
		}
		added = append(added, d)
	}
	return added
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
