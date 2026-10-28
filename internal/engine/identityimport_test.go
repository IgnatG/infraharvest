// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"path/filepath"
	"testing"
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
	f, err := loadHCL(filepath.Join(dir, ImportsFileName))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, imp := range f.imports() {
		ids[imp.to] = imp.id
	}
	if ids["aws_cloudwatch_log_group.logs"] != id || ids["aws_sqs_queue.orders"] != "https://sqs.eu-west-2.amazonaws.com/123/orders" {
		t.Errorf("imports() read %v", ids)
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
		f, err := loadHCL(filepath.Join(dir, ImportsFileName))
		if err != nil {
			t.Fatal(err)
		}
		got, ok := importIDOf(f.syntax.Blocks[0].Body)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: got %q, %v; want %q, %v", name, got, ok, tt.want, tt.ok)
		}
	}
}
