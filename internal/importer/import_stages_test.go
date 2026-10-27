// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	goversion "github.com/hashicorp/go-version"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// Preparing a call gives the provider the catalogue, and resolves the
// services "*" stands for, without --skip's, into the call: its options
// stay as the command gave them.
func TestPrepareCallResolvesBesideTheOptions(t *testing.T) {
	provider := &catalogueProvider{}
	options := Options{Services: []string{"*"}, Skip: []string{"vpc"}, All: true}

	c, err := NewRun().prepareCall(t.Context(), provider, options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if provider.cat == nil || provider.cat != c.cat {
		t.Errorf("the provider has catalogue %p, the call %p", provider.cat, c.cat)
	}
	// The catalogue comes first: it adds ivs.
	if !slices.Equal(c.services, []string{"ivs"}) {
		t.Errorf("services %v, want [ivs]", c.services)
	}
	if !reflect.DeepEqual(c.options, options) || !slices.Equal(options.Services, []string{"*"}) {
		t.Errorf("options %+v, want them as given, %+v", c.options, options)
	}
	if c.importID == nil {
		t.Error("no import IDs")
	}
}

// A call and its run keep the --layout they were given; the roots go where
// it says, with the account and region in.
func TestListCallKeepsTheLayoutAsGiven(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	options := pathOptions(out, &fakeEngine{}, "alpha")
	const layout = "{out}/{provider}/{account}/{service}/"
	options.Layout = layout
	run := options.run()

	c, err := listCall(t.Context(), run, &rootsProvider{}, options.Options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.options.Layout != layout || run.options.Layout != layout {
		t.Errorf("layout of the call %q, of the run %q, want %q", c.options.Layout, run.options.Layout, layout)
	}
	if _, ok := c.byDir[filepath.Join(out, "fake", "default", "alpha")]; !ok {
		t.Errorf("roots %v, want fake/default/alpha", c.byDir)
	}
}

// The toolchain has the binary and root files the call's tools give, and
// the modules of its --modules mode: none without modules, nor with
// --cfn-stack unless chosen, and generated local modules under --out
// otherwise.
func TestToolchainFollowsTheModulesMode(t *testing.T) {
	out := "out"
	local := filepath.Join(out, engine.ModulesDirName)
	for _, tc := range []struct {
		name       string
		options    Options
		modulesDir string
	}{
		{"none", Options{Modules: ModulesNone}, ""},
		{"local", Options{Modules: ModulesLocal}, local},
		{"registry", Options{Modules: ModulesRegistry}, local},
		{"registry with --cfn-stack", Options{Modules: ModulesRegistry, CloudFormationStacks: []string{"app"}}, ""},
		{"registry chosen with --cfn-stack", Options{Modules: ModulesRegistry, ModulesChosen: true, CloudFormationStacks: []string{"app"}}, local},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.options.Out = out
			run := NewRun()
			run.tools = (&fakeEngine{}).tools()

			tools, err := run.toolchain(t.Context(), &rootsProvider{}, &callContext{options: tc.options}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tools.execPath != "terraform" || tools.binary.Name != engine.TerraformBinary.Name || tools.root.engineVersion != "1.9.0" {
				t.Errorf("binary %+v at %q, root files %+v", tools.binary, tools.execPath, tools.root)
			}
			if tools.modulesDir != tc.modulesDir || len(tools.adapters) != 0 {
				t.Errorf("modules in %q, adapters %+v; want %q, none", tools.modulesDir, tools.adapters, tc.modulesDir)
			}
		})
	}
}

// The engine's options come together in one place: the catalogue's, for
// the release the root files pin, the toolchain's files and modules, the
// flags the engine takes, and what the selection leaves out.
func TestEngineOptionsOfTheStages(t *testing.T) {
	cat := catalogue.MustBuiltin("aws")
	v := goversion.Must(goversion.NewVersion("6.0.0"))
	c := &callContext{options: Options{IncludeOwned: true, EngineTimeout: time.Minute}, cat: cat, importID: listerID}
	external := []engine.External{{Type: "aws_vpc", ID: "vpc-1"}}
	files := map[string][]byte{engine.VersionsFileName: []byte("terraform {}\n")}
	own := []adapters.Adapter{{Source: "/modules/bucket"}}
	tools := &toolchain{root: &rootFiles{files: files, version: v}, adapters: own, modulesDir: "modules"}

	opts := engineOptions(&rootsProvider{}, c, &callListing{leftOut: external}, tools)

	if catOpts := catalogueEngineOptions(cat, v); !reflect.DeepEqual(opts.Omit, catOpts.Omit) || !reflect.DeepEqual(opts.StateOnly, catOpts.StateOnly) || opts.ARNs == nil {
		t.Error("the catalogue's options are missing")
	}
	if !reflect.DeepEqual(opts.Config, files) || !reflect.DeepEqual(opts.Adapters, own) || opts.ModulesDir != "modules" {
		t.Errorf("toolchain: files %v, adapters %+v, modules in %q", opts.Config, opts.Adapters, opts.ModulesDir)
	}
	if !opts.IncludeOwned || opts.CommandTimeout != time.Minute || !reflect.DeepEqual(opts.External, external) {
		t.Errorf("flags and selection: owned %v, timeout %s, external %+v", opts.IncludeOwned, opts.CommandTimeout, opts.External)
	}
	if !opts.SplitFiles || !opts.TidyLabels {
		t.Errorf("split %v, tidy %v", opts.SplitFiles, opts.TidyLabels)
	}
}

// Placing a call's resources splits them into roots by their environment
// tags, and writes each import block whole: the provider left to its
// type, and what the catalogue leaves out by what the lister recorded.
func TestPlaceWritesWholeImportsIntoEachRoot(t *testing.T) {
	prod := harvest.Record("i-1", "app", "aws_instance", "aws", map[string]string{"cpu_options": "default"})
	dev := harvest.RecordBare("i-2", "db", "aws_instance", "aws")
	listed := listing{tags: map[string]map[string]string{tagKey(prod): {"Env": "prod"}, tagKey(dev): {"Env": "dev"}}}
	c := &callContext{options: Options{Out: "out", EnvTags: []string{"Env"}}, cat: catalogue.MustBuiltin("aws"), importID: listerID}
	layout := callLayout{pattern: "{out}/{provider}/{env}/", root: "{out}/{provider}/{env}/"}
	resources := callResources{byService: map[string][]harvest.Resource{"ec2_instance": {prod, dev}}}

	p, err := NewRun().place(t.Context(), &rootsProvider{}, c, layout, listed, resources, map[string]engine.Provider{"aws": {Name: "aws"}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]engine.Import{
		filepath.Join("out", "fake", "prod"): {{Type: "aws_instance", Name: "app", File: "ec2_instance.tf", ID: "i-1", Omit: []string{"cpu_options"}}},
		filepath.Join("out", "fake", "dev"):  {{Type: "aws_instance", Name: "db", File: "ec2_instance.tf", ID: "i-2"}},
	}
	if !reflect.DeepEqual(p.byDir, want) {
		t.Errorf("got %+v, want %+v", p.byDir, want)
	}
	if p.kept != nil || p.places != nil {
		t.Errorf("kept %+v, places %+v, without the configuration file's environments", p.kept, p.places)
	}
}
