// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// A resource with an identity, as Terraform's list resources return it, is
// imported by it rather than by an import ID, and the engine reads the
// block back as the importer keys the resource: by the identity's ID.
func TestImportsFileByIdentity(t *testing.T) {
	t.Parallel()
	identity := map[string]string{"region": "eu-west-2", "name": "/app/logs", "account_id": "123456789012"}
	const id = "account_id=123456789012,name=/app/logs,region=eu-west-2"
	got, err := rootfile.ImportsFile([]rootfile.Import{
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
	writeConfig(t, dir, rootfile.ImportsFileName, string(got))
	f, err := hclfile.Load(filepath.Join(dir, rootfile.ImportsFileName))
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
		writeConfig(t, dir, rootfile.ImportsFileName, tt.block)
		f, err := hclfile.Load(filepath.Join(dir, rootfile.ImportsFileName))
		if err != nil {
			t.Fatal(err)
		}
		got, ok := hclfile.ImportIDOf(f.Syntax.Blocks[0].Body)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: got %q, %v; want %q, %v", name, got, ok, tt.want, tt.ok)
		}
	}
}

// A module call's adapter adds the imports of resources the root has no
// block for, such as the VPC module's default routes, from the import of
// the resource each starts from: by identity where that resource is
// imported by its identity.
func TestRetargetImportsByIdentity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.ImportsFileName, `import {
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
	got := readFile(t, dir, rootfile.ImportsFileName)
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
	writeConfig(t, dir, rootfile.ImportsFileName, "import {\n  to = aws_route_table.public\n  identity = {\n    id = \"rtb-1\"\n  }\n}\n")
	err = retargetImports(dir, nil, []moduleImport{{Import: adapters.Import{To: "aws_route.this[0]", Of: "aws_route_table.public", Suffix: "_0.0.0.0/0"}, to: "module.vpc.aws_route.this[0]"}})
	if err == nil || !strings.Contains(err.Error(), "makes no identity") {
		t.Errorf("want an error, got %v", err)
	}
}
