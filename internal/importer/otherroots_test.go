// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// Each call of a scope sees the roots of every other call, before and
// after it: a regional root refers to the global root's role, and the
// global root to the regional function.
func TestScopeRootsGoBothWays(t *testing.T) {
	role := rootfile.Import{Type: "aws_iam_role", ID: "app", Name: "app"}
	fn := rootfile.Import{Type: "aws_lambda_function", ID: "fn", Name: "fn"}
	byDirs := []map[string][]rootfile.Import{
		{"out/aws/111/global": {role}},
		nil, // a call that generates nothing
		{"out/aws/111/eu-west-2": {fn}},
	}
	want := map[string][]rootfile.Import{"out/aws/111/global": {role}, "out/aws/111/eu-west-2": {fn}}
	for _, own := range []int{0, 2} {
		if got := scopeRoots(byDirs, own); !reflect.DeepEqual(got, want) {
			t.Errorf("call %d: got %v, want %v", own, got, want)
		}
	}

	opts := engine.Options{DataSources: map[string]engine.DataSource{"aws_lambda_function": {Type: "aws_lambda_function", Argument: "function_name"}}}
	regional := newOtherRoots(opts, scopeRoots(byDirs, 2), "out").of("out/aws/111/eu-west-2")
	if want := []engine.RootResource{{Type: "aws_iam_role", ID: "app", Name: "app", Root: "aws/111/global"}}; !reflect.DeepEqual(regional.OtherRoots, want) {
		t.Errorf("regional root: got %v, want %v", regional.OtherRoots, want)
	}
	global := newOtherRoots(opts, scopeRoots(byDirs, 0), "out").of("out/aws/111/global")
	if want := []engine.RootResource{{Type: "aws_lambda_function", ID: "fn", Name: "fn", Root: "aws/111/eu-west-2"}}; !reflect.DeepEqual(global.OtherRoots, want) {
		t.Errorf("global root: got %v, want %v", global.OtherRoots, want)
	}
	if want := []engine.External{{Type: "aws_lambda_function", ID: "fn", Name: "fn"}}; !reflect.DeepEqual(global.External, want) {
		t.Errorf("global root's external: got %v, want %v", global.External, want)
	}

	// A call's own directory is its own, whatever another call puts
	// there; a call on its own sees only its roots.
	same := []map[string][]rootfile.Import{{"out/a": {role}}, {"out/a": {fn}}}
	if got := scopeRoots(same, 1)["out/a"]; !reflect.DeepEqual(got, []rootfile.Import{fn}) {
		t.Errorf("the call's own directory: got %v", got)
	}
	if got := scopeRoots(byDirs[:1], 0); !reflect.DeepEqual(got, byDirs[0]) {
		t.Errorf("one call: got %v", got)
	}
}

// TestOtherRootsOfEachRoot checks the options otherRoots gives each root
// against those of a root built on its own, as for one root at a time:
// the resources of every other root, its options' own first by name, each
// resource once.
func TestOtherRootsOfEachRoot(t *testing.T) {
	t.Parallel()
	opts := engine.Options{
		External:    []engine.External{{Type: "aws_vpc", ID: "vpc-1"}, {Type: "aws_vpc", ID: "vpc-1", Name: "main"}},
		OtherRoots:  []engine.RootResource{{Type: "aws_iam_role", ID: "app", Name: "app", Root: "elsewhere"}},
		DataSources: map[string]engine.DataSource{"aws_vpc": {Type: "aws_vpc", Argument: "id"}, "aws_iam_role": {Type: "aws_iam_role", Argument: "name"}},
	}
	byDir := map[string][]rootfile.Import{}
	for d := range 4 {
		dir := fmt.Sprintf("out/root%d", d)
		for i := range 12 {
			typ := []string{"aws_vpc", "aws_iam_role", "aws_s3_bucket"}[i%3]
			// IDs repeat across roots, some named and some not.
			imp := rootfile.Import{Type: typ, ID: fmt.Sprintf("id-%d", (i+d)%5)}
			if (i+d)%2 == 0 {
				imp.Name = fmt.Sprintf("n_%d_%d", d, i)
			}
			byDir[dir] = append(byDir[dir], imp)
		}
	}
	others := newOtherRoots(opts, byDir, "out")
	for dir := range byDir {
		got := others.of(dir)
		want := ownRootOptions(opts, byDir, dir, "out")
		if !reflect.DeepEqual(got.External, want.External) {
			t.Errorf("%s: external\n got %v\nwant %v", dir, got.External, want.External)
		}
		if !reflect.DeepEqual(got.OtherRoots, want.OtherRoots) {
			t.Errorf("%s: other roots\n got %v\nwant %v", dir, got.OtherRoots, want.OtherRoots)
		}
	}
}

// ownRootOptions builds the options of the root in dir on their own, from
// every other root: what otherRoots.of must return.
func ownRootOptions(opts engine.Options, byDir map[string][]rootfile.Import, dir, out string) engine.Options {
	dirOpts := opts
	dirOpts.External = slices.Clone(opts.External)
	dirOpts.OtherRoots = slices.Clone(opts.OtherRoots)
	for other, imports := range byDir {
		if other == dir {
			continue
		}
		for _, imp := range imports {
			if _, ok := opts.DataSources[imp.Type]; ok {
				dirOpts.External = append(dirOpts.External, engine.External{Type: imp.Type, ID: imp.ID, Name: imp.Name})
			}
			dirOpts.OtherRoots = append(dirOpts.OtherRoots, engine.RootResource{Type: imp.Type, ID: imp.ID, Name: hclfile.Label(imp.Name), Root: relativePath(out, other)})
		}
	}
	unnamed := func(e engine.External) int {
		if e.Name == "" {
			return 1
		}
		return 0
	}
	slices.SortStableFunc(dirOpts.External, func(a, b engine.External) int {
		return cmp.Or(strings.Compare(a.Type, b.Type), strings.Compare(a.ID, b.ID), cmp.Compare(unnamed(a), unnamed(b)), strings.Compare(a.Name, b.Name))
	})
	dirOpts.External = slices.CompactFunc(dirOpts.External, sameResource)
	slices.SortStableFunc(dirOpts.OtherRoots, func(a, b engine.RootResource) int {
		return cmp.Or(strings.Compare(a.Type, b.Type), strings.Compare(a.ID, b.ID), strings.Compare(a.Root, b.Root), strings.Compare(a.Name, b.Name))
	})
	dirOpts.OtherRoots = slices.Compact(dirOpts.OtherRoots)
	return dirOpts
}
