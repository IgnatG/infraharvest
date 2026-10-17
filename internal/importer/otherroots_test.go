// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"reflect"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// A later call of an account refers to the roots its earlier calls
// generated, as a region's roots do to the global root; another
// account's roots are left out.
func TestEarlierRootsAreOtherRoots(t *testing.T) {
	run := NewRun()
	role := engine.Import{Type: "aws_iam_role", ID: "app", Name: "app"}
	run.recordRoot("aws/111/global", "out/aws/111/global", []engine.Import{role})
	run.recordRoot("aws/222/global", "out/aws/222/global", []engine.Import{{Type: "aws_iam_role", ID: "other", Name: "other"}})
	run.recordRoot("aws/111/eu-west-1", "out/aws/111/empty", nil)

	byDir := map[string][]engine.Import{"out/aws/111/eu-west-2": {{Type: "aws_lambda_function", ID: "fn", Name: "fn"}}}
	roots := withEarlierRoots(byDir, run.earlierRoots("aws/111/eu-west-2"))
	want := map[string][]engine.Import{
		"out/aws/111/global":    {role},
		"out/aws/111/eu-west-2": byDir["out/aws/111/eu-west-2"],
	}
	if !reflect.DeepEqual(roots, want) {
		t.Errorf("got %v, want %v", roots, want)
	}

	dirOpts := withOtherRoots(engine.Options{}, roots, "out/aws/111/eu-west-2", "out")
	wantRoots := []engine.RootResource{{Type: "aws_iam_role", ID: "app", Name: "app", Root: "aws/111/global"}}
	if !reflect.DeepEqual(dirOpts.OtherRoots, wantRoots) {
		t.Errorf("other roots: got %v, want %v", dirOpts.OtherRoots, wantRoots)
	}
	if want := []engine.External{{Type: "aws_iam_role", ID: "app", Name: "app"}}; !reflect.DeepEqual(dirOpts.External, want) {
		t.Errorf("external: got %v, want %v", dirOpts.External, want)
	}

	// The call's own directory is its own, whatever an earlier call put
	// there; without earlier roots, the call's are as they are.
	again := withEarlierRoots(map[string][]engine.Import{"out/aws/111/global": nil}, run.earlierRoots("aws/111/us-east-1"))
	if got := again["out/aws/111/global"]; got != nil {
		t.Errorf("the call's own directory: got %v", got)
	}
	if got := withEarlierRoots(byDir, run.earlierRoots("aws/333/eu-west-2")); !reflect.DeepEqual(got, byDir) {
		t.Errorf("an account without earlier roots: got %v", got)
	}
}

func TestScopeAccount(t *testing.T) {
	for scope, want := range map[string]string{
		"aws/111/eu-west-2":      "aws/111",
		"aws/111/global":         "aws/111",
		"google/my-project/":     "google/my-project",
		"azure/sub-1/westeurope": "azure/sub-1",
		"":                       "/",
	} {
		if got := scopeAccount(scope); got != want {
			t.Errorf("scopeAccount(%q) = %q, want %q", scope, got, want)
		}
	}
}
