// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

// Names of the verification gate's checks, numbered G1 to G8 (see also
// CheckStandards, CheckScanners and CheckMoves).
const (
	CheckFormat      = "G1 format"
	CheckValidate    = "G2 validate"
	CheckPlan        = "G3 plan"
	CheckSecrets     = "G6 secrets"
	CheckDeterminism = "G7 determinism"
)

// Check is the outcome of one check of the verification gate.
type Check struct {
	Name   string
	Passed bool
	// Skipped checks couldn't run, such as the scanners check with no
	// scanner installed: they checked nothing, so they are shown as
	// skipped, but they don't fail the gate, so Passed is set too.
	Skipped bool
	// Details explain a failure, or note what a pass allowed.
	Details []string
	// Changes are the changes a plan check found, for CheckPlan.
	Changes []PlannedChange
	// files are the edited files a failure is blamed on, where the gate
	// blames edited files (see gatePolicy.diff).
	files []string
}

// PlannedChange is a change the plan has for a resource besides importing
// it.
type PlannedChange struct {
	Address    string
	Actions    []string
	Attributes []string // top-level arguments an update changes
}

// Gate is the verification gate's result for one directory.
type Gate []Check

// Passed reports whether every check passed.
func (g Gate) Passed() bool {
	for _, c := range g {
		if !c.Passed {
			return false
		}
	}
	return true
}

// check returns the check named name; one that didn't run fails.
func (g Gate) check(name string) Check {
	for _, c := range g {
		if c.Name == name {
			return c
		}
	}
	return Check{Name: name, Details: []string{"didn't run"}}
}

// runGate runs the import gate on the directory as Generate leaves it:
// every check of the gate (see fullGate), with a plan that imports each
// resource an import block names and changes nothing else (see
// planCheck). Secret variables Terraform doesn't show get placeholder
// values in the plan, so the arguments they set may change; so may the
// arguments opts.StateOnly names per type, which providers keep only in
// state and import can't set. known, if set, has the values of the
// secrets Generate found (see secretValues).
func runGate(ctx context.Context, tf Terraform, dir string, secrets []Secret, opts Options, known *secretValues) (Gate, error) {
	expected, err := countImportBlocks(dir)
	if err != nil {
		return nil, err
	}
	policy := fullGate(opts)
	policy.plan = func() (Check, []string, error) {
		p, _, diags, err := showPlanWithSummary(ctx, tf, append(placeholders(secrets), known.options()...))
		if err != nil {
			return Check{}, nil, err
		}
		return planCheck(p, diags, secrets, opts.StateOnly, expected), append(sensitiveValues(p), knownValues(secrets)...), nil
	}
	gate, _, err := policy.run(ctx, tf, dir)
	return gate, err
}

// countImportBlocks counts the import blocks in dir's configuration: how
// many resources its plan must import.
func countImportBlocks(dir string) (int, error) {
	files, err := configFiles(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			if b.Type == "import" {
				n++
			}
		}
	}
	return n, nil
}

// gatePolicy is how a run of the verification gate checks a directory:
// which of the gate's checks run, and what they judge by. The import
// gate (see runGate), Verify's and VerifyMerged's run every check; Add's
// holds the scanners back on the secrets staging's plan found (see
// withheld); a stack's merged root is planned as each environment, and
// its scanners run once, if none of them finds a secret (see others);
// and drift judges its edits by what they add to the findings the root
// had, blaming the files they are in (see baseline and diff).
type gatePolicy struct {
	// checks are the names of the checks that run; none runs them all.
	checks []string
	// plan runs the plan check, and returns with it the values its plan
	// marks sensitive, which no file may hold.
	plan func() (Check, []string, error)
	// others plan the directory again after plan, as the other
	// environments of a stack: each has a gate of its own, of its plan
	// check and the secrets check of the values it marks sensitive, and
	// the scanners run only if each of those passes.
	others []gatePlan
	// sensitive are values no file may hold besides those the plans mark
	// sensitive.
	sensitive []string
	// withheld, if set, is a secrets check of values the directory
	// mustn't hold that the gate doesn't know, such as those another
	// directory's plan marks sensitive: the scanners run only if it
	// passes, and the gate's secrets check fails with what it found.
	withheld *Check
	// baseline, for diff mode, has checks of the directory before it was
	// edited: a check it has fails only on the findings it adds to them.
	baseline Gate
	// diff has the gate judge edits: edited are the files they changed,
	// by name, which before and after hold as they were and are, and the
	// gate blames its failures on them. The format check fails only on
	// these files, as the others are as they were; the secrets check scans
	// only these, failing on a finding an edit adds, without saying where
	// the secret is; a check that fails names the files it blames (see
	// Check.files), none where it can't tell; and the gate stops at the
	// first that fails, as the edits it blames are left out and the rest
	// checked again.
	diff          bool
	edited        []string
	before, after map[string][]byte
	// omit and own are the output standard's (see checkStandards), and
	// scanners are the external scanners to run (see runScanners).
	omit     map[string][]string
	own      []string
	scanners []Scanner
}

// gatePlan is one more plan of the directory a gate checks (see
// gatePolicy.others).
type gatePlan struct {
	// name is what the scanners' check, held back on its secrets, says of
	// it, such as "environment prod".
	name string
	run  func() (Check, []string, error)
}

// fullGate returns the policy that runs every check of the gate, to the
// output standard and with the scanners of opts; its plan is still to be
// set.
func fullGate(opts Options) gatePolicy {
	return gatePolicy{omit: opts.Omit, own: ownModuleDirs(opts.Adapters), scanners: opts.Scanners}
}

// runs reports whether the gate runs the check named name.
func (p gatePolicy) runs(name string) bool {
	return len(p.checks) == 0 || slices.Contains(p.checks, name)
}

// run runs the checks of the gate that p says on dir, with tf, and
// returns their results in the gate's order, which is the order they run
// in but for the scanners, listed before the secrets check that holds
// them back; and the gate of each of p.others. A cancelled run fails as
// cancelled.
func (p gatePolicy) run(ctx context.Context, tf Terraform, dir string) (Gate, []Gate, error) {
	gate, others, err := p.runChecks(ctx, tf, dir)
	if err != nil {
		return nil, nil, err
	}
	// Checks cut short, such as a scanner that couldn't run, say nothing
	// of dir: a cancelled gate fails as cancelled, not as failed checks.
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return gate, others, nil
}

// runChecks is run but for its cancellation.
func (p gatePolicy) runChecks(ctx context.Context, tf Terraform, dir string) (Gate, []Gate, error) {
	var gate Gate
	// add adds c to the gate, and reports whether the gate stops there.
	add := func(c Check) bool {
		gate = append(gate, c)
		return p.diff && !c.Passed
	}
	if p.runs(CheckFormat) {
		c, err := p.format(ctx, tf)
		if err != nil {
			return nil, nil, err
		}
		if add(c) {
			return gate, nil, nil
		}
	}
	if p.runs(CheckValidate) {
		c, err := p.validate(ctx, tf)
		if err != nil {
			return nil, nil, err
		}
		if add(c) {
			return gate, nil, nil
		}
	}
	sensitive := slices.Clone(p.sensitive)
	var others []Gate
	// clean is whether the secrets checks of the other plans pass, and
	// held names those that don't.
	clean := true
	var held []string
	if p.runs(CheckPlan) {
		planned, values, err := p.plan()
		if err != nil {
			return nil, nil, err
		}
		sensitive = append(sensitive, values...)
		for _, other := range p.others {
			otherPlanned, otherValues, err := other.run()
			if err != nil {
				return nil, nil, err
			}
			secrets, err := scanSecrets(dir, append(slices.Clone(p.sensitive), otherValues...))
			if err != nil {
				return nil, nil, err
			}
			others = append(others, Gate{otherPlanned, secrets})
			if !secrets.Passed {
				clean = false
				held = append(held, other.name)
			}
		}
		if add(planned) {
			return gate, others, nil
		}
	}
	for _, name := range []string{CheckStandards, CheckMoves} {
		if !p.runs(name) {
			continue
		}
		c, err := p.judged(name, dir)
		if err != nil {
			return nil, nil, err
		}
		if add(c) {
			return gate, others, nil
		}
	}
	// Secrets first, wherever the scanners run: external scanners are
	// other people's code, and some send what they scan elsewhere, so they
	// never see a directory that holds a secret value.
	if p.runs(CheckSecrets) || p.runs(CheckScanners) {
		secrets, err := p.secrets(dir, sensitive)
		if err != nil {
			return nil, nil, err
		}
		if p.runs(CheckScanners) {
			scanners := Check{Name: CheckScanners, Passed: true, Skipped: true, Details: []string{"not run, as " + CheckSecrets + " found secret values"}}
			if secrets.Passed {
				scanners.Details = append(scanners.Details, held...)
				if p.withheld != nil {
					clean = clean && p.withheld.Passed
					scanners.Details = append(scanners.Details, p.withheld.Details...)
				}
				if clean {
					scanners = runScanners(ctx, dir, p.scanners)
				}
			}
			if add(scanners) {
				return gate, others, nil
			}
		}
		if p.withheld != nil && !p.withheld.Passed {
			secrets.Passed = false
			secrets.Details = append(secrets.Details, p.withheld.Details...)
		}
		if p.runs(CheckSecrets) && add(secrets) {
			return gate, others, nil
		}
	}
	if p.runs(CheckDeterminism) {
		c, err := scanNondeterminism(dir)
		if err != nil {
			return nil, nil, err
		}
		add(c)
	}
	return gate, others, nil
}

// format runs the format check. Where the gate blames edited files, only
// those fail it.
func (p gatePolicy) format(ctx context.Context, tf Terraform) (Check, error) {
	formatted, files, err := tf.FormatCheck(ctx)
	if err != nil {
		return Check{}, fmt.Errorf("terraform fmt: %w", err)
	}
	if !p.diff {
		return Check{Name: CheckFormat, Passed: formatted, Details: files}, nil
	}
	c := Check{Name: CheckFormat, Passed: true}
	for _, f := range files {
		if slices.Contains(p.edited, filepath.Base(f)) {
			c.files = append(c.files, filepath.Base(f))
		}
	}
	if len(c.files) > 0 {
		c.Passed = false
		c.Details = []string{strings.Join(c.files, ", ") + " isn't formatted"}
	}
	return c, nil
}

// validate runs the validate check, blaming the edited files an error is
// in.
func (p gatePolicy) validate(ctx context.Context, tf Terraform) (Check, error) {
	validation, err := tf.Validate(ctx)
	if err != nil {
		return Check{}, fmt.Errorf("terraform validate: %w", err)
	}
	c := Check{Name: CheckValidate, Passed: validation.Valid}
	for _, d := range errorDiagnostics(validation.Diagnostics) {
		c.Details = append(c.Details, formatDiagnostic(d))
		if d.Range == nil {
			continue
		}
		for _, name := range p.edited {
			if name == filepath.ToSlash(d.Range.Filename) || name == filepath.Base(d.Range.Filename) {
				c.files = append(c.files, name)
			}
		}
	}
	return c, nil
}

// judged runs the standards or the moves check, named name, as diff mode
// judges it where the baseline has it: with only the findings it adds,
// failing if there are any, and blaming the edited files they name.
func (p gatePolicy) judged(name, dir string) (Check, error) {
	var c Check
	var err error
	if name == CheckStandards {
		c, err = checkStandards(dir, p.omit, p.own)
	} else {
		c, err = checkMoves(dir)
	}
	if err != nil {
		return Check{}, err
	}
	i := slices.IndexFunc(p.baseline, func(b Check) bool { return b.Name == name })
	if i < 0 {
		return c, nil
	}
	c.Details = addedDetails(p.baseline[i], c)
	c.Passed = len(c.Details) == 0
	c.files = filesNamed(c.Details, p.edited)
	return c, nil
}

// secrets runs the secrets check, of the values sensitive has: on the
// files of dir, or, where the gate blames edited files, on what an edit
// adds to each.
func (p gatePolicy) secrets(dir string, sensitive []string) (Check, error) {
	if !p.diff {
		return scanSecrets(dir, sensitive)
	}
	values := newValueMatcher(sensitive)
	for _, name := range p.edited {
		var before, after Check
		scanFile(&before, values, name, string(p.before[name]))
		scanFile(&after, values, name, string(p.after[name]))
		if len(addedDetails(before, after)) > 0 {
			// What it found is a secret's whereabouts, not for a report.
			return Check{Name: CheckSecrets, Details: []string{name + " would hold a secret"}, files: []string{name}}, nil
		}
	}
	return Check{Name: CheckSecrets, Passed: true}, nil
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

// filesNamed returns the files of names that details, a check's
// findings, name, such as main.tf in "main.tf:3: moved block has no to".
func filesNamed(details, names []string) []string {
	var files []string
	for _, name := range names {
		if slices.ContainsFunc(details, func(d string) bool {
			return slices.ContainsFunc(strings.Fields(d), func(field string) bool {
				file, _, _ := strings.Cut(field, ":")
				return file == name
			})
		}) {
			files = append(files, name)
		}
	}
	return files
}

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

// sensitiveValues returns the string values the plan marks sensitive in
// the imported objects: what must never be written into a file. A value
// publicSensitive says isn't secret, such as a String parameter's, is left
// out, and so is one that is also the whole of an attribute the plan
// doesn't mark sensitive, where it plainly isn't a secret (see
// structural), such as an SSM parameter (whose value the provider always
// marks sensitive) holding a region name.
func sensitiveValues(p *tfjson.Plan) []string {
	if p == nil {
		return nil
	}
	seen, public, notSecret := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, rc := range p.ResourceChanges {
		if rc.Change != nil {
			collectSensitive(rc.Change.Before, rc.Change.BeforeSensitive, seen)
			collectPublic(rc.Change.Before, rc.Change.BeforeSensitive, public)
			if v, ok := publicValue(rc); ok {
				notSecret[v] = true
			}
		}
	}
	values := make([]string, 0, len(seen))
	for v := range seen {
		if !notSecret[v] && (!public[v] || !structural(v)) {
			values = append(values, v)
		}
	}
	sort.Strings(values)
	return values
}

// identifierLike is what names and IDs look like: lower-case words and
// digits joined by dashes, such as eu-west-2, vpc-0abc1234 or app-prod.
var identifierLike = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)+$`)

// structural reports whether a value the plan marks sensitive somewhere
// is plainly not a secret where another attribute holds it in the open:
// it holds none (see noSecret), it is a number or text with spaces, or it
// looks like a region, a name or an ID. A value that could be a password
// or a token, with upper case, punctuation or the randomness of a key,
// stays a secret even then, as a copy of a secret in an attribute the
// provider doesn't mark is the leak G6 is for.
func structural(v string) bool {
	return plainValue(v) || identifierLike.MatchString(v)
}

// collectPublic adds the strings in value that mask doesn't mark sensitive.
func collectPublic(value, mask any, out map[string]bool) {
	if marked, ok := mask.(bool); ok && marked {
		return
	}
	switch v := value.(type) {
	case string:
		out[v] = true
	case map[string]any:
		m, _ := mask.(map[string]any)
		for k, sub := range v {
			collectPublic(sub, m[k], out)
		}
	case []any:
		m, _ := mask.([]any)
		for i, sub := range v {
			var subMask any
			if i < len(m) {
				subMask = m[i]
			}
			collectPublic(sub, subMask, out)
		}
	}
}

// collectSensitive adds the strings in value that mask marks sensitive.
func collectSensitive(value, mask any, out map[string]bool) {
	if marked, ok := mask.(bool); ok && marked {
		addStrings(value, out)
		return
	}
	switch m := mask.(type) {
	case map[string]any:
		v, _ := value.(map[string]any)
		for k, sub := range m {
			collectSensitive(v[k], sub, out)
		}
	case []any:
		v, _ := value.([]any)
		for i, sub := range m {
			if i < len(v) {
				collectSensitive(v[i], sub, out)
			}
		}
	}
}

// minSecretLength leaves out values too short to tell apart from other
// text, such as "true".
const minSecretLength = 6

func addStrings(value any, out map[string]bool) {
	switch v := value.(type) {
	case string:
		if len(v) >= minSecretLength {
			out[v] = true
		}
	case map[string]any:
		for _, sub := range v {
			addStrings(sub, out)
		}
	case []any:
		for _, sub := range v {
			addStrings(sub, out)
		}
	}
}

// credentialPatterns are formats of credentials that never belong in
// configuration, sorted by name, so that findings come in the same order
// every run. scanFile looks for them in every file the gate reads, a
// README as much as a .tf file, and credentialFormat in every single value.
// An AWS access key ID on its own isn't a credential (see awsKeyPair).
var credentialPatterns = []credentialPattern{
	credential("API key", `\bsk-(ant-|proj-)?[A-Za-z0-9_-]{20,}\b`),
	credential("Azure SAS token", `[?&]sig=[A-Za-z0-9%+/=]{20,}`),
	credential("Azure shared access key", `SharedAccessKey=[A-Za-z0-9+/=]{20,}`),
	credential("Azure storage key", `AccountKey=[A-Za-z0-9+/=]{40,}`),
	wholeValueCredential("Discord webhook", `https://(ptb\.|canary\.)?discord(app)?\.com/api/webhooks/[0-9]+/[A-Za-z0-9_-]{20,}`),
	credential("GitHub fine-grained token", `\bgithub_pat_[A-Za-z0-9_]{40,}\b`),
	credential("GitHub token", `\bgh[pousr]_[A-Za-z0-9]{36,}\b`),
	credential("GitLab token", `\bglpat-[A-Za-z0-9_-]{20,}\b`),
	credential("Google API key", `\bAIza[0-9A-Za-z_-]{35}\b`),
	credential("JSON Web Token", `\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
	wholeValueCredential("Microsoft Teams webhook", `https://([A-Za-z0-9-]+\.webhook|outlook)\.office(365)?\.com/webhook(b2)?/\S{20,}`),
	credential("SendGrid key", `\bSG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}\b`),
	credential("Slack token", `\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
	wholeValueCredential("Slack webhook", `https://hooks\.slack\.com/services/[A-Za-z0-9/]{20,}`),
	credential("Stripe restricted key", `\brk_live_[0-9A-Za-z]{20,}\b`),
	credential("Stripe secret key", `\bsk_live_[0-9A-Za-z]{20,}\b`),
	wholeValueCredential("Zapier webhook", `https://hooks\.zapier\.com/hooks/catch/[0-9]+/[A-Za-z0-9]+`),
	credential("npm token", `\bnpm_[A-Za-z0-9]{36}\b`),
	credential("password in a URL", `[a-z][a-z0-9+.-]*://[^/\s:@"]+:[^/\s@"]+@`),
	valueCredential("password in a connection string", `(?im)(^|;)\s*(password|pwd)\s*=\s*[^;\s]+`),
	credential("private key", `-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

// credentialPattern is a format of credential in credentialPatterns.
type credentialPattern struct {
	name string
	// inFile matches the credential anywhere in a file; nil for a format
	// that only a single value tells (see valueCredential).
	inFile *regexp.Regexp
	// inValue matches it in a single value.
	inValue *regexp.Regexp
}

// credential is a format found the same way anywhere in a file or a value.
func credential(name, pattern string) credentialPattern {
	re := regexp.MustCompile(pattern)
	return credentialPattern{name: name, inFile: re, inValue: re}
}

// wholeValueCredential is a format, such as a webhook URL, that is the
// whole of a value that holds it, so a value must start with it; a file
// can hold it anywhere.
func wholeValueCredential(name, pattern string) credentialPattern {
	return credentialPattern{name: name, inFile: regexp.MustCompile(pattern), inValue: regexp.MustCompile("^" + pattern)}
}

// valueCredential is a format that only a single value tells: a password
// in a connection string, which in a file would match every password
// argument, such as one set from a variable.
func valueCredential(name, pattern string) credentialPattern {
	return credentialPattern{name: name, inValue: regexp.MustCompile(pattern)}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// scanSecrets checks every file Generate wrote into dir, and into the
// local modules it calls (see walkOutputAndModules), for the plan's
// sensitive values and the secret values Generate found, for credential
// formats, for provider blocks that set a credential argument to a
// constant (see isCredential), and for any secret value written in
// the configuration (see literalSecrets).
func scanSecrets(dir string, sensitive []string) (Check, error) {
	check := Check{Name: CheckSecrets, Passed: true}
	values := newValueMatcher(sensitive)
	err := walkOutputAndModules(dir, func(rel string, content string) {
		scanFile(&check, values, rel, content)
	})
	return check, err
}

// valueMatcher finds whether a text holds any of a set of values, in one
// pass over the text however many values there are: each run of
// minSecretLength bytes of the text is looked up among the values' first
// bytes, and only the values that start so are compared. A root may have
// thousands of sensitive values, and a strings.Contains for each would read
// every file that many times.
type valueMatcher struct {
	byPrefix map[uint64][]string
	// short are the values shorter than minSecretLength, compared one by
	// one.
	short []string
}

func newValueMatcher(values []string) valueMatcher {
	m := valueMatcher{byPrefix: map[uint64][]string{}}
	for _, v := range values {
		if len(v) < minSecretLength {
			m.short = append(m.short, v)
			continue
		}
		key := prefixKey(v)
		m.byPrefix[key] = append(m.byPrefix[key], v)
	}
	return m
}

// prefixKey packs the first minSecretLength bytes of s into a number.
func prefixKey(s string) uint64 {
	var key uint64
	for i := range minSecretLength {
		key = key<<8 | uint64(s[i])
	}
	return key
}

// in reports whether content holds one of the values.
func (m valueMatcher) in(content string) bool {
	for _, v := range m.short {
		if strings.Contains(content, v) {
			return true
		}
	}
	if len(m.byPrefix) == 0 {
		return false
	}
	for i := 0; i+minSecretLength <= len(content); i++ {
		for _, v := range m.byPrefix[prefixKey(content[i:])] {
			if strings.HasPrefix(content[i:], v) {
				return true
			}
		}
	}
	return false
}

// scanFile runs scanSecrets' checks on one file, rel, adding what it finds
// to check.
func scanFile(check *Check, sensitive valueMatcher, rel, content string) {
	if sensitive.in(content) {
		check.Passed = false
		check.Details = append(check.Details, rel+": contains a value the provider marks sensitive")
	}
	for _, p := range credentialPatterns {
		if p.inFile != nil && p.inFile.MatchString(content) {
			check.Passed = false
			check.Details = append(check.Details, rel+": looks like it contains a "+p.name)
		}
	}
	if awsKeyPair(content) {
		check.Passed = false
		check.Details = append(check.Details, rel+": looks like it contains an AWS access key ID with its secret access key")
	}
	// Parsed once for the checks below, and only a file they read:
	// parsing is most of what a scan costs. A file that doesn't parse
	// has nothing to scan, since validate (G2) reports it.
	if !readsLiteralSecrets(rel) {
		return
	}
	parsed, err := parseHCL(rel, []byte(content))
	if err != nil {
		return
	}
	for _, name := range providerCredentials(parsed) {
		check.Passed = false
		check.Details = append(check.Details, rel+": provider block sets "+name+", which belongs in the provider's environment")
	}
	// What Generate moves into variables, should any remain or come
	// back with an edit.
	if findings := literalSecrets(parsed); len(findings) > 0 {
		check.Passed = false
		check.Details = append(check.Details, findings...)
	}
}

// checkReadme runs scanSecrets' checks on the README Generate wrote after
// the gate ran, which the gate couldn't see, with values the secret values
// no file may contain. Should the README hold one, G6 fails and the README
// is written again to say so.
func checkReadme(dir string, result *Result, values []string) error {
	content, err := os.ReadFile(filepath.Join(dir, ReadmeFileName))
	if err != nil {
		return err
	}
	found := Check{Passed: true}
	scanFile(&found, newValueMatcher(values), ReadmeFileName, string(content))
	if found.Passed {
		return nil
	}
	for i, c := range result.Gate {
		if c.Name == CheckSecrets {
			result.Gate[i].Passed = false
			result.Gate[i].Details = append(result.Gate[i].Details, found.Details...)
		}
	}
	return WriteReadme(dir, result)
}

// providerCredentials returns the credential arguments the provider blocks
// of a .tf file set to constants, sorted, as "provider.argument".
func providerCredentials(f *hclFile) []string {
	if !strings.HasSuffix(f.path, ".tf") {
		return nil
	}
	var names []string
	for _, b := range f.syntax.Blocks {
		if b.Type != "provider" || len(b.Labels) != 1 {
			continue
		}
		for name, attr := range b.Body.Attributes {
			if !isCredential(name) {
				continue
			}
			if v, diags := attr.Expr.Value(nil); !diags.HasErrors() && v.IsWhollyKnown() {
				names = append(names, b.Labels[0]+"."+name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// nondeterministic are functions whose results differ between runs.
var nondeterministic = map[string]bool{"timestamp": true, "plantimestamp": true, "uuid": true, "uuidv5": true, "bcrypt": true}

// scanNondeterminism checks the configuration Generate wrote, with the
// local modules it calls (see walkOutputAndModules), calls no
// function whose result differs between runs, so that importing an
// unchanged estate again gives the same files.
func scanNondeterminism(dir string) (Check, error) {
	check := Check{Name: CheckDeterminism, Passed: true}
	err := walkOutputAndModules(dir, func(rel string, content string) {
		for _, call := range nondeterministicCalls(rel, content) {
			check.Passed = false
			check.Details = append(check.Details, rel+": calls "+call+"(")
		}
	})
	return check, err
}

// nondeterministicCalls returns the nondeterministic functions a .tf
// file's expressions call, each once, sorted. Strings and comments that
// only mention a function's name don't count.
func nondeterministicCalls(rel, content string) []string {
	if !strings.HasSuffix(rel, ".tf") {
		return nil
	}
	// A file that doesn't parse has nothing to scan, since validate (G2)
	// reports it.
	f, err := parseHCL(rel, []byte(content))
	if err != nil {
		return nil
	}
	calls := map[string]bool{}
	_ = hclsyntax.VisitAll(f.syntax, func(node hclsyntax.Node) hcl.Diagnostics {
		if call, ok := node.(*hclsyntax.FunctionCallExpr); ok && nondeterministic[call.Name] {
			calls[call.Name] = true
		}
		return nil
	})
	return sortedKeys(calls)
}

// walkOutput calls visit with each file Generate wrote into dir, by path
// relative to dir, skipping Terraform's working files, and the local state
// of a root applied without a backend (see LocalStateFile), which no
// check reads: it holds the secret values the configuration mustn't.
func walkOutput(dir string, visit func(rel, content string)) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".terraform" || d.Name() == workspaceStateDir {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == LockFileName || LocalStateFile(d.Name()) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		visit(filepath.ToSlash(rel), string(content))
		return nil
	})
}

// walkOutputAndModules is walkOutput on dir and then on each local module
// it calls outside it (see calledModules), such as the shared modules
// under the output directory's modules directory. The files of a module
// are named by their path relative to dir, as in ../../modules/vpc/main.tf.
func walkOutputAndModules(dir string, visit func(rel, content string)) error {
	if err := walkOutput(dir, visit); err != nil {
		return err
	}
	modules, err := calledModules(dir)
	if err != nil {
		return err
	}
	for _, m := range modules {
		err := walkOutput(m, func(path, content string) {
			visit(rel(dir, filepath.Join(m, filepath.FromSlash(path))), content)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// calledModules returns the directories of the local modules that dir's
// configuration calls, and that those call in turn, other than those under
// dir, which walkOutput covers already; sorted. A call whose source isn't
// a constant local path, or whose directory doesn't exist, is skipped, as
// is a file that doesn't parse: validate (G2) reports those.
func calledModules(dir string) ([]string, error) {
	seen := map[string]bool{}
	var walk func(d string) error
	walk = func(d string) error {
		paths, err := filepath.Glob(filepath.Join(d, "*.tf"))
		if err != nil {
			return err
		}
		sort.Strings(paths)
		for _, path := range paths {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			f, err := parseHCL(path, content)
			if err != nil {
				continue
			}
			for _, b := range f.syntax.Blocks {
				m := localModuleDir(d, b)
				if m == "" || seen[m] {
					continue
				}
				if info, err := os.Stat(m); err != nil || !info.IsDir() {
					continue
				}
				seen[m] = true
				if err := walk(m); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(dir); err != nil {
		return nil, err
	}
	var dirs []string
	for m := range seen {
		if r := rel(dir, m); r == ".." || strings.HasPrefix(r, "../") {
			dirs = append(dirs, m)
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

// localModuleDir returns the directory a module block in dir calls when
// its source is a local path, such as "../../modules/vpc", or "".
func localModuleDir(dir string, b *hclsyntax.Block) string {
	if b.Type != "module" {
		return ""
	}
	attr, ok := b.Body.Attributes["source"]
	if !ok {
		return ""
	}
	source, ok := constantString(attr.Expr)
	if !ok || (!strings.HasPrefix(source, "./") && !strings.HasPrefix(source, "../")) {
		return ""
	}
	return filepath.Join(dir, filepath.FromSlash(source))
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

// Verify runs the verification gate on a directory Generate wrote, for
// example after people edit it: on a private copy of it without its
// backend (see verifyCopy), which newTF returns a runner for, initialised
// without a backend, so verifying needs no access to the state and
// changes nothing in the directory.
// Secret variables need values, from TF_VAR_ environment variables or a
// .auto.tfvars file; opts.Omit and opts.StateOnly are the provider's.
func Verify(ctx context.Context, newTF NewRunner, dir string, opts Options) (Gate, error) {
	copied, remove, err := verifyCopy(dir)
	if err != nil {
		return nil, err
	}
	defer remove()
	tf, err := newTF(copied)
	if err != nil {
		return nil, err
	}
	tf = withTimeout(tf, opts.CommandTimeout)
	if err := initTerraform(ctx, tf, tfexec.Backend(false)); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	return runGate(ctx, tf, copied, nil, opts, nil)
}

// awsAccessKeyID and awsSecretCandidate find the two halves of an AWS
// access key: the ID, and 40 characters of base64 that could be its secret.
var (
	awsAccessKeyID     = regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)
	awsSecretCandidate = regexp.MustCompile(`(^|[^A-Za-z0-9/+=])([A-Za-z0-9/+]{40})($|[^A-Za-z0-9/+=])`)
)

// awsKeyPair reports whether content holds an AWS access key ID and,
// anywhere in the same file, what looks like a secret access key: 40
// characters of base64 mixing upper and lower case letters and digits,
// which a hex digest such as a commit SHA doesn't. An ID on its own is an
// identifier, such as an IAM access key's import ID or a tag naming one,
// and can't sign a request; GitHub's secret scanning matches the pair too.
func awsKeyPair(content string) bool {
	if !awsAccessKeyID.MatchString(content) {
		return false
	}
	for _, m := range awsSecretCandidate.FindAllStringSubmatch(content, -1) {
		s := m[2]
		if strings.ContainsAny(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") && strings.ContainsAny(s, "abcdefghijklmnopqrstuvwxyz") && strings.ContainsAny(s, "0123456789") {
			return true
		}
	}
	return false
}
