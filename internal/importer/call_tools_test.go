// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// errStub is what every stubbed tool returns, so a test tells the stub's
// answer from the real tool's.
var errStub = errors.New("stub")

// A tool a test sets is the one a call reaches, with the arguments the call
// was given, and what it returns is what the call returns.
func TestCallToolsUseTheToolSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	binary := tfrun.Binary{Name: "terraform"}
	provider := engine.Provider{Name: "aws", Source: "hashicorp/aws"}
	imports := []engine.Import{{Type: "aws_sqs_queue", Name: "orders", ID: "q-1"}}
	config := map[string][]byte{"versions.tf": []byte("terraform {}\n")}
	opts := engine.Options{Config: config}
	result := &engine.Result{}
	roots := []engine.MergeRoot{{Env: "dev", Dir: "dev"}, {Env: "prod", Dir: "prod"}}
	moved := []engine.Move{{From: "aws_sqs_queue.a", To: "aws_sqs_queue.b"}}
	removed := []engine.Removal{{From: "aws_sqs_queue.c"}}
	gone := map[string][]engine.Import{"prod": imports}
	existing := map[engine.External]string{{Type: "aws_vpc", ID: "vpc-1"}: "data.aws_vpc.main"}
	for _, tc := range []struct {
		name string
		// set sets the tool, which records its arguments into args.
		set  func(tools *callTools, args *[]any)
		call func(tools *callTools) error
		want []any
	}{
		{
			name: "find",
			set: func(tools *callTools, args *[]any) {
				tools.findBinary = func(_ context.Context, b tfrun.Binary, explicit, cache string) (string, error) {
					*args = []any{b, explicit, cache}
					return "", errStub
				}
			},
			call: func(tools *callTools) error {
				_, err := tools.find(ctx, binary, "/bin/terraform", "/cache")
				return err
			},
			want: []any{binary, "/bin/terraform", "/cache"},
		},
		{
			name: "root",
			set: func(tools *callTools, args *[]any) {
				tools.rootConfig = func(_ context.Context, registry, execPath string, p engine.Provider, _ []moduleConstraint) (*rootFiles, error) {
					*args = []any{registry, execPath, p}
					return nil, errStub
				}
			},
			call: func(tools *callTools) error {
				_, err := tools.root(ctx, "registry.opentofu.org", "/bin/tofu", provider, nil)
				return err
			},
			want: []any{"registry.opentofu.org", "/bin/tofu", provider},
		},
		{
			name: "generate",
			set: func(tools *callTools, args *[]any) {
				tools.generate = func(_ context.Context, dir string, _ terraformRun, imps []engine.Import, o engine.Options, _ *[]byte) (*engine.Result, error) {
					*args = []any{dir, imps, o.Config}
					return nil, errStub
				}
			},
			call: func(tools *callTools) error {
				var lock []byte
				_, err := tools.generateDir(ctx, "out", terraformRun{}, imports, opts, &lock)
				return err
			},
			want: []any{"out", imports, config},
		},
		{
			name: "add",
			set: func(tools *callTools, args *[]any) {
				tools.addTo = func(_ context.Context, _, _ tfrun.Terraform, staging, root string, imps []engine.Import, _ engine.Options, ext map[engine.External]string) (*engine.Result, error) {
					*args = []any{staging, root, imps, ext}
					return nil, errStub
				}
			},
			call: func(tools *callTools) error {
				_, err := tools.add(ctx, nil, nil, "staging", "root", imports, opts, existing)
				return err
			},
			want: []any{"staging", "root", imports, existing},
		},
		{
			name: "vacate",
			set: func(tools *callTools, args *[]any) {
				tools.vacateRoot = func(dir string, o engine.Options) (*engine.Result, error) {
					*args = []any{dir, o.Config}
					return nil, errStub
				}
			},
			call: func(tools *callTools) error {
				_, err := tools.vacate("root", opts)
				return err
			},
			want: []any{"root", config},
		},
		{
			name: "readme",
			set: func(tools *callTools, args *[]any) {
				tools.readme = func(dir string, r *engine.Result) error {
					*args = []any{dir, r}
					return errStub
				}
			},
			call: func(tools *callTools) error { return tools.writeReadme("root", result) },
			want: []any{"root", result},
		},
		{
			name: "merge",
			set: func(tools *callTools, args *[]any) {
				tools.merge = func(_ context.Context, dir string, r []engine.MergeRoot, _ engine.MergeOptions) (*engine.MergeResult, error) {
					*args = []any{dir, r}
					return nil, errStub
				}
			},
			call: func(tools *callTools) error {
				_, err := tools.mergeRoots(ctx, "merged", roots, engine.MergeOptions{})
				return err
			},
			want: []any{"merged", roots},
		},
		{
			name: "vacate merged",
			set: func(tools *callTools, args *[]any) {
				tools.vacateMergedRoot = func(dir string, files []string, r []engine.Removal) ([]string, gate.Check, error) {
					*args = []any{dir, files, r}
					return nil, gate.Check{}, errStub
				}
			},
			call: func(tools *callTools) error {
				_, _, err := tools.vacateMerged("merged", []string{"main.tf"}, removed)
				return err
			},
			want: []any{"merged", []string{"main.tf"}, removed},
		},
		{
			name: "merged not in cloud",
			set: func(tools *callTools, args *[]any) {
				tools.mergedNotInCloud = func(dir string, g map[string][]engine.Import) error {
					*args = []any{dir, g}
					return errStub
				}
			},
			call: func(tools *callTools) error { return tools.writeMergedNotInCloud("merged", gone) },
			want: []any{"merged", gone},
		},
		{
			name: "merged moves",
			set: func(tools *callTools, args *[]any) {
				tools.mergedMoves = func(dir string, m []engine.Move, r []engine.Removal) (gate.Check, error) {
					*args = []any{dir, m, r}
					return gate.Check{}, errStub
				}
			},
			call: func(tools *callTools) error {
				_, err := tools.writeMergedMoves("merged", moved, removed)
				return err
			},
			want: []any{"merged", moved, removed},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var args []any
			tools := &callTools{}
			tc.set(tools, &args)
			if err := tc.call(tools); !errors.Is(err, errStub) {
				t.Fatalf("err = %v, want the stub's", err)
			}
			if !reflect.DeepEqual(args, tc.want) {
				t.Errorf("args = %#v, want %#v", args, tc.want)
			}
		})
	}
}

// A nil *callTools, or one whose tool isn't set, reaches the real tool:
// each one here fails fast, before running anything, on what it was given,
// and says so in its own words.
func TestCallToolsFallBackToTheRealTool(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		call func(tools *callTools, dir string) error
		// want is in the real tool's error; "" wants no error.
		want string
	}{
		{
			name: "find",
			call: func(tools *callTools, dir string) error {
				_, err := tools.find(ctx, tfrun.Binary{Name: "terraform"}, filepath.Join(dir, "terraform"), dir)
				return err
			},
			want: "terraform",
		},
		{
			name: "root",
			call: func(tools *callTools, dir string) error {
				_, err := tools.root(ctx, "", filepath.Join(dir, "terraform"), engine.Provider{Source: "hashicorp/aws"}, nil)
				return err
			},
			want: "terraform",
		},
		{
			name: "vacate",
			call: func(tools *callTools, dir string) error {
				_, err := tools.vacate(dir, engine.Options{})
				return err
			},
			want: "no resources to hand over",
		},
		{
			name: "readme",
			call: func(tools *callTools, dir string) error {
				if err := tools.writeReadme(dir, &engine.Result{}); err != nil {
					return err
				}
				_, err := os.Stat(filepath.Join(dir, engine.ReadmeFileName))
				return err
			},
		},
		{
			name: "merge",
			call: func(tools *callTools, dir string) error {
				_, err := tools.mergeRoots(ctx, dir, nil, engine.MergeOptions{})
				return err
			},
			want: "two environments or more",
		},
		{
			name: "vacate merged",
			call: func(tools *callTools, dir string) error {
				_, _, err := tools.vacateMerged(dir, nil, nil)
				return err
			},
			want: "no resources to hand over",
		},
		{
			// There is no README to add to.
			name: "merged not in cloud",
			call: func(tools *callTools, dir string) error {
				return tools.writeMergedNotInCloud(dir, map[string][]engine.Import{"prod": {{Type: "aws_sqs_queue", Name: "q", ID: "q-1"}}})
			},
			want: engine.ReadmeFileName,
		},
	} {
		for _, tools := range []struct {
			name  string
			tools *callTools
		}{{"nil", nil}, {"unset", &callTools{}}} {
			t.Run(tc.name+"/"+tools.name, func(t *testing.T) {
				t.Parallel()
				err := tc.call(tools.tools, t.TempDir())
				switch {
				case tc.want == "" && err != nil:
					t.Fatalf("err = %v, want none", err)
				case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
					t.Fatalf("err = %v, want the real tool's, about %q", err, tc.want)
				}
			})
		}
	}
}
