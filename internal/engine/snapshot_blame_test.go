// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

func TestBlamedNamesTheResourcesOfTheErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	generated := "resource \"aws_s3_bucket\" \"a\" {\n  bucket = \"a\"\n}\n\nresource \"aws_nat_gateway\" \"main\" {\n  subnet_id = \"s\"\n}\n"
	imports := "import {\n  to = aws_s3_bucket.a\n  id = \"a\"\n}\n\nimport {\n  to = aws_nat_gateway.main\n  id = \"n\"\n}\n"
	for name, src := range map[string]string{rootfile.GeneratedFileName: generated, rootfile.ImportsFileName: imports} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	at := func(line int) tfjson.Diagnostic {
		return tfjson.Diagnostic{Severity: tfjson.DiagnosticSeverityError, Summary: "secondary_private_ip_address_count is not supported",
			Range: &tfjson.Range{Filename: rootfile.GeneratedFileName, Start: tfjson.Pos{Line: line}}}
	}
	if got := blamed(dir, []tfjson.Diagnostic{at(6)}); !slices.Equal(got, []string{"aws_nat_gateway.main"}) {
		t.Errorf("an error in a resource: %v", got)
	}
	general := tfjson.Diagnostic{Severity: tfjson.DiagnosticSeverityError, Summary: "Invalid provider configuration"}
	if got := blamed(dir, []tfjson.Diagnostic{at(6), general}); got != nil {
		t.Errorf("with an error about no resource: %v", got)
	}
}
