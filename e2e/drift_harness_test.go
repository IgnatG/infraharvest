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
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/infraharvest/infraharvest-cli/internal/cli"
	"github.com/infraharvest/infraharvest-cli/internal/engine"
	"github.com/infraharvest/infraharvest-cli/internal/importer"
	"github.com/infraharvest/infraharvest-cli/internal/report"
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
	return code, d
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
		changed := changedAttributes(rc.Change.Before, rc.Change.After)
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
		checkPlanClean(t, dir+": after the apply", planRoot(ctx, t, dir, execPath, plugins))
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
		applyRoot(ctx, t, dir, execPath, plugins, nil, tfexec.RefreshOnly(true))
		checkPlanClean(t, dir+": the accepted code, -refresh=false", planRoot(ctx, t, dir, execPath, plugins, tfexec.Refresh(false)))
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
	tf, err := engine.NewTerraform(dir, execPath, plugins, nil)
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
