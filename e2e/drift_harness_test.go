// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/driftpolicy"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// The checks of the tests of infraharvest drift, which apply the roots an
// import generated, change resources out of band, and check what drift
// reports and writes into the code.

// driftRun runs infraharvest drift on the output directory out with
// engineName's engine at execPath.
type driftRun struct {
	ctx                  context.Context
	engineName, execPath string
	out                  string
}

// run runs infraharvest drift with args and returns its exit code and the
// report it wrote. A root it couldn't check fails the test, as does a
// report whose schema isn't drift.json's.
func (r driftRun) run(t *testing.T, args ...string) (int, report.Drift) {
	t.Helper()
	code, d, _ := r.output(t, args...)
	return code, d
}

// output runs infraharvest drift with args, as run does, and returns what
// it printed too.
func (r driftRun) output(t *testing.T, args ...string) (int, report.Drift, string) {
	t.Helper()
	all := append([]string{"drift", r.out, "--engine=" + r.engineName, "--terraform-path=" + r.execPath}, args...)
	// A run that fails before it writes the report mustn't pass for one
	// with the report of the run before.
	path := filepath.Join(r.out, report.Dir, report.DriftJSONFile)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	root := cli.NewCmdRoot()
	root.SetArgs(all)
	root.SetOut(&stdout)
	err := root.ExecuteContext(r.ctx)
	code := importer.ExitCode(err)
	t.Logf("infraharvest %s: exit %d, %v\n%s", strings.Join(all[2:], " "), code, err, stdout.String())
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("infraharvest drift %v: %v; no %s: %v", args, err, report.DriftJSONFile, readErr)
	}
	var d report.Drift
	if err := json.Unmarshal(content, &d); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if code == report.ExitCouldNotRun || d.Totals.Failed > 0 {
		t.Fatalf("infraharvest drift %v: %v; roots %+v", args, err, d.Roots)
	}
	if d.SchemaVersion != 1 {
		t.Errorf("%s: schema_version %d, want 1", report.DriftJSONFile, d.SchemaVersion)
	}
	if _, err := os.Stat(filepath.Join(r.out, report.Dir, report.DriftMarkdownFile)); err != nil {
		t.Errorf("infraharvest drift %v wrote no %s: %v", args, report.DriftMarkdownFile, err)
	}
	return code, d, stdout.String()
}

// mustExit runs infraharvest drift with args and fails the test unless it
// exits with want.
func (r driftRun) mustExit(t *testing.T, want int, args ...string) report.Drift {
	t.Helper()
	code, d := r.run(t, args...)
	if code != want {
		t.Errorf("infraharvest drift %v: exit %d, want %d; changes:\n%s", args, code, want, driftChanges(d))
	}
	return d
}

// mustExitJSON runs infraharvest drift with args and --format=json, and
// fails the test unless it exits with want and prints drift.json, as it
// wrote it, and nothing else.
func (r driftRun) mustExitJSON(t *testing.T, want int, args ...string) report.Drift {
	t.Helper()
	args = append(slices.Clone(args), "--format=json")
	code, d, stdout := r.output(t, args...)
	if code != want {
		t.Errorf("infraharvest drift %v: exit %d, want %d; changes:\n%s", args, code, want, driftChanges(d))
	}
	path := filepath.Join(r.out, report.Dir, report.DriftJSONFile)
	if written := readFile(t, path); stdout != written {
		t.Errorf("infraharvest drift %v printed other than %s:\n%s\nwant\n%s", args, path, stdout, written)
	}
	var printed report.Drift
	if err := json.Unmarshal([]byte(stdout), &printed); err != nil {
		t.Fatalf("infraharvest drift %v printed no drift.json: %v", args, err)
	}
	if !reflect.DeepEqual(printed, d) {
		t.Errorf("infraharvest drift %v printed\n%+v\nwant what %s has:\n%+v", args, printed, report.DriftJSONFile, d)
	}
	return d
}

// driftChanges lists the changes of d, one per line, for a failure's
// message.
func driftChanges(d report.Drift) string {
	var b strings.Builder
	for _, c := range d.Changes {
		fmt.Fprintf(&b, "  %s %s %s: %v -> %v, %s by %s, %s %s", c.Root, c.Address, c.Attribute, c.Before, c.After, c.Decision, c.DecisionSource, c.Outcome, c.Reason)
		if c.Source != nil {
			fmt.Fprintf(&b, " (%s:%d %s; %s)", c.Source.File, c.Source.Line, c.Source.Expression, c.Source.ModulePath)
		}
		if len(c.Covers) > 0 {
			fmt.Fprintf(&b, " covers %v", c.Covers)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// driftChange returns the change of d of a resource of resourceType, or,
// with resourceType "", of tags several resources share, to attribute; it
// fails the test if there is none.
func driftChange(t *testing.T, what string, d report.Drift, resourceType, attribute string) report.DriftChange {
	t.Helper()
	for _, c := range d.Changes {
		if c.Type == resourceType && c.Attribute == attribute && (resourceType != "" || len(c.Covers) > 0) {
			return c
		}
	}
	t.Fatalf("%s: no change of %s %s; changes:\n%s", what, resourceType, attribute, driftChanges(d))
	return report.DriftChange{}
}

// checkDriftChange fails the test unless c went from before to after, and
// was decided as decision by source, with outcome.
func checkDriftChange(t *testing.T, what string, c report.DriftChange, before, after, decision, source, outcome string) {
	t.Helper()
	if fmt.Sprint(c.Before) != before || fmt.Sprint(c.After) != after || c.Kind != "changed" {
		t.Errorf("%s: %s %s is %s from %v to %v, want changed from %s to %s", what, c.Address, c.Attribute, c.Kind, c.Before, c.After, before, after)
	}
	if c.Decision != decision || c.DecisionSource != source || c.Outcome != outcome {
		t.Errorf("%s: %s %s is %s by %s, %s (%s), want %s by %s, %s", what, c.Address, c.Attribute, c.Decision, c.DecisionSource, c.Outcome, c.Reason, decision, source, outcome)
	}
}

// codeFiles returns the .tf files under out, by path relative to it: the
// roots' and the modules' code, which drift changes only to accept a
// change.
func codeFiles(t *testing.T, out string) map[string]string {
	t.Helper()
	files := map[string]string{}
	for name, content := range outputFiles(t, out) {
		if strings.HasSuffix(name, ".tf") {
			files[name] = content
		}
	}
	return files
}

// checkCodeUnchanged fails the test for each .tf file under out that isn't
// as it was in before.
func checkCodeUnchanged(t *testing.T, what string, before map[string]string, out string) {
	t.Helper()
	after := codeFiles(t, out)
	for name, content := range after {
		if old, ok := before[name]; !ok {
			t.Errorf("%s: wrote %s", what, name)
		} else if content != old {
			t.Errorf("%s: changed %s:\n%s", what, name, content)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			t.Errorf("%s: removed %s", what, name)
		}
	}
}

// codeMatches reports whether the .tf files under out, joined, match the
// regular expression expr.
func codeMatches(t *testing.T, out, expr string) bool {
	t.Helper()
	var b strings.Builder
	for _, content := range codeFiles(t, out) {
		b.WriteString(content)
	}
	return regexp.MustCompile(expr).MatchString(b.String())
}

// checkPlanClean fails the test for each managed resource plan changes,
// but for the arguments the provider keeps only in state (see
// stateOnly).
func checkPlanClean(t *testing.T, what string, plan *tfjson.Plan) {
	t.Helper()
	for _, rc := range plan.ResourceChanges {
		if rc.Mode != tfjson.ManagedResourceMode || rc.Change.Actions.NoOp() {
			continue
		}
		changed := changedAttributes(rc.Change)
		if !rc.Change.Actions.Update() || !stateOnly(rc.Type, changed) {
			t.Errorf("%s: %s is planned to %v: %v", what, rc.Address, rc.Change.Actions, changed)
		}
	}
}

// applyGenerated applies each root an import generated in out into a
// local state in the root, as a team applies them, and checks each then
// plans no change.
func applyGenerated(ctx context.Context, t *testing.T, out, execPath, plugins string) []string {
	t.Helper()
	dirs := generatedDirs(t, out)
	for _, dir := range dirs {
		applyRoot(ctx, t, dir, execPath, plugins, nil)
		// Initialised by the apply, and the apply doesn't change the code.
		checkPlanClean(t, dir+": after the apply", planRootInit(ctx, t, false, dir, execPath, plugins))
	}
	return dirs
}

// checkAccepted checks the code under out, with every change accepted,
// plans no change: planned with a refresh, and planned without one once
// the state is refreshed, as drift never writes the state.
func checkAccepted(ctx context.Context, t *testing.T, dirs []string, execPath, plugins string) {
	t.Helper()
	for _, dir := range dirs {
		checkPlanClean(t, dir+": the accepted code", planRoot(ctx, t, dir, execPath, plugins))
		// The first plan initialised the root, and neither step changes the
		// code.
		applyRootInit(ctx, t, false, dir, execPath, plugins, nil, tfexec.RefreshOnly(true))
		checkPlanClean(t, dir+": the accepted code, -refresh=false", planRootInit(ctx, t, false, dir, execPath, plugins, tfexec.Refresh(false)))
	}
}

// changeOutOfBand applies config, a configuration that imports resources
// and changes them, in a directory of its own: a change in the cloud that
// no generated root's state knows of.
func changeOutOfBand(ctx context.Context, t *testing.T, config, execPath, plugins string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	tf, err := tfrun.New(dir, execPath, plugins, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tf.SetStdout(os.Stdout)
	tf.SetStderr(os.Stderr)
	if err := tf.Init(ctx); err != nil {
		t.Fatalf("out of band: terraform init: %v", err)
	}
	if err := tf.Apply(ctx); err != nil {
		t.Fatalf("out of band: terraform apply: %v", err)
	}
}

// stateValue returns the attribute of the resource of resourceType called
// name in state, failing the test if there is none.
func stateValue(t *testing.T, state []*tfjson.StateResource, resourceType, name, attribute string) string {
	t.Helper()
	for _, r := range state {
		if r.Type == resourceType && r.Name == name {
			if v, ok := r.AttributeValues[attribute].(string); ok && v != "" {
				return v
			}
		}
	}
	t.Fatalf("no %s of %s.%s", attribute, resourceType, name)
	return ""
}

// loadDecisions reads the decisions file at path.
func loadDecisions(t *testing.T, path string) *driftpolicy.Decisions {
	t.Helper()
	f, err := driftpolicy.LoadDecisions(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// decisionEntry returns the entry of the decisions file at path for a
// change to attribute, failing the test unless there is one, decided as
// decision.
func decisionEntry(t *testing.T, path, attribute, decision string) driftpolicy.Entry {
	t.Helper()
	f := loadDecisions(t, path)
	for _, e := range f.Changes {
		if e.Attribute == attribute {
			if e.Decision != decision || e.Fingerprint == "" {
				t.Errorf("%s: the entry of %s %s is %s with fingerprint %q, want %s", path, e.Address, attribute, e.Decision, e.Fingerprint, decision)
			}
			return e
		}
	}
	t.Fatalf("%s has no entry for %s: %+v", path, attribute, f.Changes)
	return driftpolicy.Entry{}
}

// acceptDrift accepts the entries of the decisions file at path for
// changes to attribute, as a person reviewing it does.
func acceptDrift(t *testing.T, path, attribute string) {
	t.Helper()
	f := loadDecisions(t, path)
	for i := range f.Changes {
		if f.Changes[i].Attribute == attribute {
			f.Changes[i].Decision = report.DriftAccept
		}
	}
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
}

// sharedDrift describes what checkSharedDrift checks, on an applied root
// whose resources share tags (or labels) and one of them also has a change
// of its own: the key of the shared tags that changed in the cloud on every
// resource that gets it from there, and a key that changed on one resource
// of ownType alone.
type sharedDrift struct {
	// args select the root to check, such as --root=<path>, and are passed
	// to each run.
	args []string
	// dir is the root's directory in the output, which the checks of the
	// code look at alone: the other roots keep their own copy of the shared
	// key, which the run doesn't change. Empty, they look at all of it.
	dir string
	// sharedAttribute is the shared key's attribute, such as
	// tags.infraharvest-sandbox, which changed from sharedBefore to
	// sharedAfter. sharedNew and sharedOld are regular expressions the
	// code matches with the new value of the key in place of the old.
	sharedAttribute, sharedBefore, sharedAfter string
	sharedNew, sharedOld                       string
	// ownType and ownAttribute are the resource's own change, from
	// ownBefore to ownAfter, and ownNew is what the code matches once it is
	// accepted.
	ownType, ownAttribute, ownBefore, ownAfter string
	ownNew                                     string
}

// checkSharedDrift runs infraharvest drift, as drift, on out, with the
// root of p changed in the cloud as p says, and checks what it reports and
// writes for the tags several resources share, which only the AWS tests
// change everywhere else, with the decisions and the rules that decide
// them:
//
//   - Report only: the shared key is one change of the place the code keeps
//     the tags, covering every resource that gets the key from it, the
//     resource's own change another; both are kept, exiting 4 with
//     --detailed-exitcode, and the code isn't changed.
//   - Rules: the last that matches a change decides it, in flags and in a
//     --rules file, a type rule matching the shared change through the
//     resources it covers, so that both are kept; then the own change
//     alone is accepted and written into the code, the shared one kept.
//   - The decisions file: --write-decisions writes the shared change's
//     entry, undecided; set to accept, it is written, by its entry, into
//     the place the code keeps the tags.
//
// It leaves both changes accepted in the code. The caller checks the code
// then plans no change.
func checkSharedDrift(t *testing.T, drift driftRun, p sharedDrift) {
	t.Helper()
	out := drift.out
	with := func(more ...string) []string { return append(slices.Clone(p.args), more...) }
	pattern := strings.SplitN(p.sharedAttribute, ".", 2)[0] + ".*"
	both := func(what string, d report.Drift, decision, source, outcome string) {
		t.Helper()
		checkDriftChange(t, what, driftChange(t, what, d, "", p.sharedAttribute), p.sharedBefore, p.sharedAfter, decision, source, outcome)
		checkDriftChange(t, what, driftChange(t, what, d, p.ownType, p.ownAttribute), p.ownBefore, p.ownAfter, decision, source, outcome)
	}

	// Report only.
	root := filepath.Join(out, filepath.FromSlash(p.dir))
	code := codeFiles(t, out)
	d := drift.mustExit(t, report.ExitDrift, with("--detailed-exitcode")...)
	checkCodeUnchanged(t, "report only", code, out)
	both("report only", d, report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	shared := driftChange(t, "report only", d, "", p.sharedAttribute)
	if !strings.HasPrefix(shared.Address, "local.") && !strings.HasPrefix(shared.Address, "provider.") {
		t.Errorf("report only: the shared change is of %s, want the place the root keeps the tags", shared.Address)
	}
	owners := 0
	for _, address := range shared.Covers {
		if strings.Contains(address, p.ownType+".") {
			owners++
		}
	}
	if len(shared.Covers) < 2 || owners == 0 {
		t.Errorf("report only: %s covers %v, want at least two resources, one of them of %s", shared.Address, shared.Covers, p.ownType)
	}
	for _, c := range d.Changes {
		if c.Attribute == p.sharedAttribute && len(c.Covers) == 0 {
			t.Errorf("report only: %s's key is a change of its own as well as of %s: %+v", c.Address, shared.Address, c)
		}
	}

	// The last rule that matches a change decides it.
	d = drift.mustExit(t, report.ExitOK, with("--accept", "attribute="+pattern, "--keep", "type="+p.ownType)...)
	both("--accept then --keep", d, report.DriftKeep, report.DriftDecidedByFlag, report.DriftKept)
	checkCodeUnchanged(t, "--accept then --keep", code, out)
	rules := filepath.Join(t.TempDir(), "drift.rules")
	if err := os.WriteFile(rules, []byte("# every key, then the type of the resource\nattribute="+pattern+"\n\nkeep type="+p.ownType+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d = drift.mustExit(t, report.ExitOK, with("--rules", rules)...)
	both("--rules", d, report.DriftKeep, report.DriftDecidedByFlag, report.DriftKept)
	checkCodeUnchanged(t, "--rules", code, out)
	d = drift.mustExit(t, report.ExitOK, with("--keep", "all", "--accept", "attribute="+p.ownAttribute)...)
	checkDriftChange(t, "--keep then --accept", driftChange(t, "--keep then --accept", d, p.ownType, p.ownAttribute), p.ownBefore, p.ownAfter, report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	checkDriftChange(t, "--keep then --accept", driftChange(t, "--keep then --accept", d, "", p.sharedAttribute), p.sharedBefore, p.sharedAfter, report.DriftKeep, report.DriftDecidedByFlag, report.DriftKept)
	if !codeMatches(t, root, p.ownNew) {
		t.Errorf("--keep then --accept: the code doesn't match %s", p.ownNew)
	}
	if codeMatches(t, root, p.sharedNew) {
		t.Errorf("--keep then --accept: the code has the shared key's new value, which was kept: %s", p.sharedNew)
	}

	// The decisions file.
	decisions := filepath.Join(out, driftpolicy.DefaultDecisionsFile)
	code = codeFiles(t, out)
	drift.mustExit(t, report.ExitOK, with("--write-decisions")...)
	checkCodeUnchanged(t, "--write-decisions", code, out)
	decisionEntry(t, decisions, p.sharedAttribute, driftpolicy.Undecided)
	acceptDrift(t, decisions, p.sharedAttribute)
	d = drift.mustExit(t, report.ExitOK, with("--decisions", decisions)...)
	decided := driftChange(t, "--decisions", d, "", p.sharedAttribute)
	checkDriftChange(t, "--decisions", decided, p.sharedBefore, p.sharedAfter, report.DriftAccept, report.DriftDecidedByEntry, report.DriftWritten)
	if decided.DecisionStale {
		t.Errorf("--decisions: %s %s is decision_stale", decided.Address, decided.Attribute)
	}
	if !codeMatches(t, root, p.sharedNew) {
		t.Errorf("--decisions: the code doesn't match %s", p.sharedNew)
	}
	if codeMatches(t, root, p.sharedOld) {
		t.Errorf("--decisions: the code still matches %s", p.sharedOld)
	}
}
