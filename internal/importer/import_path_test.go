// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	goversion "github.com/hashicorp/go-version"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// rootsProvider lists, for each service, one fake_thing named after the
// service, with the ID id-<service>.
type rootsProvider struct {
	harvest.Provider
	service *rootsService
}

func (p *rootsProvider) Init(context.Context, []string) error  { return nil }
func (p *rootsProvider) Name() string                          { return "fake" }
func (p *rootsProvider) Selected() harvest.Lister              { return p.service }
func (p *rootsProvider) ProviderBlock() map[string]interface{} { return map[string]interface{}{} }
func (p *rootsProvider) Select(name string, _ bool) error {
	p.service = &rootsService{}
	p.service.Configure(harvest.ListerConfig{Service: name})
	return nil
}

type rootsService struct {
	harvest.ListerBase
}

func (s *rootsService) List() error {
	s.Resources = []harvest.Resource{harvest.RecordBare("id-"+s.Name(), s.Name(), "fake_thing", "fake")}
	return nil
}

// fakeEngine stands in for the engine binary, the registry and the
// engine's generation of a root: each root it generates gets the files a
// resumed root must have, and imports what it was asked to.
type fakeEngine struct {
	mu sync.Mutex
	// generated are the roots generated, in order, and opts the engine
	// options each was generated with, by directory.
	generated []string
	opts      map[string]engine.Options
	imports   map[string][]engine.Import
	// fail is the error generating a root whose directory ends in it
	// returns, if set.
	fail string
	// label, if set, gives the label of each resource the root imports,
	// for its address; its name otherwise.
	label func(engine.Import) string
}

func (f *fakeEngine) tools() *callTools {
	return &callTools{
		findBinary: func(context.Context, engine.Binary, string, string) (string, error) {
			return "terraform", nil
		},
		rootConfig: func(_ context.Context, _, _ string, p engine.Provider, _ []moduleConstraint) (*rootFiles, error) {
			p.Version = "~> 1.0"
			return &rootFiles{
				files:         map[string][]byte{engine.VersionsFileName: []byte("terraform {}\n")},
				engineVersion: "1.9.0",
				provider:      p,
				version:       goversion.Must(goversion.NewVersion("1.0.0")),
			}, nil
		},
		generate: func(_ context.Context, dir string, _ terraformRun, imports []engine.Import, opts engine.Options, _ *[]byte) (*engine.Result, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.generated = append(f.generated, filepath.Base(dir))
			if f.opts == nil {
				f.opts, f.imports = map[string]engine.Options{}, map[string][]engine.Import{}
			}
			f.opts[filepath.Base(dir)] = opts
			f.imports[filepath.Base(dir)] = imports
			if f.fail != "" && filepath.Base(dir) == f.fail {
				return nil, errors.New("plan failed")
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
			for _, name := range []string{engine.ImportsFileName, engine.VersionsFileName, engine.GeneratedFileName} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("# fake\n"), 0o644); err != nil {
					return nil, err
				}
			}
			result := &engine.Result{Imported: imports}
			for _, imp := range imports {
				label := imp.Name
				if f.label != nil {
					label = f.label(imp)
				}
				result.Addresses = append(result.Addresses, engine.Managed{Type: imp.Type, ID: imp.ID, Address: imp.Type + "." + label})
			}
			return result, nil
		},
	}
}

// pathOptions are the options of an import of services, one root each,
// into out, through f.
func pathOptions(out string, f *fakeEngine, services ...string) Options {
	return Options{
		All:          true,
		Out:          out,
		Layout:       "{out}/{provider}/{service}/",
		Services:     services,
		Modules:      ModulesNone,
		ListParallel: 1,
		tools:        f.tools(),
	}
}

// importPath lists and generates one Import call into a new run, as
// importInto does, and returns the run.
func importPath(t *testing.T, options Options) *Run {
	t.Helper()
	run := NewRun()
	c, err := listCall(t.Context(), run, &rootsProvider{}, options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatal("no call listed")
	}
	if err := c.generate(t.Context(), c.byDir); err != nil {
		t.Fatal(err)
	}
	return run
}

// directoryPaths are the paths of the directories run reports, with
// whether each failed.
func directoryPaths(run *Run) []string {
	var paths []string
	for _, d := range run.report.Directories {
		path := d.Path
		if d.Error != "" {
			path += " (failed)"
		}
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}

// An Import call lists, selects and lays out its resources, then
// generates each root with the files every root starts with, the other
// roots it may refer to, and a checkpoint; the run counts and reports
// what it did.
func TestListCallGeneratesEachRoot(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	f := &fakeEngine{}
	run := importPath(t, pathOptions(out, f, "alpha", "beta"))

	if !slices.Equal(f.generated, []string{"alpha", "beta"}) {
		t.Fatalf("generated %v, want alpha then beta", f.generated)
	}
	alpha := f.opts["alpha"]
	if string(alpha.Config[engine.VersionsFileName]) != "terraform {}\n" {
		t.Errorf("root files: %v", alpha.Config)
	}
	if len(alpha.OtherRoots) != 1 || alpha.OtherRoots[0].ID != "id-beta" || alpha.OtherRoots[0].Root != "fake/beta" {
		t.Errorf("alpha's other roots: %+v", alpha.OtherRoots)
	}
	if imports := f.imports["alpha"]; len(imports) != 1 || imports[0].ID != "id-alpha" || imports[0].Type != "fake_thing" {
		t.Errorf("alpha's imports: %+v", imports)
	}
	if got := directoryPaths(run); !slices.Equal(got, []string{"fake/alpha", "fake/beta"}) {
		t.Errorf("reported directories %v", got)
	}
	if run.discovered["fake_thing"] != 2 {
		t.Errorf("discovered %v", run.discovered)
	}
	for _, service := range []string{"alpha", "beta"} {
		c, err := readCheckpoint(out, filepath.Join(out, "fake", service))
		if err != nil || c == nil || c.Result == nil {
			t.Errorf("%s: want a finished checkpoint, got %+v, %v", service, c, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, ".gitignore")); err != nil {
		t.Errorf("no .gitignore: %v", err)
	}
}

// The calls of one scope list first, so that each root refers to the
// roots of the other calls (see scopeRoots).
func TestListedCallsReferToEachOthersRoots(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	f := &fakeEngine{}
	run := NewRun()
	var listed []*listedCall
	for _, service := range []string{"alpha", "beta"} {
		c, err := listCall(t.Context(), run.child(), &rootsProvider{}, pathOptions(out, f, service), nil)
		if err != nil {
			t.Fatal(err)
		}
		listed = append(listed, c)
	}
	byDirs := []map[string][]engine.Import{listed[0].byDir, listed[1].byDir}
	for i, c := range listed {
		if err := c.generate(t.Context(), scopeRoots(byDirs, i)); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(f.generated, []string{"alpha", "beta"}) {
		t.Fatalf("generated %v, want alpha then beta", f.generated)
	}
	if other := f.opts["beta"].OtherRoots; len(other) != 1 || other[0].ID != "id-alpha" || other[0].Root != "fake/alpha" {
		t.Errorf("beta's other roots: %+v", other)
	}
	if imports := f.imports["beta"]; len(imports) != 1 || imports[0].ID != "id-beta" {
		t.Errorf("beta generates the other call's resources too: %+v", imports)
	}
}

// --resume skips the roots an identical import generated, and generates
// again those whose input changed.
func TestListCallResumes(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	importPath(t, pathOptions(out, &fakeEngine{}, "alpha", "beta"))

	f := &fakeEngine{}
	options := pathOptions(out, f, "alpha", "beta")
	options.Resume = true
	run := importPath(t, options)
	if len(f.generated) != 0 {
		t.Errorf("an identical import generated %v again", f.generated)
	}
	if got := directoryPaths(run); !slices.Equal(got, []string{"fake/alpha", "fake/beta"}) {
		t.Errorf("resumed directories %v", got)
	}
	if d := run.report.Directories; len(d) != 2 || len(d[0].Imported) != 1 {
		t.Errorf("resumed roots report what they import: %+v", d)
	}

	// Another root changes what alpha and beta may refer to: --resume
	// generates only the new root, as the others may be in use.
	f = &fakeEngine{}
	options = pathOptions(out, f, "alpha", "beta", "gamma")
	options.Resume = true
	run = importPath(t, options)
	if !slices.Equal(f.generated, []string{"gamma"}) {
		t.Errorf("generated %v, want the new root only", f.generated)
	}
	if got := directoryPaths(run); !slices.Equal(got, []string{"fake/alpha (failed)", "fake/beta (failed)", "fake/gamma"}) {
		t.Errorf("reported directories %v", got)
	}
	if len(run.failures) != 2 || !strings.Contains(run.failures[0].Error(), "--regenerate") {
		t.Errorf("failures %v, want the roots in use refused", run.failures)
	}

	// --regenerate generates every root again.
	f = &fakeEngine{}
	options = pathOptions(out, f, "alpha", "beta", "gamma")
	options.Regenerate = true
	run = importPath(t, options)
	if !slices.Equal(f.generated, []string{"alpha", "beta", "gamma"}) || len(run.failures) != 0 {
		t.Errorf("generated %v, failures %v, want every root again", f.generated, run.failures)
	}
}

// An import into roots an earlier import generated is refused without
// --regenerate or --incremental. --regenerate generates them again, but
// for a root whose resources get other addresses, which is left as it
// was and reported as failed.
func TestListCallRegeneratesRootsWhoseAddressesStay(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	importPath(t, pathOptions(out, &fakeEngine{}, "alpha", "beta"))
	alpha := filepath.Join(out, "fake", "alpha")
	before, err := readCheckpoint(out, alpha)
	if err != nil || before == nil || len(before.Result.Addresses) != 1 {
		t.Fatalf("alpha's checkpoint: %+v, %v", before, err)
	}

	f := &fakeEngine{}
	run := importPath(t, pathOptions(out, f, "alpha", "beta"))
	if len(f.generated) != 0 {
		t.Errorf("generated %v without a flag", f.generated)
	}
	if len(run.failures) != 2 {
		t.Fatalf("failures %v, want both roots refused", run.failures)
	}
	for _, err := range run.failures {
		if !strings.Contains(err.Error(), "--regenerate") || !strings.Contains(err.Error(), "--incremental") {
			t.Errorf("failure %v doesn't name --regenerate and --incremental", err)
		}
	}

	f = &fakeEngine{label: func(imp engine.Import) string {
		if imp.ID == "id-alpha" {
			return "renamed"
		}
		return imp.Name
	}}
	options := pathOptions(out, f, "alpha", "beta")
	options.Regenerate = true
	run = importPath(t, options)
	if !slices.Equal(f.generated, []string{"alpha", "beta"}) {
		t.Errorf("generated %v, want both roots again", f.generated)
	}
	if got := directoryPaths(run); !slices.Equal(got, []string{"fake/alpha (failed)", "fake/beta"}) {
		t.Errorf("reported directories %v", got)
	}
	if len(run.failures) != 1 || !strings.Contains(run.failures[0].Error(), "is now fake_thing.renamed") {
		t.Errorf("failures %v, want alpha refused with its new address", run.failures)
	}
	if after, err := readCheckpoint(out, alpha); err != nil || !reflect.DeepEqual(after, before) {
		t.Errorf("alpha's checkpoint: %+v, %v, want it as it was, %+v", after, err, before)
	}
}

// A root that holds files no import wrote is left alone and reported as
// failed, as is one the engine fails for; the others are generated.
func TestListCallRecordsRootsThatFail(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	foreign := filepath.Join(out, "fake", "alpha")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "main.tf"), []byte("# someone's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeEngine{fail: "gamma"}
	run := importPath(t, pathOptions(out, f, "alpha", "beta", "gamma"))

	if !slices.Equal(f.generated, []string{"beta", "gamma"}) {
		t.Errorf("generated %v, want beta and gamma only", f.generated)
	}
	if got := directoryPaths(run); !slices.Equal(got, []string{"fake/alpha (failed)", "fake/beta", "fake/gamma (failed)"}) {
		t.Errorf("reported directories %v", got)
	}
	if len(run.failures) != 2 || !strings.Contains(run.failures[0].Error(), "no import by infraharvest wrote") || !strings.Contains(run.failures[1].Error(), "plan failed") {
		t.Errorf("failures %v", run.failures)
	}
	if run.failed["fake_thing"] != 2 {
		t.Errorf("failed %v", run.failed)
	}
}

// --incremental leaves out what an earlier import put in another root,
// and generates a new root with what is new.
func TestListCallIncrementalLeavesOutWhatOtherRootsHold(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	importPath(t, pathOptions(out, &fakeEngine{}, "alpha", "beta"))

	f := &fakeEngine{}
	options := pathOptions(out, f, "alpha", "beta", "gamma")
	options.Layout = "{out}/{provider}/all/"
	options.Incremental = true
	run := importPath(t, options)

	if !slices.Equal(f.generated, []string{"all"}) {
		t.Fatalf("generated %v, want the new root only", f.generated)
	}
	if imports := f.imports["all"]; len(imports) != 1 || imports[0].ID != "id-gamma" {
		t.Errorf("the new root imports %+v, want gamma only", imports)
	}
	var excluded []string
	for _, e := range run.report.Excluded {
		if strings.HasPrefix(e.Reason, InOtherRootReason) {
			excluded = append(excluded, e.ID)
		}
	}
	slices.Sort(excluded)
	if !slices.Equal(excluded, []string{"id-alpha", "id-beta"}) {
		t.Errorf("left out as held elsewhere: %v", excluded)
	}
}
