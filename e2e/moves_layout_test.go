// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/report"
)

// layoutAccount is the account testdata/aws-layout creates its resources
// in, which no other test uses.
const layoutAccount = "121212121212"

// layoutProfile is the profile the test imports layoutAccount as.
const layoutProfile = "infraharvest-e2e-layout"

// TestAWSRegenerateLayout imports testdata/aws-layout, two queues and a
// topic, into one root, by the default layout, and applies the root,
// keeping its state, as a team would. Generated again (--regenerate) with
// a root per service ({service} in --layout), the root it applied hands
// every resource over to the roots of sqs and sns: it keeps its provider
// configuration and a moves.tf of removed blocks alone, which the gate
// checks (G8), and the other roots import what it hands over, as
// coverage.json and the READMEs say. Over the state the first apply left,
// the old root then forgets each resource, creating and destroying
// nothing; once it and then the new roots are applied, no root plans any
// change, and the resources are still in the cloud.
func TestAWSRegenerateLayout(t *testing.T) {
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	fixture := seedApplied(ctx, t, "aws-layout", execPath, plugins)
	useAWSAccount(t, layoutAccount, layoutProfile)

	out := t.TempDir()
	args := []string{
		"import", "aws",
		"--engine=" + engineName,
		"--terraform-path=" + execPath,
		"--profile=" + layoutProfile,
		"--regions=us-east-1",
		"--services=sqs,sns",
		// No module calls: each resource is a resource of the root, at the
		// address the report gives it.
		"--modules=none",
		"--out=" + out,
		"--all",
	}
	runMovesImport(ctx, t, args...)
	first := coverageOf(t, out)
	checkCoverage(t, "the first import", first, nil)
	if len(first.Directories) != 1 {
		t.Fatalf("the first import generated %+v, want one root", first.Directories)
	}
	oldRoot := first.Directories[0].Path
	had := map[string]string{} // by import ID, where the old root has it
	for _, r := range first.Directories[0].Imported {
		had[r.ID] = r.Address
	}
	if len(had) != 3 {
		t.Fatalf("%s imports %v, want the two queues and the topic", oldRoot, had)
	}
	old := filepath.Join(out, filepath.FromSlash(oldRoot))

	// The root applied, as its README says: its state then has them.
	work := t.TempDir()
	workOld := filepath.Join(work, "old")
	copyRootFiles(t, old, workOld)
	applyRoot(ctx, t, workOld, execPath, plugins, nil)
	if err := os.Remove(filepath.Join(workOld, engine.ImportsFileName)); err != nil {
		t.Fatal(err)
	}

	runMovesImport(ctx, t, append(slices.Clone(args), "--regenerate", "--layout={out}/{provider}/{account}/{service}/{region}/")...)
	second := coverageOf(t, out)
	checkCoverage(t, "--regenerate with a root per service", second, nil)
	newRoots := map[string]int{} // by path, how many resources it is handed
	for id, from := range had {
		var to report.Move
		for _, d := range second.Departed {
			if d.ID == id && d.FromRoot == oldRoot && d.From == from && d.ToRoot != oldRoot {
				to = d
			}
		}
		if to.ToRoot == "" {
			t.Errorf("--regenerate: coverage.json doesn't hand %s over from %s: departed %+v", id, oldRoot, second.Departed)
			continue
		}
		if at := importedAt(t, second, to.ToRoot, id); at != to.To {
			t.Errorf("--regenerate: %s imports %s at %s, coverage.json hands it over to %s", to.ToRoot, id, at, to.To)
		}
		newRoots[to.ToRoot]++
	}
	if len(newRoots) != 2 {
		t.Errorf("--regenerate: the resources go to %v, want a root for sqs and one for sns", newRoots)
	}
	if len(second.IncompleteHandovers) != 0 {
		t.Errorf("--regenerate: incomplete handovers %+v", second.IncompleteHandovers)
	}
	moves := strings.Join(strings.Fields(readFile(t, filepath.Join(old, engine.MovesFileName))), " ")
	for _, from := range had {
		if want := "removed { from = " + from + " lifecycle { destroy = false } }"; !strings.Contains(moves, want) {
			t.Errorf("%s/%s doesn't have %q:\n%s", oldRoot, engine.MovesFileName, want, moves)
		}
	}
	for _, name := range []string{engine.GeneratedFileName, engine.ImportsFileName} {
		if _, err := os.Stat(filepath.Join(old, name)); err == nil {
			t.Errorf("%s keeps its %s", oldRoot, name)
		}
	}
	for _, d := range second.Directories {
		if d.Path == oldRoot && !slices.ContainsFunc(d.Checks, func(c report.Check) bool { return c.Name == gate.CheckMoves && c.Passed && !c.Skipped }) {
			t.Errorf("%s: the gate doesn't pass %s: %+v", oldRoot, gate.CheckMoves, d.Checks)
		}
	}
	// Each README names the other roots, for the order to apply them in.
	readme := readFile(t, filepath.Join(old, engine.ReadmeFileName))
	for root := range newRoots {
		if !strings.Contains(readme, root) {
			t.Errorf("%s doesn't name %s:\n%s", filepath.Join(old, engine.ReadmeFileName), root, readme)
		}
		if content := readFile(t, filepath.Join(out, filepath.FromSlash(root), engine.ReadmeFileName)); !strings.Contains(content, oldRoot) {
			t.Errorf("%s doesn't name %s:\n%s", filepath.Join(root, engine.ReadmeFileName), oldRoot, content)
		}
	}

	// The old root generated again, over the state the first apply left:
	// each resource is forgotten, nothing created or destroyed.
	clearRootFiles(t, workOld)
	copyRootFiles(t, old, workOld)
	forgotten := map[string]bool{}
	for _, rc := range planRoot(ctx, t, workOld, execPath, plugins).ResourceChanges {
		if rc.Mode != tfjson.ManagedResourceMode {
			continue
		}
		if rc.Change.Actions.Forget() {
			forgotten[rc.Address] = true
			continue
		}
		t.Errorf("%s: %s is planned to %v", oldRoot, rc.Address, rc.Change.Actions)
	}
	for _, from := range had {
		if !forgotten[from] {
			t.Errorf("%s: the plan doesn't forget %s", oldRoot, from)
		}
	}
	applyRoot(ctx, t, workOld, execPath, plugins, nil)

	// Then the roots they go to import them, and no root changes more.
	dirs := []string{workOld}
	for _, root := range sortedKeys(newRoots) {
		dir := filepath.Join(work, filepath.FromSlash(root))
		copyRootFiles(t, filepath.Join(out, filepath.FromSlash(root)), dir)
		imported := 0
		for _, n := range checkNoChanges(ctx, t, dir, execPath, plugins, nil) {
			imported += n
		}
		if imported != newRoots[root] {
			t.Errorf("%s: imports %d resources, want the %d handed over", root, imported, newRoots[root])
		}
		applyRoot(ctx, t, dir, execPath, plugins, nil)
		dirs = append(dirs, dir)
	}
	for _, dir := range dirs {
		for _, rc := range planRoot(ctx, t, dir, execPath, plugins).ResourceChanges {
			if rc.Mode == tfjson.ManagedResourceMode && (!rc.Change.Actions.NoOp() || rc.Change.Importing != nil || rc.PreviousAddress != "") {
				t.Errorf("%s: after the apply, %s is planned to %v", dir, rc.Address, rc.Change.Actions)
			}
		}
	}
	// The configuration that created them finds them as it left them.
	if changed, err := fixture.tf.Plan(ctx); err != nil || changed {
		t.Errorf("testdata/aws-layout: terraform plan: changes %v, %v", changed, err)
	}
}

// useAWSAccount makes account's credentials the test's, with profile, in
// a credentials file of its own, the profile for them: the import keeps
// the credentials it resolves for the rest of the process, by profile,
// and a profile of its own keeps the tests after it out of account.
func useAWSAccount(t *testing.T, account, profile string) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", account)
	credentials := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(credentials, []byte("[default]\naws_access_key_id = test\naws_secret_access_key = test\n\n["+profile+"]\naws_access_key_id = "+account+"\naws_secret_access_key = test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credentials)
}
