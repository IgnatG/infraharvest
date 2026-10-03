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

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-exec/tfexec"
)

func TestImportsFile(t *testing.T) {
	got, err := ImportsFile([]Import{
		{Type: "aws_sqs_queue", Name: "tfer--orders", ID: "https://sqs.eu-west-1.amazonaws.com/123/orders"},
		{Type: "aws_s3_bucket", Name: "tfer--logs", ID: `odd"id`},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := `import {
  to = aws_s3_bucket.tfer--logs
  id = "odd\"id"
}

import {
  to = aws_sqs_queue.tfer--orders
  id = "https://sqs.eu-west-1.amazonaws.com/123/orders"
}
`
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestImportsFileRejectsInvalidAddress(t *testing.T) {
	if _, err := ImportsFile([]Import{{Type: "aws_sqs_queue", Name: "has space", ID: "x"}}); err == nil {
		t.Error("want an error for an invalid resource name")
	}
}

func TestProvidersFile(t *testing.T) {
	got, err := ProvidersFile(Provider{
		Name:    "azurerm",
		Source:  "hashicorp/azurerm",
		Version: "~> 4.0",
		Config: map[string]interface{}{
			"subscription_id":            "sub",
			"features":                   map[string]interface{}{},
			"skip_provider_registration": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := `terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}

provider "azurerm" {
  features {
  }
  skip_provider_registration = true
  subscription_id            = "sub"
}
`
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestProvidersFileRejectsUnsupportedValue(t *testing.T) {
	_, err := ProvidersFile(Provider{Name: "aws", Source: "hashicorp/aws", Config: map[string]interface{}{"region": struct{}{}}})
	if err == nil || !strings.Contains(err.Error(), "region") {
		t.Errorf("want an error naming the argument, got %v", err)
	}
}

// fakeTerraform records calls and writes generated.tf like Terraform does.
type fakeTerraform struct {
	dir     string
	planErr error
	calls   []string
}

func (f *fakeTerraform) Init(context.Context, ...tfexec.InitOption) error {
	f.calls = append(f.calls, "init")
	return nil
}

func (f *fakeTerraform) Plan(context.Context, ...tfexec.PlanOption) (bool, error) {
	f.calls = append(f.calls, "plan")
	if f.planErr != nil {
		return false, f.planErr
	}
	return true, os.WriteFile(filepath.Join(f.dir, GeneratedFileName), []byte("# generated\n"), 0o644)
}

func TestGenerate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "aws", "sqs")
	tf := &fakeTerraform{dir: dir}

	err := Generate(context.Background(), tf, dir, []byte("# providers\n"), []Import{{Type: "aws_sqs_queue", Name: "tfer--a", ID: "a"}})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Join(tf.calls, ",") != "init,plan" {
		t.Errorf("calls: got %v, want [init plan]", tf.calls)
	}
	for _, name := range []string{ProvidersFileName, ImportsFileName, GeneratedFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s not written: %v", name, err)
		}
	}
}

// Output directories such as generated/aws/sqs don't exist before the
// first import into them.
func TestNewTerraformCreatesDirs(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "generated", "aws", "sqs")
	cache := filepath.Join(base, "cache", "plugins")

	if _, err := NewTerraform(dir, os.Args[0], cache); err != nil {
		t.Fatal(err)
	}

	for _, d := range []string{dir, cache} {
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			t.Errorf("%s not created: %v", d, err)
		}
	}
}

func TestGenerateRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, GeneratedFileName), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	tf := &fakeTerraform{dir: dir}

	err := Generate(context.Background(), tf, dir, nil, []Import{{Type: "aws_sqs_queue", Name: "tfer--a", ID: "a"}})

	if err == nil || len(tf.calls) != 0 {
		t.Errorf("want an error before running Terraform, got err=%v calls=%v", err, tf.calls)
	}
}

func TestGenerateReportsPlanErrors(t *testing.T) {
	dir := t.TempDir()
	tf := &fakeTerraform{dir: dir, planErr: errors.New("Cannot import non-existent remote object")}

	err := Generate(context.Background(), tf, dir, nil, []Import{{Type: "aws_sqs_queue", Name: "tfer--a", ID: "a"}})

	if err == nil || !strings.Contains(err.Error(), "terraform plan: Cannot import") {
		t.Errorf("want the plan error, got %v", err)
	}
}

func TestFindTerraform(t *testing.T) {
	versions := map[string]string{}
	versionOf := func(_ context.Context, path string) (*version.Version, error) {
		v, ok := versions[path]
		if !ok {
			return nil, errors.New("not a terraform binary")
		}
		return version.NewVersion(v)
	}
	installed := ""
	install := func(_ context.Context, dir string) (string, error) {
		if _, err := os.Stat(dir); err != nil {
			return "", err // findTerraform must create the cache directory first
		}
		installed = filepath.Join(dir, binaryName())
		return installed, nil
	}
	t.Setenv("PATH", t.TempDir()) // no terraform on PATH

	t.Run("explicit path too old", func(t *testing.T) {
		versions["/old/terraform"] = "1.4.6"
		if _, err := findTerraform(context.Background(), "/old/terraform", t.TempDir(), versionOf, install); err == nil {
			t.Error("want an error for Terraform older than 1.5")
		}
	})

	t.Run("cached binary is reused", func(t *testing.T) {
		cache := t.TempDir()
		cached := filepath.Join(cache, binaryName())
		if err := os.WriteFile(cached, nil, 0o755); err != nil {
			t.Fatal(err)
		}
		versions[cached] = "1.16.5"
		installed = ""

		got, err := findTerraform(context.Background(), "", cache, versionOf, install)

		if err != nil || got != cached || installed != "" {
			t.Errorf("got %q, %v (installed %q); want the cached binary without installing", got, err, installed)
		}
	})

	t.Run("installs when nothing qualifies", func(t *testing.T) {
		cache := filepath.Join(t.TempDir(), "not-yet-created")

		got, err := findTerraform(context.Background(), "", cache, versionOf, install)

		if err != nil || got != filepath.Join(cache, binaryName()) {
			t.Errorf("got %q, %v; want a fresh install into the cache", got, err)
		}
	})
}
