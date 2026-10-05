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

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

// Names of the verification gate's checks (definition of done G1 to G7; see
// also CheckStandards and CheckScanners).
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
	// Details explain a failure, or note what a pass allowed.
	Details []string
	// Changes are the changes a plan check found, for CheckPlan.
	Changes []PlannedChange
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

// runGate checks the directory as Generate leaves it: formatted, valid,
// planning only imports, following the output standard (see
// checkStandards), passing the installed scanners (see runScanners),
// without secret values in any file, and without functions that make
// output differ between runs. Secret variables get
// placeholder values in the plan, so the arguments they set may change;
// so may the arguments opts.StateOnly names per type, which providers keep
// only in state and import can't set.
func runGate(ctx context.Context, tf Terraform, dir string, secrets []Secret, opts Options) (Gate, error) {
	return gateWith(ctx, tf, dir, opts, func() (Check, []string, error) {
		p, diags, err := showPlan(ctx, tf, placeholders(secrets))
		if err != nil {
			return Check{}, nil, err
		}
		return planCheck(p, diags, secrets, opts.StateOnly), sensitiveValues(p), nil
	})
}

// gateWith runs the verification gate's checks on dir, with plan for the
// plan check; plan also returns the values the plan marks sensitive, which
// no file may contain.
func gateWith(ctx context.Context, tf Terraform, dir string, opts Options, plan func() (Check, []string, error)) (Gate, error) {
	var gate Gate

	formatted, files, err := tf.FormatCheck(ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform fmt: %w", err)
	}
	gate = append(gate, Check{Name: CheckFormat, Passed: formatted, Details: files})

	validation, err := tf.Validate(ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform validate: %w", err)
	}
	validCheck := Check{Name: CheckValidate, Passed: validation.Valid}
	for _, d := range errorDiagnostics(validation.Diagnostics) {
		validCheck.Details = append(validCheck.Details, formatDiagnostic(d))
	}
	gate = append(gate, validCheck)

	planned, sensitive, err := plan()
	if err != nil {
		return nil, err
	}
	gate = append(gate, planned)

	standards, err := checkStandards(dir, opts.Omit)
	if err != nil {
		return nil, err
	}
	gate = append(gate, standards, runScanners(ctx, dir, opts.Scanners))

	secretCheck, err := scanSecrets(dir, sensitive)
	if err != nil {
		return nil, err
	}
	gate = append(gate, secretCheck)

	determinism, err := scanNondeterminism(dir)
	if err != nil {
		return nil, err
	}
	return append(gate, determinism), nil
}

// planCheck passes if the plan has no changes besides imports, other than
// updates to arguments that secrets set or stateOnly names.
func planCheck(p *tfjson.Plan, diags []tfjson.Diagnostic, secrets []Secret, stateOnly map[string][]string) Check {
	check := Check{Name: CheckPlan}
	if len(diags) > 0 || p == nil {
		for _, d := range diags {
			check.Details = append(check.Details, formatDiagnostic(d))
		}
		return check
	}
	secretArguments := map[string][]string{}
	for _, s := range secrets {
		top, _, _ := strings.Cut(s.Attribute, ".")
		top, _, _ = strings.Cut(top, "[")
		secretArguments[s.Address] = append(secretArguments[s.Address], top)
	}
	check.Passed = true
	for _, rc := range p.ResourceChanges {
		if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil || rc.Change.Actions.NoOp() || rc.Change.Actions.Read() {
			continue
		}
		change := PlannedChange{Address: rc.Address}
		for _, a := range rc.Change.Actions {
			change.Actions = append(change.Actions, string(a))
		}
		if !rc.Change.Actions.Update() {
			check.Passed = false
			check.Changes = append(check.Changes, change)
			continue
		}
		change.Attributes = changedAttributes(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown)
		allowed := append(append([]string(nil), stateOnly[rc.Type]...), secretArguments[rc.Address]...)
		// A placeholder secret value also makes what the provider computes
		// from it unknown, such as an SSM parameter's version.
		placeholderChanged := false
		for _, a := range change.Attributes {
			if slices.Contains(secretArguments[rc.Address], a) {
				placeholderChanged = true
			}
		}
		unknown := unknownAttributes(rc.Change.AfterUnknown)
		explained := true
		for _, a := range change.Attributes {
			if !slices.Contains(allowed, a) && (!placeholderChanged || !slices.Contains(unknown, a)) {
				explained = false
			}
		}
		if explained {
			check.Details = append(check.Details, fmt.Sprintf("%s: only state-only or secret arguments change (%s)", rc.Address, strings.Join(change.Attributes, ", ")))
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
	return check
}

// changedAttributes returns the top-level attributes that differ between
// before and after, or that will only be known after apply, sorted.
func changedAttributes(before, after, afterUnknown any) []string {
	b, _ := before.(map[string]any)
	a, _ := after.(map[string]any)
	unknown, _ := afterUnknown.(map[string]any)
	var changed []string
	for k, v := range a {
		if !reflect.DeepEqual(b[k], v) {
			changed = append(changed, k)
		}
	}
	for k, u := range unknown {
		if containsTrue(u) && !slices.Contains(changed, k) {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	return changed
}

// unknownAttributes returns the top-level attributes after_unknown marks
// as known only after apply.
func unknownAttributes(afterUnknown any) []string {
	unknown, _ := afterUnknown.(map[string]any)
	var names []string
	for k, u := range unknown {
		if containsTrue(u) {
			names = append(names, k)
		}
	}
	return names
}

// containsTrue reports whether an after_unknown value marks anything
// unknown.
func containsTrue(v any) bool {
	switch u := v.(type) {
	case bool:
		return u
	case map[string]any:
		for _, sub := range u {
			if containsTrue(sub) {
				return true
			}
		}
	case []any:
		for _, sub := range u {
			if containsTrue(sub) {
				return true
			}
		}
	}
	return false
}

// sensitiveValues returns the string values the plan marks sensitive in
// the imported objects: what must never be written into a file.
func sensitiveValues(p *tfjson.Plan) []string {
	if p == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, rc := range p.ResourceChanges {
		if rc.Change != nil {
			collectSensitive(rc.Change.Before, rc.Change.BeforeSensitive, seen)
		}
	}
	values := make([]string, 0, len(seen))
	for v := range seen {
		values = append(values, v)
	}
	sort.Strings(values)
	return values
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

// secretPatterns are formats of credentials that never belong in
// configuration.
var secretPatterns = map[string]*regexp.Regexp{
	"AWS access key ID": regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`),
	"private key":       regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	"GitHub token":      regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`),
	"Slack token":       regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
	"Google API key":    regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	"Azure storage key": regexp.MustCompile(`AccountKey=[A-Za-z0-9+/=]{40,}`),
	"Stripe secret key": regexp.MustCompile(`\bsk_live_[0-9A-Za-z]{20,}\b`),
	"JSON Web Token":    regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
	"password in a URL": regexp.MustCompile(`[a-z][a-z0-9+.-]*://[^/\s:@"]+:[^/\s@"]+@`),
}

// scanSecrets checks every file Generate wrote into dir for the plan's
// sensitive values and for credential formats.
func scanSecrets(dir string, sensitive []string) (Check, error) {
	check := Check{Name: CheckSecrets, Passed: true}
	err := walkOutput(dir, func(rel string, content string) {
		for _, v := range sensitive {
			if strings.Contains(content, v) {
				check.Passed = false
				check.Details = append(check.Details, rel+": contains a value the provider marks sensitive")
				break
			}
		}
		names := make([]string, 0, len(secretPatterns))
		for name := range secretPatterns {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if secretPatterns[name].MatchString(content) {
				check.Passed = false
				check.Details = append(check.Details, rel+": looks like it contains a "+name)
			}
		}
	})
	return check, err
}

// nondeterministic are functions whose results differ between runs.
var nondeterministic = regexp.MustCompile(`\b(timestamp|plantimestamp|uuid|uuidv5|bcrypt)\(`)

// scanNondeterminism checks the configuration Generate wrote calls no
// function whose result differs between runs, so that importing an
// unchanged estate again gives the same files.
func scanNondeterminism(dir string) (Check, error) {
	check := Check{Name: CheckDeterminism, Passed: true}
	err := walkOutput(dir, func(rel string, content string) {
		if strings.HasSuffix(rel, ".tf") && nondeterministic.MatchString(content) {
			check.Passed = false
			check.Details = append(check.Details, rel+": calls "+nondeterministic.FindString(content))
		}
	})
	return check, err
}

// walkOutput calls visit with each file Generate wrote into dir, by path
// relative to dir, skipping Terraform's working files.
func walkOutput(dir string, visit func(rel, content string)) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".terraform" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == LockFileName {
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

// showPlan plans with vars into a plan file and returns it as JSON, or the
// errors if the configuration doesn't plan. The plan file holds secret
// values: it lives in a private temporary directory, removed before
// showPlan returns.
func showPlan(ctx context.Context, tf Terraform, vars []tfexec.PlanOption) (*tfjson.Plan, []tfjson.Diagnostic, error) {
	p, _, diags, err := showPlanWithSummary(ctx, tf, vars)
	return p, diags, err
}

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
	p, err := tf.ShowPlanFile(ctx, planFile)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("terraform show: %w", err)
	}
	return p, summary, nil, nil
}

// Verify runs the verification gate on a directory Generate wrote, for
// example after people edit it: it initialises the directory without its
// backend, so verifying needs no access to the state, then checks it.
// Secret variables need values, from TF_VAR_ environment variables or a
// .auto.tfvars file; opts.Omit and opts.StateOnly are the provider's.
func Verify(ctx context.Context, tf Terraform, dir string, opts Options) (Gate, error) {
	if err := tf.Init(ctx, tfexec.Backend(false)); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	return runGate(ctx, tf, dir, nil, opts)
}
