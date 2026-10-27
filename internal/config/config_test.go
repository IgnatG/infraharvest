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
	t.Parallel()
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
	t.Parallel()
	f, err := Load(write(t, "version: 1\nproviders:\n  aws:\n    region: eu-west-2\n"))
	if err != nil {
		t.Fatal(err)
	}
	fs, _, _, _, _ := flags()
	if err := f.Apply(fs, "aws"); err == nil || !strings.Contains(err.Error(), `"region"`) {
		t.Errorf("want an error naming the setting, got %v", err)
	}
}

// check and config are flags of the command line alone: check in a file
// would make every run that reads it import nothing.
func TestApplyRejectsCommandLineFlags(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		"version: 1\nsettings:\n  check: true\n",
		"version: 1\nproviders:\n  aws:\n    check: true\n",
		"version: 1\nsettings:\n  config: other.yaml\n",
	} {
		f, err := Load(write(t, content))
		if err != nil {
			t.Fatal(err)
		}
		fs, _, _, _, _ := flags()
		fs.Bool("check", false, "")
		fs.String("config", "", "")
		if err := f.Apply(fs, "aws"); err == nil || !strings.Contains(err.Error(), "for the command line") {
			t.Errorf("%q: got %v, want it rejected", content, err)
		}
		if fs.Changed("check") || fs.Changed("config") {
			t.Errorf("%q: set a flag of the command line", content)
		}
	}
}

// A flag given once per value, such as --id, takes each item of a list as
// one value, commas and all.
func TestApplyRepeatedFlags(t *testing.T) {
	f, err := Load(write(t, "version: 1\nproviders:\n  aws:\n    id: [\"vpc=vpc-1,vpc-2\", subnet=subnet-1]\n    tag: env=prod\n"))
	if err != nil {
		t.Fatal(err)
	}
	fs := pflag.NewFlagSet("aws", pflag.ContinueOnError)
	ids := fs.StringArray("id", nil, "")
	tags := fs.StringArray("tag", nil, "")
	if err := f.Apply(fs, "aws"); err != nil {
		t.Fatal(err)
	}
	if len(*ids) != 2 || (*ids)[0] != "vpc=vpc-1,vpc-2" || (*ids)[1] != "subnet=subnet-1" {
		t.Errorf("id: got %q", *ids)
	}
	if len(*tags) != 1 || (*tags)[0] != "env=prod" {
		t.Errorf("tag: got %q", *tags)
	}
}

func TestLoadRejectsMistakes(t *testing.T) {
	t.Parallel()
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

// Settings for a provider that names no command, such as the Terraform
// provider's azurerm for azure, would never apply: they fail, listing the
// commands.
func TestApplyRejectsUnknownProviders(t *testing.T) {
	t.Parallel()
	f, err := Load(write(t, "version: 1\nproviders:\n  google:\n    projects: [p]\n  azurerm:\n    subscription: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	fs, _, _, _, _ := flags()
	if err := f.Apply(fs, "aws"); err == nil || !strings.Contains(err.Error(), `unknown provider "azurerm": use aws, azure, google`) {
		t.Errorf("want an error listing the valid ones, got %v", err)
	}
	// The command being run is always one.
	f, err = Load(write(t, "version: 1\nproviders:\n  fake:\n    profile: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Apply(fs, "fake"); err != nil {
		t.Errorf("the running command's settings: %v", err)
	}
}

func TestBackendFile(t *testing.T) {
	t.Parallel()
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

// A root's state is where its backend.tf keeps it, named as
// --managed-state names state objects.
func TestBackendStateURL(t *testing.T) {
	for _, tc := range []struct {
		backend Backend
		want    string
	}{
		{Backend{S3: &S3{Bucket: "acme-state", Region: "eu-west-2", KeyPrefix: "imported"}}, "s3://acme-state/imported/aws/1/eu-west-2/terraform.tfstate"},
		{Backend{AzureRM: &AzureRM{StorageAccountName: "acmestate", ContainerName: "tfstate"}}, "https://acmestate.blob.core.windows.net/tfstate/aws/1/eu-west-2/terraform.tfstate"},
		{Backend{GCS: &GCS{Bucket: "acme-state", Prefix: "roots"}}, "gs://acme-state/roots/aws/1/eu-west-2/default.tfstate"},
		{Backend{}, ""},
	} {
		if got := tc.backend.StateURL("aws/1/eu-west-2"); got != tc.want {
			t.Errorf("%+v: got %q, want %q", tc.backend, got, tc.want)
		}
	}
}

func TestLoadModules(t *testing.T) {
	t.Parallel()
	path := write(t, `version: 1
modules:
  - source: git::https://github.com/acme/terraform-modules.git//bucket?ref=v1.4.0
  - source: app.terraform.io/acme/queue/aws
    version: 2.1.0
  - source: ./modules/role
`)
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Modules) != 3 || f.Modules[1].Version != "2.1.0" {
		t.Fatalf("modules: %+v", f.Modules)
	}
	if want := filepath.Join(filepath.Dir(path), "modules", "role"); f.Modules[2].Source != want {
		t.Errorf("a directory is relative to the file: got %s, want %s", f.Modules[2].Source, want)
	}
}

func TestLoadRejectsUnpinnedModules(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ module, want string }{
		"git without a ref":            {"source: git::https://github.com/acme/modules.git//bucket", "pin a tag or commit"},
		"git with an empty ref":        {"source: git::https://github.com/acme/modules.git//bucket?ref=", "pin a tag or commit"},
		"git with ref in another name": {"source: git::https://github.com/acme/modules.git//bucket?noref=v1.0.0", "pin a tag or commit"},
		"a registry module unversion":  {"source: app.terraform.io/acme/queue/aws", "needs an exact version"},
		"a version range":              {"source: app.terraform.io/acme/queue/aws\n    version: \"~> 2.1\"", "needs an exact version"},
		"a versioned directory":        {"source: ./modules/role\n    version: 1.0.0", "a directory has no version"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, "version: 1\nmodules:\n  - "+tc.module+"\n"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestGitRef(t *testing.T) {
	t.Parallel()
	for source, want := range map[string]string{
		"git::https://github.com/acme/modules.git//bucket?ref=v1.4.0":   "v1.4.0",
		"git::https://github.com/acme/modules.git?depth=1&ref=main":     "main",
		"git::https://github.com/acme/modules.git?ref=v1.0.0//bucket":   "v1.0.0",
		"git::https://github.com/acme/modules.git//bucket?noref=v1.0.0": "",
		"github.com/acme/modules//bucket":                               "",
	} {
		if got := gitRef(source); got != want {
			t.Errorf("gitRef(%s): got %q, want %q", source, got, want)
		}
	}
	if !versionTag("v1.4.0") || !versionTag("2.1") || versionTag("main") || versionTag("release-2026-10") {
		t.Error("versionTag")
	}
	if !commitSHA.MatchString("0123456789abcdef0123456789abcdef01234567") || commitSHA.MatchString("main") {
		t.Error("commitSHA")
	}
}

// A registry module's source ends with its provider, and only a registry
// module at its version or a git repository at a commit can't change.
func TestModuleProviderAndImmutable(t *testing.T) {
	t.Parallel()
	const commit = "0123456789abcdef0123456789abcdef01234567"
	for _, c := range []struct {
		module    Module
		provider  string
		immutable bool
	}{
		{Module{Source: "example.com/platform/network/aws", Version: "1.2.0"}, "aws", true},
		{Module{Source: "platform/network/google//modules/subnets", Version: "1.2.0"}, "google", true},
		{Module{Source: "git::https://example.com/modules.git//network?ref=" + commit}, "", true},
		{Module{Source: "git::https://example.com/modules.git//network?ref=v1.2.0"}, "", false},
		{Module{Source: "github.com/example/modules//network?ref=main"}, "", false},
		{Module{Source: "./modules/network"}, "", false},
		{Module{Source: "example.com/network"}, "", false},
	} {
		if got := c.module.RegistryProvider(); got != c.provider {
			t.Errorf("%s: provider %q, want %q", c.module.Source, got, c.provider)
		}
		if got := c.module.Immutable(); got != c.immutable {
			t.Errorf("%s: immutable %v, want %v", c.module.Source, got, c.immutable)
		}
	}
}

// ApplyShared sets the shared settings it is given, as the command line
// didn't, and leaves the file's other settings, such as the import's
// selection, to the provider commands; a provider's setting of one of
// them is an error, as a command such as drift runs for no one provider.
func TestApplyShared(t *testing.T) {
	t.Parallel()
	f, err := Load(write(t, "version: 1\nsettings:\n  engine: tofu\n  catalogue: overlay\n  selection: selection.yaml\nproviders:\n  aws:\n    profile: prod\n"))
	if err != nil {
		t.Fatal(err)
	}
	fs := pflag.NewFlagSet("drift", pflag.ContinueOnError)
	engine := fs.String("engine", "terraform", "")
	catalogue := fs.String("catalogue", "", "")
	if err := fs.Parse([]string{"--catalogue=mine"}); err != nil {
		t.Fatal(err)
	}
	if err := f.ApplyShared(fs, "drift", []string{"engine", "catalogue", "terraform-path"}); err != nil {
		t.Fatal(err)
	}
	if *engine != "tofu" || *catalogue != "mine" {
		t.Errorf("got engine=%s catalogue=%s, want tofu and the command line's", *engine, *catalogue)
	}

	f, err = Load(write(t, "version: 1\nproviders:\n  aws:\n    engine: tofu\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.ApplyShared(fs, "drift", []string{"engine"}); err == nil || !strings.Contains(err.Error(), "providers: aws: engine") || !strings.Contains(err.Error(), "under settings") {
		t.Errorf("a provider's engine: %v, want an error saying to set it under settings", err)
	}
}
