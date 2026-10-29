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

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/report"
)

// movesAccount is the account testdata/aws-moves creates its queues in,
// which no other test uses.
const movesAccount = "555555555555"

// movesProfile is the profile the test imports movesAccount as.
const movesProfile = "infraharvest-e2e-moves"

// TestAWSRegenerateMoves imports testdata/aws-moves, one root per
// environment (--env-tags), and applies the root, keeping its state, as
// a team would. It then changes the estate: one queue gets another Name
// tag, so another address, and another queue another environment, so
// another root; a third stays as it was, and keeps its address. Generated again (--regenerate), the root it applied gets a moved
// block for the first queue and a removed block for the second in
// moves.tf, which the gate checks (G8); the other environment's root
// imports the second, and the READMEs and report.json say so. Over the
// state the first apply left, the root then plans the first queue as
// moved and forgets the second, creating and destroying nothing; once
// both roots are applied, in that order, neither plans any change.
func TestAWSRegenerateMoves(t *testing.T) {
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
	fixture := seedApplied(ctx, t, "aws-moves", execPath, plugins)
	renamed, handed, kept := fixture.queueURL(ctx, t, "renamed"), fixture.queueURL(ctx, t, "handed"), fixture.queueURL(ctx, t, "kept")
	t.Setenv("AWS_ACCESS_KEY_ID", movesAccount)
	// The import keeps the credentials it resolves for the rest of the
	// process, by profile: a profile of its own keeps the tests after it
	// out of this account.
	credentials := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(credentials, []byte("[default]\naws_access_key_id = test\naws_secret_access_key = test\n\n["+movesProfile+"]\naws_access_key_id = "+movesAccount+"\naws_secret_access_key = test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credentials)

	out := t.TempDir()
	args := []string{
		"import", "aws",
		"--engine=" + engineName,
		"--terraform-path=" + execPath,
		"--profile=" + movesProfile,
		"--regions=us-east-1",
		"--services=sqs",
		"--env-tags=Env",
		// No module calls: each queue is a resource of the root, at the
		// address the report gives it.
		"--modules=none",
		"--out=" + out,
		"--all",
	}
	runMovesImport(ctx, t, args...)
	first := coverageOf(t, out)
	checkCoverage(t, "the first import", first, nil)
	devRoot, prodRoot := "envs/dev/aws/"+movesAccount+"/us-east-1", "envs/prod/aws/"+movesAccount+"/us-east-1"
	dev, prod := filepath.Join(out, filepath.FromSlash(devRoot)), filepath.Join(out, filepath.FromSlash(prodRoot))
	renamedFrom, handedFrom, keptAt := importedAt(t, first, devRoot, renamed), importedAt(t, first, devRoot, handed), importedAt(t, first, devRoot, kept)

	// The root applied, as its README says: its state then has its queues.
	work := t.TempDir()
	workDev, workProd := filepath.Join(work, "dev"), filepath.Join(work, "prod")
	copyRootFiles(t, dev, workDev)
	applyRoot(ctx, t, workDev, execPath, plugins, nil)
	if err := os.Remove(filepath.Join(workDev, rootfile.ImportsFileName)); err != nil {
		t.Fatal(err)
	}

	fixture.apply(ctx, t, tfexec.Var("renamed_name=infraharvest-e2e-moves-after"), tfexec.Var("handed_env=prod"))
	runMovesImport(ctx, t, append(args, "--regenerate")...)
	second := coverageOf(t, out)
	checkCoverage(t, "--regenerate", second, nil)

	var renamedTo string
	for _, m := range second.Moved {
		if m.ID == renamed && m.FromRoot == devRoot && m.ToRoot == devRoot && m.From == renamedFrom && m.To != renamedFrom {
			renamedTo = m.To
		}
	}
	if renamedTo == "" {
		t.Fatalf("--regenerate: report.json doesn't move %s from %s: moved %+v", renamed, renamedFrom, second.Moved)
	}
	// The queue whose name is the same keeps its label.
	if at := importedAt(t, second, devRoot, kept); at != keptAt || slices.ContainsFunc(second.Moved, func(m report.Move) bool { return m.ID == kept }) {
		t.Errorf("--regenerate: %s is at %s, was at %s; moved %+v", kept, at, keptAt, second.Moved)
	}
	var handedTo string
	for _, d := range second.Departed {
		if d.ID == handed && d.FromRoot == devRoot && d.From == handedFrom && d.ToRoot == prodRoot {
			handedTo = d.To
		}
	}
	if handedTo == "" {
		t.Errorf("--regenerate: report.json doesn't hand %s over from %s to %s: departed %+v", handed, devRoot, prodRoot, second.Departed)
	}
	if len(second.IncompleteHandovers) != 0 {
		t.Errorf("--regenerate: incomplete handovers %+v", second.IncompleteHandovers)
	}
	moves := strings.Join(strings.Fields(readFile(t, filepath.Join(dev, rootfile.MovesFileName))), " ")
	for _, want := range []string{
		"moved { from = " + renamedFrom + " to = " + renamedTo + " }",
		"removed { from = " + handedFrom + " lifecycle { destroy = false } }",
	} {
		if !strings.Contains(moves, want) {
			t.Errorf("%s/%s doesn't have %q:\n%s", devRoot, rootfile.MovesFileName, want, moves)
		}
	}
	for _, d := range second.Directories {
		if d.Path == devRoot && !slices.ContainsFunc(d.Checks, func(c report.Check) bool { return c.Name == gate.CheckMoves && c.Passed && !c.Skipped }) {
			t.Errorf("%s: the gate doesn't pass %s: %+v", devRoot, gate.CheckMoves, d.Checks)
		}
	}
	// Each README names the other root, for the order to apply them in.
	for _, readme := range []struct{ dir, other string }{{dev, prodRoot}, {prod, devRoot}} {
		if content := readFile(t, filepath.Join(readme.dir, rootfile.ReadmeFileName)); !strings.Contains(content, readme.other) {
			t.Errorf("%s doesn't name %s:\n%s", filepath.Join(readme.dir, rootfile.ReadmeFileName), readme.other, content)
		}
	}

	// The root generated again, over the state the first apply left: the
	// renamed queue is moved, the other forgotten, nothing created or
	// destroyed.
	clearRootFiles(t, workDev)
	copyRootFiles(t, dev, workDev)
	plan := planRoot(ctx, t, workDev, execPath, plugins)
	moved, forgotten := false, false
	for _, rc := range plan.ResourceChanges {
		if rc.Mode != tfjson.ManagedResourceMode {
			continue
		}
		switch {
		case rc.Change.Actions.Create() || rc.Change.Actions.Delete() || rc.Change.Actions.Replace():
			t.Errorf("%s: %s is planned to %v", devRoot, rc.Address, rc.Change.Actions)
		case rc.Address == renamedTo && rc.PreviousAddress == renamedFrom:
			moved = true
		case rc.Address == handedFrom && rc.Change.Actions.Forget():
			forgotten = true
		}
	}
	if !moved || !forgotten {
		t.Errorf("%s: the plan moves %s to %s: %v, forgets %s: %v", devRoot, renamedFrom, renamedTo, moved, handedFrom, forgotten)
	}
	applyRoot(ctx, t, workDev, execPath, plugins, nil)

	// Then the root it goes to imports it, and neither root changes more.
	copyRootFiles(t, prod, workProd)
	if imported := checkNoChanges(ctx, t, workProd, execPath, plugins, nil); imported["aws_sqs_queue"] != 1 {
		t.Errorf("%s: imports %v, want the queue handed over", prodRoot, imported)
	}
	applyRoot(ctx, t, workProd, execPath, plugins, nil)
	for _, dir := range []string{workDev, workProd} {
		for _, rc := range planRoot(ctx, t, dir, execPath, plugins).ResourceChanges {
			if rc.Mode == tfjson.ManagedResourceMode && (!rc.Change.Actions.NoOp() || rc.Change.Importing != nil || rc.PreviousAddress != "") {
				t.Errorf("%s: after the apply, %s is planned to %v", dir, rc.Address, rc.Change.Actions)
			}
		}
	}
}

// queueURL returns the URL, the ID, of the fixture's queue called name.
func (f *appliedFixture) queueURL(ctx context.Context, t *testing.T, name string) string {
	t.Helper()
	state, err := f.tf.Show(ctx)
	if err != nil {
		t.Fatalf("seed: terraform show: %v", err)
	}
	for _, r := range state.Values.RootModule.Resources {
		if r.Type == "aws_sqs_queue" && r.Name == name {
			if id, ok := r.AttributeValues["id"].(string); ok && id != "" {
				return id
			}
		}
	}
	t.Fatalf("testdata/%s created no aws_sqs_queue.%s", f.name, name)
	return ""
}
