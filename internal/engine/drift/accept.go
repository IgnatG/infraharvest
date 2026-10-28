// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

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

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// decideDrift sets each change's decision, from accept (nil keeps them
// all), and the outcome of each change the next apply would revert: kept,
// or, accepted, a secret or a hand edit where sources, by change, say so.
// It returns the accepted changes the code can take by an edit.
func decideDrift(changes []Change, sources []codeSource, accept func(Change) bool) []*Change {
	var accepted []*Change
	for i := range changes {
		c := &changes[i]
		c.Decision = Keep
		if accept != nil && accept(*c) {
			c.Decision = Accept
		}
		if c.Outcome != Reverts {
			continue
		}
		src := sources[i]
		switch {
		case c.Decision == Keep:
			c.Outcome = Kept
		case src.Secret:
			c.Outcome, c.Reason = Secret, src.Reason
		case src.Edit == nil:
			c.Outcome, c.Reason = Manual, src.Reason
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
func settleDrift(changes []*Change, failed map[*Change]codeSource) ([]*Change, bool) {
	var left []*Change
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
			c.Outcome, c.Reason = Secret, src.Reason
		default:
			c.Outcome, c.Reason = Manual, src.Reason
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
// other resource must plan what code has it plan (see
// tfrun.ChangeSignature), which an edit of a local others use would
// break. Then the edited files must pass the format, validate, standards,
// moves and secrets checks (see driftChecks). The edits the plan or a
// check blames are left out (see judgeDrift and checkBlame; one that
// blames no file blames them all), and the others planned and checked
// again, in at most as many rounds as there are edits (see acceptance and
// editRounds); a change of shared tags whose edit is blamed, or can't be
// made, is accepted at each resource it covers instead (see unshare).
// Only then are the edits made in the root's own files, which are
// restored if writing one fails (see writeAccepted). For a root
// environments share, root and the copy's files hold the environment's
// var file too, the only file edited (see variableResolver.env), and a
// change written gets its source there. Each accepted change ends
// written, rejected by the plan or a check, a hand edit or a secret.
func acceptDrift(ctx context.Context, stf tfrun.DriftTerraform, dir, staging string, root map[string][]byte, accepted []*Change, code *tfjson.Plan, vars []tfexec.PlanOption, editor *driftEditor, opts Options) error {
	a, err := newAcceptance(ctx, stf, staging, root, accepted, code, vars, editor, opts)
	if err != nil {
		return err
	}
	rounds := editRounds(accepted)
	passed, err := gate.Verifier[*Change, driftRejection]{
		Edit:   a.edit,
		Verify: a.verify,
		Drop:   a.drop,
		// A check that can't tell which edited files fail it fails them
		// all.
		BlameAll: true,
		Rounds:   rounds,
	}.Run(ctx)
	if err != nil {
		return err
	}
	if !passed {
		// Only the bound, which no round reaches, leaves changes pending.
		for _, c := range a.pending {
			c.Outcome, c.Reason = RejectedByPlan, fmt.Sprintf("the edits weren't settled in %d plans", rounds)
		}
		return nil
	}
	return writeAccepted(dir, root, a.pending, editor, opts)
}

// An acceptance is the rounds in which acceptDrift makes the edits of the
// accepted changes in the staged copy, plans them and checks them (see
// gate.Verifier).
type acceptance struct {
	stf      tfrun.DriftTerraform
	staging  string
	code     *tfjson.Plan
	planOpts []tfexec.PlanOption
	editor   *driftEditor
	opts     Options
	// staged are the copy's files as they were, the modules' among them,
	// and standards and moves the checks of them, which the edited files
	// may add no finding to.
	staged           map[string][]byte
	standards, moves gate.Check
	// pending are the changes still in play, and written the files a
	// round edited.
	pending []*Change
	written map[string]bool
	// edited are the files as the last round's edits left them, names
	// those it edited, and applied the sources it edited by.
	edited  map[string][]byte
	names   []string
	applied map[*Change]codeSource
}

// newAcceptance returns the acceptance of accepted (see acceptDrift),
// with the schemas stf has for the editor, and the checks of the staged
// copy as it is.
func newAcceptance(ctx context.Context, stf tfrun.DriftTerraform, staging string, root map[string][]byte, accepted []*Change, code *tfjson.Plan, vars []tfexec.PlanOption, editor *driftEditor, opts Options) (*acceptance, error) {
	schemas, err := stf.ProvidersSchema(ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform providers schema: %w", err)
	}
	editor.schemas = schemas
	staged, err := driftContents(staging, opts.Environment)
	if err != nil {
		return nil, err
	}
	// The modules' files, which the copy has copies of (see stageModule).
	for name, content := range root {
		if isModuleFile(name) {
			staged[name] = content
		}
	}
	standards, err := opts.Rules.Standards(staging)
	if err != nil {
		return nil, err
	}
	moves, err := opts.Rules.Moves(staging)
	if err != nil {
		return nil, err
	}
	return &acceptance{
		stf: stf, staging: staging, code: code, editor: editor, opts: opts,
		planOpts: append(slices.Clone(vars), tfexec.Refresh(false), tfexec.Lock(false)),
		staged:   staged, standards: standards, moves: moves,
		pending: accepted, written: map[string]bool{},
	}, nil
}

// editRounds bounds the rounds of accepting accepted: each round that
// fails rejects an edit, or has a change of shared tags edited at each
// resource it covers instead, so no round makes progress without
// dropping an edit, and the rounds can't outnumber the edits, those of
// the shared tags' members counted.
func editRounds(accepted []*Change) int {
	rounds := 0
	for _, c := range accepted {
		rounds++
		if c.shared != nil {
			rounds += len(c.shared.members)
		}
	}
	return rounds
}

// edit makes the edits of the pending changes on the copy's files as they
// were, and writes the files any round edited into the copy, so that
// those an earlier round edited are as they were, but for this round's
// edits. It returns the changes it edited; none once every one is
// settled.
func (a *acceptance) edit(context.Context) ([]*Change, error) {
	// A change of shared tags whose edit can't be made has its members
	// edited before anything is planned.
	for unshared := true; unshared && len(a.pending) > 0; {
		a.edited = maps.Clone(a.staged)
		var failed map[*Change]codeSource
		var err error
		a.names, a.applied, failed, err = a.editor.apply(a.edited, a.pending)
		if err != nil {
			return nil, err
		}
		a.pending, unshared = settleDrift(a.pending, failed)
	}
	if len(a.pending) == 0 {
		return nil, nil
	}
	for _, name := range a.names {
		a.written[name] = true
	}
	for _, name := range slices.Sorted(maps.Keys(a.written)) {
		if err := fsutil.WriteFile(stagedPath(a.staging, name), a.edited[name], 0o600); err != nil {
			return nil, err
		}
	}
	return a.pending, nil
}

// verify plans the edited copy and checks its edited files: nil if both
// take the round's edits, else the verdict that rejects those it blames
// (see judgeDrift and checkBlame).
func (a *acceptance) verify(ctx context.Context) (*gate.Verdict[*Change, driftRejection], error) {
	forms := a.editor.secretForms
	p, _, diags, err := tfrun.ShowPlan(ctx, a.stf, a.planOpts)
	if err != nil {
		return nil, secret.RedactError(err, forms)
	}
	if rejected := judgeDrift(p, diags, a.code, a.pending, a.applied, a.opts.StateOnly, a.edited, forms); len(rejected) > 0 {
		return planRejection(rejected), nil
	}
	failedCheck, files, err := driftChecks(ctx, a.stf, a.staging, a.staged, a.edited, a.names, p, a.standards, a.moves, a.editor.sensitive, a.opts)
	if err != nil || failedCheck == "" {
		return nil, err
	}
	failure := &gate.Verdict[*Change, driftRejection]{
		Blamed: map[*Change]driftRejection{},
		Why:    driftRejection{RejectedByCheck, "the edited code fails " + secret.RedactText(failedCheck, forms)},
	}
	for _, c := range checkBlame(a.pending, a.applied, files) {
		failure.Blamed[c] = failure.Why
	}
	return failure, nil
}

// drop takes the changes rejected out of play, each with its outcome and
// reason, but a change of shared tags, whose resources are each edited on
// their own instead.
func (a *acceptance) drop(rejected map[*Change]driftRejection) {
	var left []*Change
	for _, c := range a.pending {
		r, ok := rejected[c]
		switch {
		case !ok:
			left = append(left, c)
		case c.shared != nil:
			left = append(left, unshare(c, r.reason)...)
		default:
			c.Outcome, c.Reason = r.outcome, r.reason
		}
	}
	a.pending = left
}

// writeAccepted makes the edits of pending, which the plan and the checks
// of the copy took, in root, the files of the root in dir, writes them,
// and sets each change's outcome: written, with, for a root environments
// share, its source in the environment's var file; or, where the root's
// files don't take the same edits, rejected by the plan.
func writeAccepted(dir string, root map[string][]byte, pending []*Change, editor *driftEditor, opts Options) error {
	out := maps.Clone(root)
	names, applied, failed, err := editor.apply(out, pending)
	if err != nil {
		return err
	}
	if len(failed) > 0 {
		// The root's files are the staged copy's, but for its backend and
		// the sources of local modules: the same edits must apply.
		for _, c := range pending {
			c.Outcome, c.Reason = RejectedByPlan, "the root's files differ from the copy the edits were planned in: edit them by hand"
		}
		return nil
	}
	if err := writeDriftFiles(dir, out, names, opts.writeFile); err != nil {
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
		c.Outcome, c.Reason = Written, applied[c].Note
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
	backup, err := fsutil.BackupFiles(dir, names...)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := write(filepath.Join(dir, name), contents[name], 0o644); err != nil {
			werr := fmt.Errorf("write the accepted changes into %s: %w", name, err)
			if rerr := backup.Restore(); rerr != nil {
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
// other than code has it (see rejections.blameLeak). A change of shared
// tags is judged on every resource it covers. Reasons have the values of
// forms redacted.
func judgeDrift(p *tfjson.Plan, diags []tfjson.Diagnostic, code *tfjson.Plan, pending []*Change, sources map[*Change]codeSource, stateOnly map[string][]string, contents map[string][]byte, forms []string) map[*Change]string {
	r := rejections{reasons: map[*Change]string{}, forms: forms}
	if len(diags) > 0 || p == nil {
		r.planFails(diags, pending, sources, contents)
		return r.reasons
	}
	baseline := managedChanges(code)
	byAddress := map[string][]*Change{}
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
		r.judgeResource(rc, base, cs, stateOnly)
	}
	if len(leaked) > 0 {
		r.blameLeak(leaked, pending, sources, contents)
	}
	return r.reasons
}

// rejections are the changes the plan of edited code rejects, each with
// the first reason it rejects it for, with the values of forms redacted
// (see judgeDrift).
type rejections struct {
	reasons map[*Change]string
	forms   []string
}

// reject rejects c for reason, unless it is rejected already.
func (r rejections) reject(c *Change, reason string) {
	if _, ok := r.reasons[c]; !ok {
		r.reasons[c] = secret.RedactText(reason, r.forms)
	}
}

// planFails rejects the changes of pending for the errors of a plan that
// failed, diags: each error the changes whose edits, made as sources say
// in contents, it is about (see diagnosticOf), or all of them where it is
// about none; and every change where the plan reported neither errors nor
// a change summary.
func (r rejections) planFails(diags []tfjson.Diagnostic, pending []*Change, sources map[*Change]codeSource, contents map[string][]byte) {
	for _, d := range diags {
		reason := "the plan of the edited code fails: " + strings.Join(strings.Fields(tfrun.FormatDiagnostic(d)), " ")
		blamed := false
		for _, c := range pending {
			if diagnosticOf(d, c, sources[c], contents) {
				r.reject(c, reason)
				blamed = true
			}
		}
		if !blamed {
			for _, c := range pending {
				r.reject(c, reason)
			}
		}
	}
	if len(diags) == 0 {
		for _, c := range pending {
			r.reject(c, "terraform plan reported no change summary")
		}
	}
}

// managedChanges returns the planned changes of the managed resources of
// p, by address; none for no plan.
func managedChanges(p *tfjson.Plan) map[string]*tfjson.ResourceChange {
	changes := map[string]*tfjson.ResourceChange{}
	if p == nil {
		return changes
	}
	for _, rc := range p.ResourceChanges {
		if rc.Mode == tfjson.ManagedResourceMode && rc.Change != nil {
			changes[rc.Address] = rc
		}
	}
	return changes
}

// judgeResource rejects the changes cs of the resource the plan of the
// edited code plans rc for: those of an attribute it still changes; all
// of them where it changes another attribute that base, the resource's
// change in the plan of the code, didn't, but for what is secret or kept
// only in state, or a key of its own tags or labels that only the state
// records (see recordedTagLeaves); and all of them where it replaces the
// resource and base didn't.
func (r rejections) judgeResource(rc, base *tfjson.ResourceChange, cs []*Change, stateOnly map[string][]string) {
	if rc.Change.Actions.NoOp() {
		return
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
				r.reject(c, "the plan of the edited code still changes "+l.path)
				matched = true
			}
		}
		if !matched && !baseLeaves[l.path] {
			for _, c := range cs {
				r.reject(c, "the plan of the edited code changes "+l.path+" too")
			}
		}
	}
	if rc.Change.Actions.Replace() && (base == nil || !base.Change.Actions.Replace()) {
		for _, c := range cs {
			r.reject(c, "the plan of the edited code replaces the resource")
		}
	}
}

// blameLeak rejects the changes of pending whose edits, made as sources
// say in contents, the plan of other resources, leaked, changing is
// blamed on: those of locals, of shared tags and of module calls'
// arguments, which reach every resource the module makes from them,
// first, then those of the resources the leaked ones refer to, else all
// of them.
func (r rejections) blameLeak(leaked []string, pending []*Change, sources map[*Change]codeSource, contents map[string][]byte) {
	reason := "the edit changes what the plan does to " + strings.Join(leaked, ", ") + " too"
	blamed := false
	for _, c := range pending {
		if src := sources[c]; src.Kind == sourceLocal || src.Kind == sourceSharedTags || src.Module != "" {
			r.reject(c, reason)
			blamed = true
		}
	}
	if !blamed {
		referred := referredTo(contents, leaked)
		for _, c := range pending {
			if referred[hclfile.TrimInstanceKey(c.Address)] {
				r.reject(c, reason)
				blamed = true
			}
		}
	}
	if !blamed {
		for _, c := range pending {
			r.reject(c, reason)
		}
	}
}

// sameAsBaseline reports whether rc plans what base, the plan of the
// code before the edits, has for the resource: nothing if base has none.
func sameAsBaseline(rc, base *tfjson.ResourceChange) bool {
	if base == nil {
		return rc.Change.Actions.NoOp() || rc.Change.Actions.Read()
	}
	return tfrun.ChangeSignature(rc) == tfrun.ChangeSignature(base)
}

// planLeaves returns the attributes a plan changes for rc, as drift
// counts them (see driftLeaves).
func planLeaves(rc *tfjson.ResourceChange, stateOnly map[string][]string) []valueLeaf {
	return driftLeaves(rc.Type, valueLeaves(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown, rc.Change.BeforeSensitive, rc.Change.AfterSensitive), stateOnly)
}

// diagnosticOf reports whether d is about the edit of c, made as src
// says in contents: in its resource, or in the file and block it edits.
func diagnosticOf(d tfjson.Diagnostic, c *Change, src codeSource, contents map[string][]byte) bool {
	if c.shared != nil {
		if d.Address != "" {
			return slices.ContainsFunc(c.Covers, func(a string) bool { return hclfile.TrimInstanceKey(a) == hclfile.TrimInstanceKey(d.Address) })
		}
		return d.Range != nil && src.Edit != nil && filepath.Base(d.Range.Filename) == src.Edit.File
	}
	if d.Address != "" {
		return hclfile.TrimInstanceKey(d.Address) == hclfile.TrimInstanceKey(c.Address)
	}
	if d.Range == nil || src.Edit == nil || filepath.Base(d.Range.Filename) != src.Edit.File {
		return false
	}
	f, err := hclfile.Parse(src.Edit.File, contents[src.Edit.File])
	if err != nil {
		return true
	}
	if address := f.ResourceAt(d.Range.Start.Line); address != "" {
		return address == hclfile.TrimInstanceKey(c.Address)
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
		b, ok := cfg.blocks[hclfile.TrimInstanceKey(address)]
		if !ok {
			continue
		}
		for _, t := range hclfile.Traversals(b.b.Body) {
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
func driftChecks(ctx context.Context, stf tfrun.DriftTerraform, staging string, original, edited map[string][]byte, names []string, p *tfjson.Plan, standards, moves gate.Check, sensitive []string, opts Options) (string, []string, error) {
	policy := gate.Policy{Standards: opts.Rules.Standards, Moves: opts.Rules.Moves}
	policy.Checks = []string{gate.CheckFormat, gate.CheckValidate, gate.CheckStandards, gate.CheckMoves, gate.CheckSecrets}
	policy.Sensitive = append(secret.InPlan(p), sensitive...)
	policy.Baseline = gate.Gate{standards, moves}
	policy.Diff = true
	policy.Edited = names
	policy.Before, policy.After = original, edited
	checks, _, err := policy.Run(ctx, stf, staging)
	if err != nil {
		return "", nil, err
	}
	for _, c := range checks {
		if !c.Passed {
			return c.Name + ": " + strings.Join(c.Details, "; "), c.Files, nil
		}
	}
	return "", nil, nil
}

// checkBlame returns the changes of pending whose edits, made as sources
// say, are in files, the edited files a check fails on.
func checkBlame(pending []*Change, sources map[*Change]codeSource, files []string) []*Change {
	var blamed []*Change
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
func planRejection(rejected map[*Change]string) *gate.Verdict[*Change, driftRejection] {
	failure := &gate.Verdict[*Change, driftRejection]{
		Blamed: map[*Change]driftRejection{},
		Why:    driftRejection{RejectedByPlan, "the plan of the edited code doesn't take the edits"},
	}
	for c, reason := range rejected {
		failure.Blamed[c] = driftRejection{RejectedByPlan, reason}
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
