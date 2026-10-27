// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// moduleMovesAccount is the account testdata/aws-module-moves creates its
// roles in, which no other test uses.
const moduleMovesAccount = "999999999999"

// moduleMovesProfile is the profile the test imports moduleMovesAccount
// as.
const moduleMovesProfile = "infraharvest-e2e-module-moves"

// TestAWSRegenerateModules imports testdata/aws-module-moves, two roles
// each with a policy of its own, with --modules=none, so that each is a
// resource of the root, and applies the root, keeping its state, as a
// team would. Generated again (--regenerate) with --modules=local, the
// two roles, clusters of one shape, become calls of a generated local
// module: the root's moves.tf moves each resource into its call, which
// the gate checks (G8), and coverage.json says so. Over the state the
// first apply left, the root then plans each resource as moved, creating
// and destroying nothing, and, applied, plans no change. Once moves.tf is
// deleted, as its README says, and the root generated again with
// --modules=none, moves.tf moves each resource back out of its call, and
// the root plans the same.
//
// The root is applied in a copy of the output with its generated
// modules, at the same paths, as a team applies it in its repository.
func TestAWSRegenerateModules(t *testing.T) {
	checkModuleMoves(t, moduleMoves{
		fixture:   "aws-module-moves",
		account:   moduleMovesAccount,
		profile:   moduleMovesProfile,
		services:  "iam",
		modules:   "local",
		resources: 4,
		calls:     2,
	})
}

// curatedMovesAccount is the account testdata/aws-curated-moves creates
// its buckets in, which no other test uses.
const curatedMovesAccount = "989898989898"

// curatedMovesProfile is the profile the test imports curatedMovesAccount
// as.
const curatedMovesProfile = "infraharvest-e2e-curated-moves"

// TestAWSRegenerateCuratedModules is TestAWSRegenerateModules with
// testdata/aws-curated-moves, two buckets each with its versioning, and
// the public access block and encryption the emulator gives each, and
// --modules=registry: generated again, each bucket becomes a call of the
// curated s3-bucket module from the registry, which moves.tf moves its
// resources into, and, with --modules=none, out of again.
func TestAWSRegenerateCuratedModules(t *testing.T) {
	checkModuleMoves(t, moduleMoves{
		fixture:   "aws-curated-moves",
		account:   curatedMovesAccount,
		profile:   curatedMovesProfile,
		services:  "s3",
		modules:   "registry",
		resources: 8,
		calls:     2,
	})
}

// moduleMoves is a root of resources that --modules makes calls of
// modules: the fixture that creates them, in the account the test imports
// as the profile, of the services, the --modules that makes them calls,
// and how many resources and calls the root has.
type moduleMoves struct {
	fixture, account, profile, services, modules string
	resources, calls                             int
}

// checkModuleMoves imports c's fixture with --modules=none, applies the
// root, and generates it again with c.modules, then, once moves.tf is
// deleted, with --modules=none, as TestAWSRegenerateModules says.
func checkModuleMoves(t *testing.T, c moduleMoves) {
	t.Helper()
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
	seedApplied(ctx, t, c.fixture, execPath, plugins)
	useAWSAccount(t, c.account, c.profile)

	out := t.TempDir()
	importWith := func(modules string, more ...string) report.Report {
		t.Helper()
		runMovesImport(ctx, t, append([]string{
			"import", "aws",
			"--engine=" + engineName,
			"--terraform-path=" + execPath,
			"--profile=" + c.profile,
			"--regions=us-east-1",
			"--services=" + c.services,
			"--modules=" + modules,
			"--out=" + out,
			"--all",
		}, more...)...)
		coverage := coverageOf(t, out)
		checkCoverage(t, "--modules="+modules+" "+strings.Join(more, " "), coverage, nil)
		return coverage
	}
	first := importWith("none")
	if len(first.Directories) != 1 {
		t.Fatalf("the first import generated %+v, want one root", first.Directories)
	}
	rootPath := first.Directories[0].Path
	// By type and import ID, as a bucket and its versioning share an ID.
	plain := map[engine.External]string{} // where the root has each
	for _, r := range first.Directories[0].Imported {
		if strings.HasPrefix(r.Address, "module.") {
			t.Errorf("--modules=none: %s is in a module call", r.Address)
		}
		plain[engine.External{Type: engine.AddressType(r.Address), ID: r.ID}] = r.Address
	}
	if len(plain) != c.resources {
		t.Fatalf("%s imports %v, want %d resources", rootPath, plain, c.resources)
	}
	root := filepath.Join(out, filepath.FromSlash(rootPath))

	// The root applied, as its README says, where it is in the output.
	work := t.TempDir()
	workRoot := filepath.Join(work, filepath.FromSlash(rootPath))
	copyRootFiles(t, root, workRoot)
	applyRoot(ctx, t, workRoot, execPath, plugins, nil)
	if err := os.Remove(filepath.Join(workRoot, engine.ImportsFileName)); err != nil {
		t.Fatal(err)
	}

	// regenerate generates the root again with --modules=modules, and
	// checks it moves each resource from where from has it to where it is
	// now, which it returns, by type and import ID, and that the root,
	// over the state of the last apply, plans the moves alone, and,
	// applied, no change.
	regenerate := func(modules string, from map[engine.External]string) map[engine.External]string {
		t.Helper()
		coverage := importWith(modules, "--regenerate")
		now := map[engine.External]string{}
		for id, address := range from {
			now[id] = importedAs(t, coverage, rootPath, id)
			if !slices.ContainsFunc(coverage.Moved, func(m report.Move) bool {
				return m.Type == id.Type && m.ID == id.ID && m.FromRoot == rootPath && m.ToRoot == rootPath && m.From == address && m.To == now[id]
			}) {
				t.Errorf("--modules=%s: coverage.json doesn't move %s from %s to %s: moved %+v", modules, id, address, now[id], coverage.Moved)
			}
		}
		moves := strings.Join(strings.Fields(readFile(t, filepath.Join(root, engine.MovesFileName))), " ")
		for id, address := range from {
			if want := "moved { from = " + address + " to = " + now[id] + " }"; !strings.Contains(moves, want) {
				t.Errorf("--modules=%s: %s/%s doesn't have %q:\n%s", modules, rootPath, engine.MovesFileName, want, moves)
			}
		}
		for _, d := range coverage.Directories {
			if d.Path == rootPath && !slices.ContainsFunc(d.Checks, func(c report.Check) bool { return c.Name == engine.CheckMoves && c.Passed && !c.Skipped }) {
				t.Errorf("--modules=%s: %s: the gate doesn't pass %s: %+v", modules, rootPath, engine.CheckMoves, d.Checks)
			}
		}

		clearRootFiles(t, workRoot)
		copyRootFiles(t, root, workRoot)
		copyModules(t, out, work)
		moved := map[string]bool{}
		for _, rc := range planRoot(ctx, t, workRoot, execPath, plugins).ResourceChanges {
			if rc.Mode != tfjson.ManagedResourceMode {
				continue
			}
			if rc.Change.Actions.Create() || rc.Change.Actions.Delete() || rc.Change.Actions.Replace() {
				t.Errorf("--modules=%s: %s: %s is planned to %v", modules, rootPath, rc.Address, rc.Change.Actions)
			}
			if rc.PreviousAddress != "" {
				moved[rc.PreviousAddress+" "+rc.Address] = true
			}
		}
		for id, address := range from {
			if !moved[address+" "+now[id]] {
				t.Errorf("--modules=%s: %s: the plan doesn't move %s to %s", modules, rootPath, address, now[id])
			}
		}
		applyRoot(ctx, t, workRoot, execPath, plugins, nil)
		for _, rc := range planRoot(ctx, t, workRoot, execPath, plugins).ResourceChanges {
			if rc.Mode == tfjson.ManagedResourceMode && (!rc.Change.Actions.NoOp() || rc.Change.Importing != nil || rc.PreviousAddress != "") {
				t.Errorf("--modules=%s: %s: after the apply, %s is planned to %v", modules, rootPath, rc.Address, rc.Change.Actions)
			}
		}
		return now
	}

	// Into calls of modules.
	called := regenerate(c.modules, plain)
	calls := map[string]bool{}
	for _, address := range called {
		if !strings.HasPrefix(address, "module.") {
			t.Errorf("--modules=%s: %s isn't in a module call", c.modules, address)
			continue
		}
		calls[strings.SplitN(address, ".", 3)[1]] = true
	}
	if len(calls) != c.calls {
		t.Errorf("--modules=%s: the resources are in the calls %v, want %d", c.modules, calls, c.calls)
	}

	// Out of them again, once the moves are applied and moves.tf deleted.
	if err := os.Remove(filepath.Join(root, engine.MovesFileName)); err != nil {
		t.Fatal(err)
	}
	for id, address := range regenerate("none", called) {
		if strings.HasPrefix(address, "module.") {
			t.Errorf("--modules=none: %s (%s) is still in a module call", address, id)
		}
	}
}

// copyModules copies the generated local modules of the output out, the
// files under its modules directory, into the same directory of work,
// replacing what that had.
func copyModules(t *testing.T, out, work string) {
	t.Helper()
	src, dst := filepath.Join(out, engine.ModulesDirName), filepath.Join(work, engine.ModulesDirName)
	if err := os.RemoveAll(dst); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".terraform" {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dst, rel)), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), []byte(readFile(t, path)), 0o600)
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// importedAs returns the address coverage has the resource of id at, in
// the root at path.
func importedAs(t *testing.T, coverage report.Report, path string, id engine.External) string {
	t.Helper()
	for _, d := range coverage.Directories {
		if d.Path != path {
			continue
		}
		for _, r := range d.Imported {
			if r.ID == id.ID && engine.AddressType(r.Address) == id.Type {
				return r.Address
			}
		}
	}
	t.Fatalf("%s doesn't import %s %s: %+v", path, id.Type, id.ID, coverage.Directories)
	return ""
}
