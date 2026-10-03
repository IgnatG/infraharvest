// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGenerateWithRealTerraform runs the whole engine against real Terraform
// and the random provider, which can import without cloud credentials.
// It downloads Terraform and the provider, so it runs in CI:
//
//	go test -tags integration ./engine/...
func TestGenerateWithRealTerraform(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cache := t.TempDir()
	dir := t.TempDir()

	execPath, err := FindTerraform(ctx, "", filepath.Join(cache, "terraform"))
	if err != nil {
		t.Fatal(err)
	}
	providers, err := ProvidersFile(Provider{Name: "random", Source: "hashicorp/random"})
	if err != nil {
		t.Fatal(err)
	}
	tf, err := NewTerraform(dir, execPath, filepath.Join(cache, "plugins"))
	if err != nil {
		t.Fatal(err)
	}

	result, err := Generate(ctx, tf, dir, providers, []Import{{Type: "random_string", Name: "tfer--example", ID: "s3cr3tvalue"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Secrets) != 0 || len(result.Rejected) != 0 {
		t.Errorf("want nothing left to do, got %+v", result)
	}

	generated, err := os.ReadFile(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), `resource "random_string" "tfer--example"`) {
		t.Errorf("generated.tf has no random_string resource:\n%s", generated)
	}
	if _, err := os.Stat(filepath.Join(dir, "terraform.tfstate")); err == nil {
		t.Error("the engine must not write state")
	}
}
