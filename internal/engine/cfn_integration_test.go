// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package engine

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/pkg/cfn"
)

// guessedNameErrors are the validation errors of a name the converter
// derived from CloudFormation's and reported as a guess: the user is told
// to check those, so they are logged, not failed. Any other error, such
// as a sensitive count or a value of the wrong type, fails.
var guessedNameErrors = []string{"Unsupported argument", "Unsupported block type", "Invalid resource type", "Missing required argument", "Unsupported attribute"}

// TestConvertedTemplatesValidate converts each template of pkg/cfn's
// golden tests and runs terraform validate on the result, with the latest
// AWS provider, which needs no credentials. It downloads Terraform and the
// provider, so it runs in CI:
//
//	go test -tags integration ./internal/engine/ -run TestConvertedTemplatesValidate
func TestConvertedTemplatesValidate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cache := t.TempDir()
	execPath, err := tfrun.FindTerraform(ctx, "", filepath.Join(cache, "terraform"))
	if err != nil {
		t.Fatal(err)
	}
	tfVersion, err := tfrun.BinaryVersion(ctx, execPath)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := LatestProviderVersion(ctx, http.DefaultClient, "", "hashicorp/aws")
	if err != nil {
		t.Fatal(err)
	}
	p := rootfile.Provider{Name: "aws", Source: "hashicorp/aws", Version: rootfile.ProviderConstraint(latest)}
	templates, err := filepath.Glob(filepath.Join("..", "..", "pkg", "cfn", "testdata", "*.*"))
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, template := range templates {
		if ext := filepath.Ext(template); ext != ".yaml" && ext != ".json" {
			continue
		}
		ran++
		t.Run(filepath.Base(template), func(t *testing.T) {
			src, err := os.ReadFile(template)
			if err != nil {
				t.Fatal(err)
			}
			tmpl, err := cfn.Parse(strings.NewReader(string(src)))
			if err != nil {
				t.Fatal(err)
			}
			res, err := cfn.Convert(tmpl, cfn.Options{StackName: "example"})
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			files := map[string][]byte{rootfile.VersionsFileName: rootfile.VersionsFile(rootfile.RequiredVersion(tfVersion), p)}
			for name, content := range res.Files {
				files[name] = content
			}
			for name, content := range files {
				if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			tf, err := tfrun.New(dir, execPath, filepath.Join(cache, "plugins"), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := tf.Init(ctx); err != nil {
				t.Fatalf("terraform init: %v", err)
			}
			validation, err := tf.Validate(ctx)
			if err != nil {
				t.Fatalf("terraform validate: %v", err)
			}
			for _, d := range tfrun.ErrorDiagnostics(validation.Diagnostics) {
				guessed := false
				for _, summary := range guessedNameErrors {
					guessed = guessed || d.Summary == summary
				}
				if guessed {
					t.Logf("a guessed name: %s", tfrun.FormatDiagnostic(d))
					continue
				}
				t.Errorf("%s", tfrun.FormatDiagnostic(d))
			}
		})
	}
	if ran == 0 {
		t.Fatal("no templates")
	}
}
