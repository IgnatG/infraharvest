// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
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
	// Findings are every finding of the scanners check, for CheckScanners,
	// with the resource each is about, which Details list only up to a
	// bound per scanner (see runScanners).
	Findings []Finding `json:",omitempty"`
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
// secrets Generate found (see secret.Values).
func runGate(ctx context.Context, tf tfrun.Terraform, dir string, secrets []secret.Variable, opts Options, known *secret.Values) (Gate, error) {
	expected, err := countImportBlocks(dir)
	if err != nil {
		return nil, err
	}
	policy := fullGate(opts)
	policy.plan = func() (Check, []string, error) {
		p, _, diags, err := tfrun.ShowPlan(ctx, tf, append(secret.Placeholders(secrets), known.Options()...))
		if err != nil {
			return Check{}, nil, err
		}
		return planCheck(p, diags, secrets, opts.StateOnly, expected), append(secret.InPlan(p), secret.KnownValues(secrets)...), nil
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
		for _, b := range f.Syntax.Blocks {
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
	// scanners are the external scanners to run, in slots (see
	// runScanners).
	omit     map[string][]string
	own      []string
	scanners []Scanner
	slots    *ScannerSlots
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
// output standard and with the scanners and scanner slots of opts; its
// plan is still to be set.
func fullGate(opts Options) gatePolicy {
	return gatePolicy{omit: opts.Omit, own: ownModuleDirs(opts.Adapters), scanners: opts.Scanners, slots: opts.ScannerSlots}
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
func (p gatePolicy) run(ctx context.Context, tf tfrun.Terraform, dir string) (Gate, []Gate, error) {
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
func (p gatePolicy) runChecks(ctx context.Context, tf tfrun.Terraform, dir string) (Gate, []Gate, error) {
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
					scanners = runScanners(ctx, dir, p.scanners, p.slots)
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
func (p gatePolicy) format(ctx context.Context, tf tfrun.Terraform) (Check, error) {
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
func (p gatePolicy) validate(ctx context.Context, tf tfrun.Terraform) (Check, error) {
	validation, err := tf.Validate(ctx)
	if err != nil {
		return Check{}, fmt.Errorf("terraform validate: %w", err)
	}
	c := Check{Name: CheckValidate, Passed: validation.Valid}
	for _, d := range tfrun.ErrorDiagnostics(validation.Diagnostics) {
		c.Details = append(c.Details, tfrun.FormatDiagnostic(d))
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
	values := secret.NewMatcher(sensitive)
	for _, name := range p.edited {
		before := Check{Details: secret.InFile(values, name, string(p.before[name]))}
		after := Check{Details: secret.InFile(values, name, string(p.after[name]))}
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
// constant (see secret.IsCredential), and for any secret value written in
// the configuration (see secret.Literals).
func scanSecrets(dir string, sensitive []string) (Check, error) {
	check := Check{Name: CheckSecrets}
	values := secret.NewMatcher(sensitive)
	err := walkOutputAndModules(dir, func(rel string, content string) {
		check.Details = append(check.Details, secret.InFile(values, rel, content)...)
	})
	check.Passed = len(check.Details) == 0
	return check, err
}
