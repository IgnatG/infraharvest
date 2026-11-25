// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package pkg_test checks the boundary of pkg/, the public packages other
// modules import: what each pulls in. A package that imports the engine,
// Terraform's execution libraries or a cloud SDK it doesn't name would
// bring them to every importer, which is why the packages are small. The
// linter's depguard rules say the same of the imports in the source; this
// test says it of what a build links, which an import through another
// package would slip past.
package pkg_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/IgnatG/infraharvest"

// forbidden are the modules no package of pkg/ may link, at any depth.
var forbidden = []string{
	"github.com/hashicorp/terraform-exec",
	"github.com/hashicorp/hc-install",
}

// allowedInternal are the packages of internal/ that pkg/ may link: the
// leaves, which import nothing of the engine.
var allowedInternal = []string{
	module + "/internal/catalogue",
	module + "/internal/secretdetect",
	module + "/internal/tftypes",
}

// budgets are the most packages of infraharvest's own (the package itself
// and everything of this module it links) each package of pkg/ may link.
// A package not named here fails the test: say how much it may pull in.
var budgets = map[string]int{
	module + "/pkg/tfstate":               12,
	module + "/pkg/tfstate/stores/azblob": 12,
	module + "/pkg/tfstate/stores/gcs":    12,
	module + "/pkg/tfstate/stores/hcp":    12,
	module + "/pkg/tfstate/stores/s3":     12,
	module + "/pkg/typemap":               6,
	module + "/pkg/secretscan":            4,
}

// goList runs go list with args in this directory, pkg/.
func goList(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "go", append([]string{"list"}, args...)...).Output()
	if err != nil {
		t.Fatalf("go list %s: %v", strings.Join(args, " "), err)
	}
	return strings.Fields(string(out))
}

func TestPackagesLinkNoEngineAndNoTerraformExecution(t *testing.T) {
	t.Parallel()
	packages := goList(t, "-f", "{{if .GoFiles}}{{.ImportPath}}{{end}}", "./...")
	if len(packages) == 0 {
		t.Fatal("no package under pkg/")
	}
	for _, pkg := range packages {
		budget, ok := budgets[pkg]
		if !ok {
			t.Errorf("%s has no budget: add it to budgets, with how many packages of infraharvest's it may link", pkg)
			continue
		}
		deps := goList(t, "-deps", "-f", "{{.ImportPath}}", pkg)
		own := 0
		for _, dep := range deps {
			for _, f := range forbidden {
				if dep == f || strings.HasPrefix(dep, f+"/") {
					t.Errorf("%s links %s, which pkg/ must not", pkg, dep)
				}
			}
			if !strings.HasPrefix(dep, module+"/") {
				continue
			}
			own++
			if strings.HasPrefix(dep, module+"/internal/") && !slices.ContainsFunc(allowedInternal, func(ok string) bool {
				return dep == ok || strings.HasPrefix(dep, ok+"/")
			}) {
				t.Errorf("%s links %s, which pkg/ must not: only %v are allowed", pkg, dep, allowedInternal)
			}
		}
		if own > budget {
			t.Errorf("%s links %d packages of infraharvest's, more than its budget of %d: %v", pkg, own, budget, deps)
		}
	}
}
