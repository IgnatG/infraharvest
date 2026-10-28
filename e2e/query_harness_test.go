// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
	"github.com/IgnatG/infraharvest/internal/tfquery"
)

// queryRoundTrip is a resource the test created of a type no lister
// lists, for the query service to list through Terraform's list resources
// and import by its identity (see checkQueryRoundTrip).
type queryRoundTrip struct {
	// command is the provider's command: aws, azure or google.
	command string
	// scope are the flags that say where to list, such as --regions, and
	// as whom.
	scope []string
	// typ is the type --query-types names, and identity the attributes
	// of the resource's identity the test knows: the identity Terraform
	// lists may have more, such as the account.
	typ      string
	identity map[string]string
	// file is the file of the root the resource's block goes in, its
	// type family's.
	file string
	// plugins is the plugin cache of the test's plans, where Terraform is
	// downloaded to as well when it isn't on the PATH.
	plugins string
}

// checkQueryRoundTrip takes q's resource through the query service:
// --engine=tofu is refused, as OpenTofu has no terraform query; discover
// lists the resource under the query service, included, by an ID of its
// identity's attributes; and the import of that ID alone (--id) writes an
// import block by identity, without an ID, and the resource's block in
// its type family's file, passes every check, and plans as one import
// with no changes. It lists and imports with Terraform, whatever engine
// the test runs with: the query service always does.
func checkQueryRoundTrip(ctx context.Context, t *testing.T, q queryRoundTrip) {
	t.Helper()
	query := []string{"--services=" + harvest.QueryService, "--query-types=" + q.typ}

	root := cli.NewCmdRoot()
	root.SetArgs(slices.Concat([]string{"import", q.command, "--engine=tofu", "--out=" + t.TempDir(), "--all"}, query, q.scope))
	if err := root.ExecuteContext(ctx); err == nil || !strings.Contains(err.Error(), "OpenTofu doesn't have") {
		t.Errorf("import %s --engine=tofu with the %s service: %v, want it refused", q.command, harvest.QueryService, err)
	}

	execPath, err := tfquery.Binary().Find(ctx, "", filepath.Join(q.plugins, "terraform"))
	if err != nil {
		t.Fatal(err)
	}

	selectionFile := filepath.Join(t.TempDir(), "selection.yaml")
	root = cli.NewCmdRoot()
	// --out is where discover saves its inventory.
	root.SetArgs(slices.Concat([]string{"discover", q.command, "--pick=false", "--terraform-path=" + execPath, "--out=" + t.TempDir(), "--selection=" + selectionFile}, query, q.scope))
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("discover %s with the %s service: %v", q.command, harvest.QueryService, err)
	}
	f, err := selection.Load(selectionFile)
	if err != nil {
		t.Fatal(err)
	}
	id := ""
	for _, r := range f.Resources {
		if r.Type != q.typ || !identityHas(identityOfID(r.ID), q.identity) {
			continue
		}
		if r.Service != harvest.QueryService || !r.Include {
			t.Errorf("discover lists %s %s under the service %q, include %v (%s); want it under %s, included", r.Type, r.ID, r.Service, r.Include, r.Reason, harvest.QueryService)
		}
		id = r.ID
	}
	if id == "" {
		t.Fatalf("discover doesn't list the %s with %v; it lists %+v", q.typ, q.identity, f.Resources)
	}

	// The resource alone: a real account may have others of the type.
	out := t.TempDir()
	_, idType, _ := strings.Cut(q.typ, "_")
	root = cli.NewCmdRoot()
	root.SetArgs(slices.Concat([]string{"import", q.command, "--engine=terraform", "--terraform-path=" + execPath, "--out=" + out, "--all", "--id=" + idType + "=" + id}, query, q.scope))
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("import %s with the %s service: %v", q.command, harvest.QueryService, err)
	}
	var coverage report.Report
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(out, report.Dir, "coverage.json"))), &coverage); err != nil {
		t.Fatal(err)
	}
	if coverage.Totals.Imported != 1 || coverage.Totals.LeftOut != 0 || coverage.Totals.Failed != 0 || len(coverage.Failures) != 0 {
		t.Errorf("coverage.json: %+v, failures %v; want the one %s imported", coverage.Totals, coverage.Failures, q.typ)
	}
	for _, d := range coverage.Directories {
		for _, c := range d.Checks {
			if !c.Passed {
				t.Errorf("%s: %s failed: %v", d.Path, c.Name, c.Details)
			}
		}
	}
	dirs := generatedDirs(t, out)
	if len(dirs) != 1 {
		t.Fatalf("roots %v, want one", dirs)
	}
	dir := dirs[0]
	checkIdentityImport(t, dir, q)
	files := map[string]bool{}
	for _, path := range configPaths(t, dir) {
		parsed, diags := hclsyntax.ParseConfig([]byte(readFile(t, path)), path, hcl.InitialPos)
		if diags.HasErrors() {
			t.Fatal(diags)
		}
		for _, b := range parsed.Body.(*hclsyntax.Body).Blocks {
			if b.Type == "resource" && len(b.Labels) == 2 && b.Labels[0] == q.typ {
				files[filepath.Base(path)] = true
			}
		}
	}
	if want := map[string]bool{q.file: true}; !maps.Equal(files, want) {
		t.Errorf("%s: the %s block is in %v, want %s, its type family's file", dir, q.typ, slices.Sorted(maps.Keys(files)), q.file)
	}
	if imported := checkNoChanges(ctx, t, dir, execPath, q.plugins, nil); imported[q.typ] != 1 || len(imported) != 1 {
		t.Errorf("%s: the plan imports %v, want the one %s", dir, imported, q.typ)
	}
}

// checkIdentityImport checks that imports.tf of the root in dir imports
// q's resource, and nothing else, by its identity: an import block to a
// resource of q's type with an identity that has q's attributes, and no
// id.
func checkIdentityImport(t *testing.T, dir string, q queryRoundTrip) {
	t.Helper()
	path := filepath.Join(dir, engine.ImportsFileName)
	parsed, diags := hclsyntax.ParseConfig([]byte(readFile(t, path)), path, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	var imports []*hclsyntax.Block
	for _, b := range parsed.Body.(*hclsyntax.Body).Blocks {
		if b.Type == "import" {
			imports = append(imports, b)
		}
	}
	if len(imports) != 1 {
		t.Fatalf("%s: %d import blocks, want one:\n%s", path, len(imports), readFile(t, path))
	}
	attrs := imports[0].Body.Attributes
	var to *hclsyntax.ScopeTraversalExpr
	if attr := attrs["to"]; attr != nil {
		to, _ = attr.Expr.(*hclsyntax.ScopeTraversalExpr)
	}
	if to == nil || to.Traversal.RootName() != q.typ {
		t.Errorf("%s: the import isn't to a %s:\n%s", path, q.typ, readFile(t, path))
	}
	if _, ok := attrs["id"]; ok {
		t.Errorf("%s: the import has an id, want its identity alone:\n%s", path, readFile(t, path))
	}
	identity := map[string]string{}
	if attr := attrs["identity"]; attr != nil {
		value, diags := attr.Expr.Value(nil)
		if diags.HasErrors() || !value.CanIterateElements() {
			t.Fatalf("%s: identity: %v", path, diags)
		}
		for k, v := range value.AsValueMap() {
			if v.Type() == cty.String {
				identity[k] = v.AsString()
			}
		}
	}
	if !identityHas(identity, q.identity) {
		t.Errorf("%s: the import's identity is %v, want one with %v:\n%s", path, identity, q.identity, readFile(t, path))
	}
}

// identityOfID returns the attributes of an identity the query service
// recorded as an ID, <key>=<value> separated by commas (see
// harvest.RecordIdentity).
func identityOfID(id string) map[string]string {
	identity := map[string]string{}
	for part := range strings.SplitSeq(id, ",") {
		if k, v, ok := strings.Cut(part, "="); ok {
			identity[k] = v
		}
	}
	return identity
}

// identityHas reports whether identity has every attribute of want, with
// its value, but for case: Azure's IDs keep the case they were created
// with, or not.
func identityHas(identity, want map[string]string) bool {
	for k, v := range want {
		if !strings.EqualFold(identity[k], v) {
			return false
		}
	}
	return true
}
