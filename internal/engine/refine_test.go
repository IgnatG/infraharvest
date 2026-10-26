// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

// refining returns a Refine that writes refined.tf and edits providers.tf,
// failing with err if set; it records what it was given, and providers.tf
// as it was.
func refining(got *[]Managed, providers *string, err error) func(context.Context, string, []Managed, *tfjson.ProviderSchemas) ([]string, error) {
	return func(_ context.Context, dir string, addresses []Managed, _ *tfjson.ProviderSchemas) ([]string, error) {
		*got = addresses
		before, rerr := os.ReadFile(filepath.Join(dir, ProvidersFileName))
		if rerr != nil {
			return nil, rerr
		}
		*providers = string(before)
		if werr := os.WriteFile(filepath.Join(dir, "refined.tf"), []byte("# refined\n"), 0o644); werr != nil {
			return nil, werr
		}
		if werr := os.WriteFile(filepath.Join(dir, ProvidersFileName), []byte("# providers, refined\n"), 0o644); werr != nil {
			return nil, werr
		}
		if err != nil {
			return nil, err
		}
		return []string{"refined"}, nil
	}
}

func generateRefined(t *testing.T, tf *fakeTerraform, refine func(context.Context, string, []Managed, *tfjson.ProviderSchemas) ([]string, error)) *Result {
	t.Helper()
	config := map[string][]byte{
		VersionsFileName:  VersionsFile(">= 1.16, < 2.0", Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.14"}),
		ProvidersFileName: []byte("# providers\n"),
	}
	result, err := Generate(context.Background(), tf, tf.dir, []Import{{Type: "aws_sqs_queue", Name: "orders", ID: "a"}}, Options{Config: config, Refine: refine})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// Refine runs once the addresses are final, with the schemas, and the gate
// checks what it wrote.
func TestGenerateRefinesBeforeTheGate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "root")
	tf := &fakeTerraform{dir: dir, schemas: &tfjson.ProviderSchemas{}}
	var got []Managed
	var providers string
	result := generateRefined(t, tf, refining(&got, &providers, nil))
	if len(got) != 1 || got[0].Address != "aws_sqs_queue.orders" {
		t.Errorf("Refine got %+v", got)
	}
	if calls := strings.Join(tf.calls, ","); !strings.Contains(calls, "schema,fmt,validate,plan,show") {
		t.Errorf("calls: %s, want the schema read before the gate", calls)
	}
	if !result.Gate.Passed() || !slices.Contains(result.Warnings, "refined") {
		t.Errorf("gate %+v, warnings %q", result.Gate, result.Warnings)
	}
	if _, err := os.Stat(filepath.Join(dir, "refined.tf")); err != nil {
		t.Errorf("refined.tf: %v", err)
	}
}

// A gate that fails with Refine's edits runs again without them: the root
// passes as it would have, with a warning, and Refine's own warnings go.
func TestGenerateRollsBackRefineThatFailsTheGate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "root")
	tf := &fakeTerraform{dir: dir, validations: []*tfjson.ValidateOutput{{Valid: false}}}
	var got []Managed
	var providers string
	result := generateRefined(t, tf, refining(&got, &providers, nil))
	if !result.Gate.Passed() {
		t.Errorf("the gate without the edits failed: %+v", result.Gate)
	}
	warnings := strings.Join(result.Warnings, "\n")
	if !strings.Contains(warnings, "made its gate fail (G2 validate), so they were rolled back") || slices.Contains(result.Warnings, "refined") {
		t.Errorf("warnings: %q", result.Warnings)
	}
	assertUnrefined(t, dir, providers)
}

// A Refine that fails leaves the root as it was, with a warning.
func TestGenerateRollsBackRefineThatFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "root")
	tf := &fakeTerraform{dir: dir}
	var got []Managed
	var providers string
	result := generateRefined(t, tf, refining(&got, &providers, errors.New("no template")))
	if !result.Gate.Passed() || !strings.Contains(strings.Join(result.Warnings, "\n"), "failed, and were rolled back: no template") {
		t.Errorf("gate %+v, warnings %q", result.Gate, result.Warnings)
	}
	assertUnrefined(t, dir, providers)
}

func assertUnrefined(t *testing.T, dir, providers string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "refined.tf")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("refined.tf is still there: %v", err)
	}
	if got := readFile(t, dir, ProvidersFileName); got != providers {
		t.Errorf("providers.tf: %q, want %q", got, providers)
	}
}
