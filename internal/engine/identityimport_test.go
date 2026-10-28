// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// A resource with an identity, as Terraform's list resources return it, is
// imported by it rather than by an import ID, and the engine reads the
// block back as the importer keys the resource: by the identity's ID.
func TestImportsFileByIdentity(t *testing.T) {
	t.Parallel()
	identity := map[string]string{"region": "eu-west-2", "name": "/app/logs", "account_id": "123456789012"}
	const id = "account_id=123456789012,name=/app/logs,region=eu-west-2"
	got, err := ImportsFile([]Import{
		{Type: "aws_cloudwatch_log_group", Name: "logs", ID: id, Identity: identity},
		{Type: "aws_sqs_queue", Name: "orders", ID: "https://sqs.eu-west-2.amazonaws.com/123/orders"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `import {
  to = aws_cloudwatch_log_group.logs
  identity = {
    account_id = "123456789012"
    name       = "/app/logs"
    region     = "eu-west-2"
  }
}

import {
  to = aws_sqs_queue.orders
  id = "https://sqs.eu-west-2.amazonaws.com/123/orders"
}
`
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}

	dir := t.TempDir()
	writeConfig(t, dir, ImportsFileName, string(got))
	f, err := hclfile.Load(filepath.Join(dir, ImportsFileName))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, imp := range f.Imports() {
		ids[imp.To] = imp.ID
	}
	if ids["aws_cloudwatch_log_group.logs"] != id || ids["aws_sqs_queue.orders"] != "https://sqs.eu-west-2.amazonaws.com/123/orders" {
		t.Errorf("Imports() read %v", ids)
	}
	if got := importTargetsIn(f)["aws_cloudwatch_log_group\x00"+id]; got != "aws_cloudwatch_log_group.logs" {
		t.Errorf("importTargetsIn: got %q", got)
	}
}

// An identity that isn't all literals, or is empty, has no ID the engine
// can tell its resource by.
func TestImportIDOfIdentity(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		block string
		want  string
		ok    bool
	}{
		"numbers and bools": {"import {\n  to = a.b\n  identity = {\n    port = 443\n    global = true\n    zone = null\n  }\n}\n", "global=true,port=443", true},
		"a variable":        {"import {\n  to = a.b\n  identity = {\n    name = var.name\n  }\n}\n", "", false},
		"empty":             {"import {\n  to = a.b\n  identity = {}\n}\n", "", false},
		"neither":           {"import {\n  to = a.b\n}\n", "", false},
	} {
		dir := t.TempDir()
		writeConfig(t, dir, ImportsFileName, tt.block)
		f, err := hclfile.Load(filepath.Join(dir, ImportsFileName))
		if err != nil {
			t.Fatal(err)
		}
		got, ok := hclfile.ImportIDOf(f.Syntax.Blocks[0].Body)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: got %q, %v; want %q, %v", name, got, ok, tt.want, tt.ok)
		}
	}
}

// addLogGroup adds to the root r, written by writeRootDir, a log group
// called name, imported by its identity, or by its ID if byID.
func addLogGroup(t *testing.T, r MergeRoot, name string, byID bool) {
	t.Helper()
	group := "/shop/" + r.Tokens[0] + "/" + name
	importBlock := "\nimport {\n  to = aws_cloudwatch_log_group." + name + "\n  identity = {\n    account_id = \"" + r.Scope + "\"\n    name       = \"" + group + "\"\n    region     = \"eu-west-2\"\n  }\n}\n"
	if byID {
		importBlock = "\nimport {\n  to = aws_cloudwatch_log_group." + name + "\n  id = \"" + group + "\"\n}\n"
	}
	for file, content := range map[string]string{
		"logs.tf":       "\nresource \"aws_cloudwatch_log_group\" \"" + name + "\" {\n  name = \"" + group + "\"\n}\n",
		ImportsFileName: importBlock,
	} {
		f, err := os.OpenFile(filepath.Join(r.Dir, file), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString(content)
		if err := errors.Join(err, f.Close()); err != nil {
			t.Fatal(err)
		}
	}
}

// Roots that import resources by identity merge as those that import
// them by ID: the merged root's import blocks take each environment's
// identities from its imports.tfvars, and each environment's plan
// imports them.
func TestMergeRootsByIdentity(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "aws", "eu-west-2")
	var overrides []string
	roots := mergeRoots(t, dir, nil, &overrides)
	addLogGroup(t, roots[0], "app", false)
	addLogGroup(t, roots[1], "app", false)
	addLogGroup(t, roots[1], "audit", false)
	result, err := MergeRoots(context.Background(), dir, roots, MergeOptions{Profile: mergeProfiles["aws"]})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != "" || !result.Passed() {
		t.Fatalf("not merged: %s %+v", result.Reason, result.Environments)
	}
	imports := readFile(t, dir, ImportsFileName)
	for _, want := range []string{
		"to       = aws_cloudwatch_log_group.app\n  identity = var.import_identities[\"aws_cloudwatch_log_group.app\"]",
		"identity = var.import_identities[\"aws_cloudwatch_log_group.audit[0]\"]",
		"id = var.import_ids[\"aws_sqs_queue.orders\"]",
	} {
		if !strings.Contains(imports, want) {
			t.Errorf("imports.tf misses %q:\n%s", want, imports)
		}
	}
	if variables := readFile(t, dir, hclfile.VariablesFileName); !strings.Contains(variables, "variable \"import_identities\" {") || !strings.Contains(variables, "map(map(string))") {
		t.Errorf("variables.tf doesn't declare import_identities:\n%s", variables)
	}
	prod := readFile(t, dir, "vars/prod/imports.tfvars")
	for _, want := range []string{"\"aws_cloudwatch_log_group.app\" = {\n    account_id = \"222222222222\"\n    name       = \"/shop/prd/app\"", "\"aws_cloudwatch_log_group.audit[0]\" = {", "\"aws_sqs_queue.orders\""} {
		if !strings.Contains(prod, want) {
			t.Errorf("prod's imports.tfvars misses %q:\n%s", want, prod)
		}
	}
	if dev := readFile(t, dir, "vars/dev/imports.tfvars"); strings.Contains(dev, "audit") || !strings.Contains(dev, "/shop/dev/app") {
		t.Errorf("dev's imports.tfvars:\n%s", dev)
	}
	const id = "account_id=222222222222,name=/shop/prd/app,region=eu-west-2"
	if got := result.Environments[1].Imports["aws_cloudwatch_log_group.app"]; got != id {
		t.Errorf("prod's import of the log group: %q", got)
	}
	ids, err := hclfile.MergedImportIDs(dir, "prod")
	if err != nil || ids["aws_cloudwatch_log_group.app"] != id || ids["aws_sqs_queue.orders"] == "" {
		t.Errorf("MergedImportIDs: %v %v", ids, err)
	}
}

// One import block can't import a resource by ID in one environment and
// by identity in another.
func TestMergeRootsByIDAndIdentity(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "aws", "eu-west-2")
	var overrides []string
	roots := mergeRoots(t, dir, nil, &overrides)
	addLogGroup(t, roots[0], "app", true)
	addLogGroup(t, roots[1], "app", false)
	result, err := MergeRoots(context.Background(), dir, roots, MergeOptions{Profile: mergeProfiles["aws"]})
	if err != nil || !strings.Contains(result.Reason, "by ID and others by identity") {
		t.Errorf("want a reason not to merge, got %v %+v", err, result)
	}
}

// A module call's adapter adds the imports of resources the root has no
// block for, such as the VPC module's default routes, from the import of
// the resource each starts from: by identity where that resource is
// imported by its identity.
func TestRetargetImportsByIdentity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, ImportsFileName, `import {
  to = aws_route_table.public
  identity = {
    account_id = "123456789012"
    id         = "rtb-1"
  }
}

import {
  to = aws_route_table.private
  id = "rtb-2"
}
`)
	route := map[string]string{"account_id": "{account_id}", "region": "{region}", "route_table_id": "{id}", "destination_cidr_block": "0.0.0.0/0"}
	err := retargetImports(dir, map[string]string{"aws_route_table.public": "module.vpc.aws_route_table.public[0]"}, []moduleImport{
		{Import: adapters.Import{To: "aws_route.public_internet_gateway[0]", Of: "aws_route_table.public", Suffix: "_0.0.0.0/0", Identity: route}, to: "module.vpc.aws_route.public_internet_gateway[0]"},
		{Import: adapters.Import{To: "aws_route.private_nat_gateway[0]", Of: "aws_route_table.private", Suffix: "_0.0.0.0/0", Identity: route}, to: "module.vpc.aws_route.private_nat_gateway[0]"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := readFile(t, dir, ImportsFileName)
	for _, want := range []string{
		"to = module.vpc.aws_route_table.public[0]",
		"to = module.vpc.aws_route.public_internet_gateway[0]\n  identity = {\n    account_id             = \"123456789012\"\n    destination_cidr_block = \"0.0.0.0/0\"\n    route_table_id         = \"rtb-1\"\n  }\n",
		"to = module.vpc.aws_route.private_nat_gateway[0]\n  id = \"rtb-2_0.0.0.0/0\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("imports.tf misses %q:\n%s", want, got)
		}
	}

	// An adapter that makes no identity can't add the import.
	writeConfig(t, dir, ImportsFileName, "import {\n  to = aws_route_table.public\n  identity = {\n    id = \"rtb-1\"\n  }\n}\n")
	err = retargetImports(dir, nil, []moduleImport{{Import: adapters.Import{To: "aws_route.this[0]", Of: "aws_route_table.public", Suffix: "_0.0.0.0/0"}, to: "module.vpc.aws_route.this[0]"}})
	if err == nil || !strings.Contains(err.Error(), "makes no identity") {
		t.Errorf("want an error, got %v", err)
	}
}
