// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "infraharvest.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const sample = `version: 1
settings:
  engine: terraform
  all: true
providers:
  aws:
    profile: prod
    regions: [eu-west-2, us-east-1]
backend:
  s3:
    bucket: acme-terraform-state
    region: eu-west-2
    key_prefix: imported
`

func flags() (*pflag.FlagSet, *string, *bool, *string, *[]string) {
	fs := pflag.NewFlagSet("aws", pflag.ContinueOnError)
	engine := fs.String("engine", "tofu", "")
	all := fs.Bool("all", false, "")
	profile := fs.String("profile", "default", "")
	regions := fs.StringSlice("regions", nil, "")
	return fs, engine, all, profile, regions
}

func TestApply(t *testing.T) {
	f, err := Load(write(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	fs, engine, all, profile, regions := flags()
	// The command line wins.
	if err := fs.Parse([]string{"--profile=staging"}); err != nil {
		t.Fatal(err)
	}

	if err := f.Apply(fs, "aws"); err != nil {
		t.Fatal(err)
	}

	if *engine != "terraform" || !*all || *profile != "staging" || strings.Join(*regions, ",") != "eu-west-2,us-east-1" {
		t.Errorf("got engine=%s all=%v profile=%s regions=%v", *engine, *all, *profile, *regions)
	}
}

func TestApplyRejectsUnknownSettings(t *testing.T) {
	f, err := Load(write(t, "version: 1\nproviders:\n  aws:\n    region: eu-west-2\n"))
	if err != nil {
		t.Fatal(err)
	}
	fs, _, _, _, _ := flags()
	if err := f.Apply(fs, "aws"); err == nil || !strings.Contains(err.Error(), `"region"`) {
		t.Errorf("want an error naming the setting, got %v", err)
	}
}

func TestLoadRejectsMistakes(t *testing.T) {
	for name, content := range map[string]string{
		"other version":    "version: 2\n",
		"unknown key":      "version: 1\nengine: terraform\n",
		"two backends":     "version: 1\nbackend:\n  s3: { bucket: a, region: b }\n  gcs: { bucket: c }\n",
		"no backend kind":  "version: 1\nbackend: {}\n",
		"missing settings": "version: 1\nbackend:\n  s3: { bucket: a }\n",
	} {
		if _, err := Load(write(t, content)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestBackendFile(t *testing.T) {
	for name, tc := range map[string]struct {
		backend Backend
		want    string
	}{
		"s3": {Backend{S3: &S3{Bucket: "state", Region: "eu-west-2", KeyPrefix: "imported"}}, `terraform {
  backend "s3" {
    bucket       = "state"
    key          = "imported/aws/111122223333/eu-west-2/terraform.tfstate"
    region       = "eu-west-2"
    encrypt      = true
    use_lockfile = true
  }
}
`},
		"azurerm": {Backend{AzureRM: &AzureRM{ResourceGroupName: "rg", StorageAccountName: "sa", ContainerName: "tfstate"}}, `terraform {
  backend "azurerm" {
    resource_group_name  = "rg"
    storage_account_name = "sa"
    container_name       = "tfstate"
    key                  = "aws/111122223333/eu-west-2/terraform.tfstate"
  }
}
`},
		"gcs": {Backend{GCS: &GCS{Bucket: "state", Prefix: "imported"}}, `terraform {
  backend "gcs" {
    bucket = "state"
    prefix = "imported/aws/111122223333/eu-west-2"
  }
}
`},
	} {
		if got := string(tc.backend.File("aws/111122223333/eu-west-2")); got != tc.want {
			t.Errorf("%s: got:\n%s\nwant:\n%s", name, got, tc.want)
		}
	}
}
