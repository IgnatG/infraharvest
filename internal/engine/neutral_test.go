// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// providerPrefixes start the names of the resource types and data sources
// of the providers infraharvest imports with.
var providerPrefixes = []string{"aws_", "azurerm_", "azapi_", "azuread_", "google_"}

// cloudBackendNames are the types of Terraform's backends that keep state
// in a cloud.
var cloudBackendNames = []string{"s3", "gcs", "azurerm", "oss", "cos"}

// The engine runs Terraform for any provider and tells none apart by
// name: what it knows of one comes from the catalogue or the provider's
// profile. So none of its code, in this package or below, has a string
// that names a provider's type or a cloud's backend.
func TestEngineNamesNoProvider(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && d.Name() == "testdata":
			return filepath.SkipDir
		case d.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go"):
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Errorf("%s: %v", fset.Position(lit.Pos()), err)
				return true
			}
			if slices.ContainsFunc(providerPrefixes, func(prefix string) bool { return strings.HasPrefix(value, prefix) }) || slices.Contains(cloudBackendNames, value) {
				t.Errorf("%s: %s names a provider's type or a cloud's backend", fset.Position(lit.Pos()), lit.Value)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
