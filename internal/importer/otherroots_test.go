// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"reflect"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// Each call of a scope sees the roots of every other call, before and
// after it: a regional root refers to the global root's role, and the
// global root to the regional function.
func TestScopeRootsGoBothWays(t *testing.T) {
	role := engine.Import{Type: "aws_iam_role", ID: "app", Name: "app"}
	fn := engine.Import{Type: "aws_lambda_function", ID: "fn", Name: "fn"}
	byDirs := []map[string][]engine.Import{
		{"out/aws/111/global": {role}},
		nil, // a call that generates nothing
		{"out/aws/111/eu-west-2": {fn}},
	}
	want := map[string][]engine.Import{"out/aws/111/global": {role}, "out/aws/111/eu-west-2": {fn}}
	for _, own := range []int{0, 2} {
		if got := scopeRoots(byDirs, own); !reflect.DeepEqual(got, want) {
			t.Errorf("call %d: got %v, want %v", own, got, want)
		}
	}

	regional := withOtherRoots(engine.Options{}, scopeRoots(byDirs, 2), "out/aws/111/eu-west-2", "out")
	if want := []engine.RootResource{{Type: "aws_iam_role", ID: "app", Name: "app", Root: "aws/111/global"}}; !reflect.DeepEqual(regional.OtherRoots, want) {
		t.Errorf("regional root: got %v, want %v", regional.OtherRoots, want)
	}
	global := withOtherRoots(engine.Options{}, scopeRoots(byDirs, 0), "out/aws/111/global", "out")
	if want := []engine.RootResource{{Type: "aws_lambda_function", ID: "fn", Name: "fn", Root: "aws/111/eu-west-2"}}; !reflect.DeepEqual(global.OtherRoots, want) {
		t.Errorf("global root: got %v, want %v", global.OtherRoots, want)
	}
	if want := []engine.External{{Type: "aws_lambda_function", ID: "fn", Name: "fn"}}; !reflect.DeepEqual(global.External, want) {
		t.Errorf("global root's external: got %v, want %v", global.External, want)
	}

	// A call's own directory is its own, whatever another call puts
	// there; a call on its own sees only its roots.
	same := []map[string][]engine.Import{{"out/a": {role}}, {"out/a": {fn}}}
	if got := scopeRoots(same, 1)["out/a"]; !reflect.DeepEqual(got, []engine.Import{fn}) {
		t.Errorf("the call's own directory: got %v", got)
	}
	if got := scopeRoots(byDirs[:1], 0); !reflect.DeepEqual(got, byDirs[0]) {
		t.Errorf("one call: got %v", got)
	}
}
