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
	expected, err := countImportBlocks(dir)
	if err != nil {
		return nil, err
	}
	return gateWith(ctx, tf, dir, opts, func() (Check, []string, error) {
		p, _, diags, err := showPlanWithSummary(ctx, tf, placeholders(secrets))
		if err != nil {
			return Check{}, nil, err
		}
		return planCheck(p, diags, secrets, opts.StateOnly, expected), sensitiveValues(p), nil
	})
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

// planCheck passes if the plan imports expected resources and has no
// changes besides those imports, other than updates to the attributes
// secrets set, to what a secret's placeholder makes unknown, or to the
// stateOnly arguments of the resource's type. An expected below zero
// doesn't check the number of imports.
func planCheck(p *tfjson.Plan, diags []tfjson.Diagnostic, secrets []Secret, stateOnly map[string][]string, expected int) Check {
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
			case slices.Contains(stateOnly[rc.Type], topOf(l.path)):
			case slices.Contains(secretPaths[rc.Address], withoutIndexes(l.path)):
			case placeholderChanged && l.unknown:
			default:
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
	if expected >= 0 && importing != expected {
		check.Passed = false
		check.Details = append(check.Details, fmt.Sprintf("the plan imports %d resources, the configuration has %d import blocks", importing, expected))
	}
	return check
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

// secretPatternNames are the names of secretPatterns, sorted, so that
// findings come in the same order every run.
var secretPatternNames = sortedKeys(secretPatterns)

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// scanSecrets checks every file Generate wrote into dir for the plan's
// sensitive values, for credential formats, and for provider blocks that
// set a credential argument to a constant (see IsCredentialArgument).
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
		for _, name := range secretPatternNames {
			if secretPatterns[name].MatchString(content) {
				check.Passed = false
				check.Details = append(check.Details, rel+": looks like it contains a "+name)
			}
		}
		for _, name := range providerCredentials(rel, content) {
			check.Passed = false
			check.Details = append(check.Details, rel+": provider block sets "+name+", which belongs in the provider's environment")
		}
	})
	return check, err
}

// providerCredentials returns the credential arguments the provider blocks
// of a .tf file set to constants, sorted, as "provider.argument".
func providerCredentials(rel, content string) []string {
	if !strings.HasSuffix(rel, ".tf") {
		return nil
	}
	body, ok := parseBody(rel, content)
	if !ok {
		return nil
	}
	var names []string
	for _, b := range body.Blocks {
		if b.Type != "provider" || len(b.Labels) != 1 {
			continue
		}
		for name, attr := range b.Body.Attributes {
			if !IsCredentialArgument(name) {
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

// parseBody parses a .tf file's content; a file that doesn't parse has
// nothing to scan, since validate (G2) reports it.
func parseBody(rel, content string) (*hclsyntax.Body, bool) {
	f, diags := hclsyntax.ParseConfig([]byte(content), rel, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, false
	}
	body, ok := f.Body.(*hclsyntax.Body)
	return body, ok
}

// nondeterministic are functions whose results differ between runs.
var nondeterministic = map[string]bool{"timestamp": true, "plantimestamp": true, "uuid": true, "uuidv5": true, "bcrypt": true}

// scanNondeterminism checks the configuration Generate wrote calls no
// function whose result differs between runs, so that importing an
// unchanged estate again gives the same files.
func scanNondeterminism(dir string) (Check, error) {
	check := Check{Name: CheckDeterminism, Passed: true}
	err := walkOutput(dir, func(rel string, content string) {
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
	body, ok := parseBody(rel, content)
	if !ok {
		return nil
	}
	calls := map[string]bool{}
	hclsyntax.VisitAll(body, func(node hclsyntax.Node) hcl.Diagnostics {
		if call, ok := node.(*hclsyntax.FunctionCallExpr); ok && nondeterministic[call.Name] {
			calls[call.Name] = true
		}
		return nil
	})
	return sortedKeys(calls)
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
