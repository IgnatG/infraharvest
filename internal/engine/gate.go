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

// Names of the verification gate's checks, numbered G1 to G7 (see also
// CheckStandards and CheckScanners).
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
// output differ between runs. Secret variables Terraform doesn't show get
// placeholder values in the plan, so the arguments they set may change;
// so may the arguments opts.StateOnly names per type, which providers keep
// only in state and import can't set.
func runGate(ctx context.Context, tf Terraform, dir string, secrets []Secret, opts Options) (Gate, error) {
	expected, err := countImportBlocks(dir)
	if err != nil {
		return nil, err
	}
	return gateWith(ctx, tf, dir, opts, func() (Check, []string, error) {
		p, _, diags, err := showPlanWithSummary(ctx, tf, append(placeholders(secrets), opts.secretValues.options()...))
		if err != nil {
			return Check{}, nil, err
		}
		return planCheck(p, diags, secrets, opts.StateOnly, expected), append(sensitiveValues(p), knownValues(secrets)...), nil
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

	standards, err := checkStandards(dir, opts.Omit, ownModuleDirs(opts.Adapters))
	if err != nil {
		return nil, err
	}
	// Secrets first: external scanners are other people's code, and some
	// send what they scan elsewhere, so they never see a directory that
	// holds a secret value.
	secretCheck, err := scanSecrets(dir, sensitive)
	if err != nil {
		return nil, err
	}
	scanners := Check{Name: CheckScanners, Passed: true, Skipped: true, Details: []string{"not run, as " + CheckSecrets + " found secret values"}}
	if secretCheck.Passed {
		scanners = runScanners(ctx, dir, opts.Scanners)
	}
	gate = append(gate, standards, scanners, secretCheck)

	determinism, err := scanNondeterminism(dir)
	if err != nil {
		return nil, err
	}
	// Checks cut short, such as a scanner that couldn't run, say nothing
	// of dir: a cancelled gate fails as cancelled, not as failed checks.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append(gate, determinism), nil
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

// secretPatterns are formats of credentials that never belong in
// configuration.
// An AWS access key ID on its own isn't a credential (see awsKeyPair).
var secretPatterns = map[string]*regexp.Regexp{
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

// scanSecrets checks every file Generate wrote into dir, and into the
// local modules it calls (see walkOutputAndModules), for the plan's
// sensitive values and the secret values Generate found, for credential
// formats, for provider blocks that set a credential argument to a
// constant (see IsCredentialArgument), and for any secret value written in
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
	for _, name := range secretPatternNames {
		if secretPatterns[name].MatchString(content) {
			check.Passed = false
			check.Details = append(check.Details, rel+": looks like it contains a "+name)
		}
	}
	if awsKeyPair(content) {
		check.Passed = false
		check.Details = append(check.Details, rel+": looks like it contains an AWS access key ID with its secret access key")
	}
	// Parsed once for the checks below: parsing is most of what a scan
	// costs.
	parsed := &parsedFile{rel: rel, content: content}
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
func providerCredentials(f *parsedFile) []string {
	if !strings.HasSuffix(f.rel, ".tf") {
		return nil
	}
	body, ok := f.body()
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

// parsedFile is a file of the output, rel, parsed the first time a check
// asks for its body (see parseBody).
type parsedFile struct {
	rel, content string
	parsed       bool
	syntax       *hclsyntax.Body
	ok           bool
}

func (f *parsedFile) body() (*hclsyntax.Body, bool) {
	if !f.parsed {
		f.parsed = true
		f.syntax, f.ok = parseBody(f.rel, f.content)
	}
	return f.syntax, f.ok
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
	body, ok := parseBody(rel, content)
	if !ok {
		return nil
	}
	calls := map[string]bool{}
	_ = hclsyntax.VisitAll(body, func(node hclsyntax.Node) hcl.Diagnostics {
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
			body, ok := parseBody(path, string(content))
			if !ok {
				continue
			}
			for _, b := range body.Blocks {
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
// example after people edit it: it initialises the directory without its
// backend, so verifying needs no access to the state, then checks it.
// Secret variables need values, from TF_VAR_ environment variables or a
// .auto.tfvars file; opts.Omit and opts.StateOnly are the provider's.
func Verify(ctx context.Context, tf Terraform, dir string, opts Options) (Gate, error) {
	tf = withTimeout(tf, opts.CommandTimeout)
	if err := initTerraform(ctx, tf, tfexec.Backend(false)); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	return runGate(ctx, tf, dir, nil, opts)
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
