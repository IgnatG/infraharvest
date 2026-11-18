// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package incremental

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/run"
)

func TestRootNames(t *testing.T) {
	t.Parallel()
	files, err := hclfile.ConfigFiles(existingRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	want := run.Names{
		"aws_vpc.main":                        true,
		"module.logs":                         true,
		"data.aws_security_group.sg_0def5678": true,
		"local.tags":                          true,
		"local.vpc_id":                        true,
		"var.aws_ssm_parameter_token_value":   true,
	}
	if got := run.RootNames(files); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestHasConfiguration(t *testing.T) {
	t.Parallel()
	if has, err := HasConfiguration(existingRoot(t)); err != nil || !has {
		t.Errorf("a generated root: got %v, %v", has, err)
	}
	empty := t.TempDir()
	writeConfig(t, empty, rootfile.ProvidersFileName, awsProviders)
	if has, err := HasConfiguration(empty); err != nil || has {
		t.Errorf("a root without resources: got %v, %v", has, err)
	}
	if has, err := HasConfiguration(filepath.Join(empty, "missing")); err != nil || has {
		t.Errorf("no directory: got %v, %v", has, err)
	}
}

func TestExisting(t *testing.T) {
	t.Parallel()
	dir := existingRoot(t)
	previous := &run.Result{
		Imported: []rootfile.Import{
			{Type: "aws_vpc", Name: "main", ID: "vpc-0abc1234"},
			{Type: "aws_s3_bucket", Name: "logs", ID: "logs"}, // moved into module.logs
		},
		Rejected: []run.Rejection{{Address: "aws_ecs_service.web", ID: "apps/web"}},
	}
	got, err := Existing(dir, previous)
	if err != nil {
		t.Fatal(err)
	}
	want := map[run.External]string{
		{Type: "aws_vpc", ID: "vpc-0abc1234"}:     "aws_vpc.main",
		{Type: "aws_s3_bucket", ID: "logs"}:       "",
		{Type: "aws_ecs_service", ID: "apps/web"}: "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// Import blocks alone tell too.
	if got, err := Existing(dir, nil); err != nil || len(got) != 1 || got[run.External{Type: "aws_s3_bucket", ID: "logs"}] != "" {
		t.Errorf("without a checkpoint: got %v, %v", got, err)
	}
}

func TestAppliedTags(t *testing.T) {
	t.Parallel()
	files, err := hclfile.ConfigFiles(existingRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if tags, ok := AppliedTags(files, awsDefaultTags); !ok || !reflect.DeepEqual(tags, map[string]string{"Project": "shop"}) {
		t.Errorf("through a local: got %v, %v", tags, ok)
	}

	literal := t.TempDir()
	writeConfig(t, literal, rootfile.ProvidersFileName, `provider "aws" {
  default_tags {
    tags = { Team = "web" }
  }
}
`)
	files, err = hclfile.ConfigFiles(literal)
	if err != nil {
		t.Fatal(err)
	}
	if tags, ok := AppliedTags(files, awsDefaultTags); !ok || !reflect.DeepEqual(tags, map[string]string{"Team": "web"}) {
		t.Errorf("literal: got %v, %v", tags, ok)
	}

	none := t.TempDir()
	writeConfig(t, none, rootfile.ProvidersFileName, awsProviders)
	files, err = hclfile.ConfigFiles(none)
	if err != nil {
		t.Fatal(err)
	}
	if tags, ok := AppliedTags(files, awsDefaultTags); ok {
		t.Errorf("without default tags: got %v", tags)
	}
}

func TestMerge(t *testing.T) {
	t.Parallel()
	root := existingRoot(t)
	generatedBefore := readFile(t, root, rootfile.GeneratedFileName)
	staging := t.TempDir()
	writeConfig(t, staging, rootfile.GeneratedFileName, `resource "aws_subnet" "a" {
  cidr_block = "10.0.1.0/24"
  vpc_id     = data.aws_vpc.vpc_0abc1234.id
}

resource "aws_instance" "web" {
  subnet_id              = aws_subnet.a.id
  vpc_security_group_ids = [data.aws_security_group.sg_0def5678_2.id, data.aws_security_group.sg_0aaa1111.id]
  ami                    = local.ami
}
`)
	writeConfig(t, staging, rootfile.DataFileName, `data "aws_vpc" "vpc_0abc1234" {
  id = "vpc-0abc1234"
}

data "aws_security_group" "sg_0def5678_2" {
  id = "sg-0def5678"
}

data "aws_security_group" "sg_0aaa1111" {
  id = "sg-0aaa1111"
}
`)
	writeConfig(t, staging, rootfile.ImportsFileName, `import {
  to = aws_subnet.a
  id = "subnet-1"
}

import {
  to = aws_instance.web
  id = "i-1"
}
`)
	writeConfig(t, staging, rootfile.LocalsFileName, `locals {
  ami  = "ami-0123456789abcdef0"
  tags = { Project = "shop" }
}
`)
	writeConfig(t, staging, rootfile.ProvidersFileName, `provider "aws" {
  default_tags {
    tags = local.tags
  }
}
`)
	writeConfig(t, staging, hclfile.VariablesFileName, `variable "aws_instance_web_user_data" {
  type      = string
  sensitive = true
}
`)
	existing := map[run.External]string{{Type: "aws_vpc", ID: "vpc-0abc1234"}: "aws_vpc.main"}

	written, err := mergeInto(t, staging, root, existing, awsDataSources, map[string]string{})
	if err != nil || !slices.Equal(written, []string{AddedFileName(2)}) {
		t.Fatalf("got %q, %v", written, err)
	}

	if got := readFile(t, root, rootfile.GeneratedFileName); got != generatedBefore {
		t.Errorf("generated.tf changed:\n%s", got)
	}
	added := squashed(readFile(t, root, AddedFileName(2)))
	for _, want := range []string{
		"vpc_id = aws_vpc.main.id", // a resource the root has
		"vpc_security_group_ids = [data.aws_security_group.sg_0def5678.id, data.aws_security_group.sg_0aaa1111.id]", // the root's data source, and a new one
		"subnet_id = aws_subnet.a.id",
	} {
		if !strings.Contains(added, want) {
			t.Errorf("%s misses %q:\n%s", AddedFileName(2), want, added)
		}
	}
	data := squashed(readFile(t, root, rootfile.DataFileName))
	if !strings.Contains(data, `data "aws_security_group" "sg_0aaa1111"`) || strings.Count(data, "data ") != 2 {
		t.Errorf("data.tf: want the root's data source and the new one:\n%s", data)
	}
	imports := squashed(readFile(t, root, rootfile.ImportsFileName))
	for _, want := range []string{"to = module.logs.aws_s3_bucket.this[0]", "to = aws_subnet.a", "to = aws_instance.web"} {
		if !strings.Contains(imports, want) {
			t.Errorf("imports.tf misses %q:\n%s", want, imports)
		}
	}
	if variables := readFile(t, root, hclfile.VariablesFileName); strings.Count(variables, "variable ") != 2 {
		t.Errorf("variables.tf: want both variables:\n%s", variables)
	}
	locals := squashed(readFile(t, root, rootfile.LocalsFileName))
	if !strings.Contains(locals, `ami = "ami-0123456789abcdef0"`) || strings.Count(locals, "tags") != 1 || strings.Count(locals, "locals") != 1 {
		t.Errorf("locals.tf: want the new local in the root's block, and the root's tags only:\n%s", locals)
	}

	// The next addition takes the next file.
	writeConfig(t, staging, rootfile.GeneratedFileName, `resource "aws_sqs_queue" "jobs" {
  name = "jobs"
}
`)
	for _, name := range []string{rootfile.DataFileName, rootfile.ImportsFileName, rootfile.LocalsFileName, hclfile.VariablesFileName} {
		if err := os.Remove(filepath.Join(staging, name)); err != nil {
			t.Fatal(err)
		}
	}
	if written, err := mergeInto(t, staging, root, existing, awsDataSources, map[string]string{}); err != nil || !slices.Equal(written, []string{AddedFileName(3)}) {
		t.Fatalf("got %q, %v", written, err)
	}
	if got := readFile(t, root, AddedFileName(3)); !strings.Contains(got, `resource "aws_sqs_queue" "jobs"`) {
		t.Errorf("%s:\n%s", AddedFileName(3), got)
	}
}

// mergeInto merges staging into the root in dir through its workspace (see
// merge), and writes the workspace back to dir.
func mergeInto(t *testing.T, staging, dir string, existing map[run.External]string, sources map[string]run.DataSource, fileOf map[string]string) ([]string, error) {
	t.Helper()
	ws := rootWorkspace(t, dir)
	written, err := merge(staging, ws, existing, sources, fileOf)
	flushed(t, ws)
	return written, err
}

// What an incremental import adds goes after the blocks of the file of
// its type, which keeps the root's blocks as they were, or into a new
// file of that name; only what names no file gets one of its own.
func TestMergeIntoTheFilesOfTheTypes(t *testing.T) {
	t.Parallel()
	root := existingRoot(t)
	network := "resource \"aws_vpc\" \"main\" {\n  cidr_block = \"10.0.0.0/16\"\n}\n"
	writeConfig(t, root, "network.tf", network)
	staging := t.TempDir()
	writeConfig(t, staging, rootfile.GeneratedFileName, `resource "aws_instance" "web" {
  ami = "ami-1"
}

resource "aws_sqs_queue" "jobs" {
  name = "jobs"
}

resource "aws_subnet" "a" {
  cidr_block = "10.0.1.0/24"
}
`)
	fileOf := map[string]string{"aws_subnet.a": "network.tf", "aws_sqs_queue.jobs": "sqs.tf"}

	written, err := mergeInto(t, staging, root, nil, awsDataSources, fileOf)
	if err != nil || !slices.Equal(written, []string{AddedFileName(2), "network.tf", "sqs.tf"}) {
		t.Fatalf("got %q, %v", written, err)
	}
	if got := readFile(t, root, "network.tf"); !strings.HasPrefix(got, network) || !strings.Contains(got, `resource "aws_subnet" "a"`) {
		t.Errorf("network.tf:\n%s", got)
	}
	if got := readFile(t, root, "sqs.tf"); !strings.HasPrefix(got, `resource "aws_sqs_queue" "jobs"`) {
		t.Errorf("sqs.tf:\n%s", got)
	}
	if got := readFile(t, root, AddedFileName(2)); !strings.Contains(got, `resource "aws_instance" "web"`) || strings.Contains(got, "aws_subnet") {
		t.Errorf("%s:\n%s", AddedFileName(2), got)
	}

	files := (&run.Result{Files: []string{"network.tf"}}).With(&run.Result{Files: written})
	if !slices.Equal(files.Files, []string{AddedFileName(2), "network.tf", "sqs.tf"}) {
		t.Errorf("the files of both: %v", files.Files)
	}
}

// googleStagingProviders writes into staging the providers.tf and
// versions.tf Generate writes for a module call that creates resources
// with google-beta (see writeModuleProviders), pinned as pinModuleProviders
// pins it.
func googleStagingProviders(t *testing.T, staging string) {
	t.Helper()
	writeConfig(t, staging, rootfile.ProvidersFileName, `provider "google" {
  project = "acme-prod"
}

provider "google-beta" {
  project = "acme-prod"
}
`)
	writeConfig(t, staging, rootfile.VersionsFileName, `terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 7.2"
    }
    google-beta = {
      source  = "hashicorp/google-beta"
      version = "~> 7.2"
    }
  }
}
`)
}

// A module call that needs google-beta brings its provider block and its
// pinned requirement into a root that has neither; the root's own stay as
// they were. Before, the call ran on an unconfigured, unpinned provider.
func TestMergeAddsTheProvidersOfModuleCalls(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rootProviders := "provider \"google\" {\n  project = \"acme-prod\"\n  region  = \"us-central1\"\n}\n"
	writeConfig(t, root, rootfile.ProvidersFileName, rootProviders)
	writeConfig(t, root, rootfile.VersionsFileName, `terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 7.1"
    }
  }
}
`)
	writeConfig(t, root, rootfile.GeneratedFileName, "resource \"google_pubsub_topic\" \"orders\" {\n  name = \"orders\"\n}\n")
	staging := t.TempDir()
	writeConfig(t, staging, rootfile.GeneratedFileName, `module "main" {
  source       = "terraform-google-modules/network/google"
  version      = "13.0.0"
  network_name = "main"
  project_id   = "acme-prod"
}
`)
	googleStagingProviders(t, staging)

	if _, err := mergeInto(t, staging, root, nil, nil, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	providers := squashed(readFile(t, root, rootfile.ProvidersFileName))
	if !strings.HasPrefix(providers, squashed(rootProviders)) || !strings.Contains(providers, `provider "google-beta" { project = "acme-prod" }`) || strings.Count(providers, "provider ") != 2 {
		t.Errorf("providers.tf: want the root's provider and google-beta:\n%s", providers)
	}
	versions := squashed(readFile(t, root, rootfile.VersionsFileName))
	for _, want := range []string{
		`google = { source = "hashicorp/google" version = "~> 7.1" }`,
		`google-beta = { source = "hashicorp/google-beta" version = "~> 7.2" }`,
	} {
		if !strings.Contains(versions, want) {
			t.Errorf("versions.tf misses %q:\n%s", want, versions)
		}
	}

	// A root that has them gets nothing more.
	before := dirFiles(t, root)
	if _, err := mergeInto(t, staging, root, nil, nil, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	after := dirFiles(t, root)
	for _, name := range []string{rootfile.ProvidersFileName, rootfile.VersionsFileName} {
		if after[name] != before[name] {
			t.Errorf("%s changed:\n%s", name, after[name])
		}
	}
}

// A root that requires none of its providers, as one written by hand may,
// gets a required_providers block with only the provider it lacks.
func TestMergeRequiresOnlyTheNewProviders(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeConfig(t, root, "main.tf", "provider \"google\" {\n  project = \"acme-prod\"\n}\n")
	staging := t.TempDir()
	writeConfig(t, staging, rootfile.GeneratedFileName, "module \"main\" {\n  source = \"terraform-google-modules/network/google\"\n}\n")
	googleStagingProviders(t, staging)

	if _, err := mergeInto(t, staging, root, nil, nil, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	versions := squashed(readFile(t, root, rootfile.VersionsFileName))
	if !strings.Contains(versions, `terraform { required_providers { google-beta = { source = "hashicorp/google-beta" version = "~> 7.2" } } }`) || strings.Contains(versions, "hashicorp/google\"") {
		t.Errorf("versions.tf:\n%s", versions)
	}
	if providers := squashed(readFile(t, root, rootfile.ProvidersFileName)); providers != `provider "google-beta" { project = "acme-prod" }` {
		t.Errorf("providers.tf:\n%s", providers)
	}
}

// What an incremental import adds reads the data sources and takes the
// variables with a default that the root declares the same, under the
// root's names, rather than declaring them again under others; a secret's
// variable, without a default, stays a variable of its own. Before, the
// root declared the network lookup twice, as main and main_2.
func TestMergeReusesTheRootsLookupsAndVariables(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeConfig(t, root, rootfile.GeneratedFileName, "resource \"google_pubsub_topic\" \"orders\" {\n  name = \"orders\"\n}\n")
	writeConfig(t, root, rootfile.DataFileName, `data "google_compute_network" "main" {
  name    = "main"
  project = "acme-prod"
}
`)
	subnetwork := `description = "The self link of google_compute_subnetwork.app, which the root google/acme-prod/us-central1 manages. The default is its value when this root was imported."
  type        = string
  default     = "https://www.googleapis.com/compute/v1/projects/acme-prod/regions/us-central1/subnetworks/app"`
	writeConfig(t, root, hclfile.VariablesFileName, `variable "app_subnetwork_self_link" {
  `+subnetwork+`
}

variable "token" {
  type      = string
  sensitive = true
}
`)
	staging := t.TempDir()
	writeConfig(t, staging, rootfile.GeneratedFileName, `resource "google_compute_router" "edge" {
  name       = "edge"
  network    = data.google_compute_network.main_2.self_link
  subnetwork = var.app_subnetwork_self_link_2
  token      = var.token_2
  other      = data.google_compute_network.other.self_link
}
`)
	writeConfig(t, staging, rootfile.DataFileName, `data "google_compute_network" "main_2" {
  name    = "main"
  project = "acme-prod"
}

data "google_compute_network" "other" {
  name    = "other"
  project = "acme-prod"
}
`)
	writeConfig(t, staging, hclfile.VariablesFileName, `variable "app_subnetwork_self_link_2" {
  `+subnetwork+`
}

variable "token_2" {
  type      = string
  sensitive = true
}
`)

	written, err := mergeInto(t, staging, root, nil, nil, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	added := squashed(readFile(t, root, written[0]))
	for _, want := range []string{
		"network = data.google_compute_network.main.self_link",
		"subnetwork = var.app_subnetwork_self_link",
		"token = var.token_2",
		"other = data.google_compute_network.other.self_link",
	} {
		if !strings.Contains(added, want+" ") {
			t.Errorf("%s misses %q:\n%s", written[0], want, added)
		}
	}
	data := readFile(t, root, rootfile.DataFileName)
	if strings.Count(data, "data ") != 2 || strings.Contains(data, "main_2") || !strings.Contains(data, `data "google_compute_network" "other"`) {
		t.Errorf("data.tf: want the root's lookup and the new one:\n%s", data)
	}
	variables := readFile(t, root, hclfile.VariablesFileName)
	if strings.Count(variables, "variable ") != 3 || strings.Contains(variables, "app_subnetwork_self_link_2") || !strings.Contains(variables, `variable "token_2"`) {
		t.Errorf("variables.tf: want the root's variables and the secret's:\n%s", variables)
	}
}

// A data source or variable declared otherwise, as with another default,
// is one of its own.
func TestDeclarationKey(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, t.TempDir(), "main.tf", `data "google_compute_network" "a" {
  project = "acme-prod"
  name    = "main"
}

data "google_compute_network" "b" {
  name = "main"
  project = "acme-prod"
}

data "google_compute_network" "c" {
  name    = "main"
  project = "acme-test"
}

data "google_compute_subnetwork" "d" {
  name    = "main"
  project = "acme-prod"
}

variable "e" {
  default = "main"
}

data "aws_ami" "f" {
  filter {
    name   = "name"
    values = ["al2023-*"]
  }
}
`)
	keys := map[string]string{}
	for _, b := range f.Syntax.Blocks {
		keys[b.Labels[len(b.Labels)-1]] = declarationKey(b, f.Src)
	}
	if keys["a"] != keys["b"] || keys["a"] == "" {
		t.Errorf("the same lookup, in another order: %q, %q", keys["a"], keys["b"])
	}
	for _, other := range []string{"c", "d", "e"} {
		if keys[other] == keys["a"] {
			t.Errorf("%s: declared like a", other)
		}
	}
	if keys["f"] != "" {
		t.Errorf("a block with nested blocks: %q", keys["f"])
	}
}

// What moved and removed blocks take resources from is a name of the
// root, which an incremental import gives nothing it adds: Terraform
// rejects a moved block from an address the configuration declares.
func TestRootNamesHaveWhatMovedBlocksMoveFrom(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, t.TempDir(), rootfile.MovesFileName, `moved {
  from = aws_s3_bucket.logs
  to   = module.logs.aws_s3_bucket.this[0]
}

moved {
  from = aws_subnet.a["x"]
  to   = aws_subnet.private
}

removed {
  from = module.old

  lifecycle {
    destroy = false
  }
}
`)
	want := run.Names{"aws_s3_bucket.logs": true, "aws_subnet.a": true, "module.old": true}
	if got := run.RootNames([]*hclfile.File{f}); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// An add's options pin the providers to staging's lock, if it has one,
// call only the curated modules staging can call by the same source,
// make no local modules and keep generated.tf, and take the root's
// resources as external, once each; the caller's Options stay as they
// were.
func TestAdding(t *testing.T) {
	t.Parallel()
	versions := rootfile.VersionsFile(">= 1.5, < 2.0", rootfile.Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.67"})
	lock := []byte(`provider "registry.terraform.io/hashicorp/aws" {
  version     = "6.30.1"
  constraints = "~> 6.30"
}
`)
	pinned := rootfile.VersionsFile(">= 1.5, < 2.0", rootfile.Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.30"})
	local := adapters.Adapter{Source: "../modules/queue"}
	absolute := adapters.Adapter{Source: filepath.Join(t.TempDir(), "queue")}
	vpc := run.External{Type: "aws_vpc", ID: "vpc-0abc1234"}
	subnet := run.External{Type: "aws_subnet", ID: "subnet-0abc1234"}
	existing := map[run.External]string{vpc: "aws_vpc.main", subnet: "aws_subnet.a"}
	for name, tc := range map[string]struct {
		lock     []byte
		versions []byte
	}{
		"with a lock":    {lock: lock, versions: pinned},
		"without a lock": {versions: versions},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			opts := run.Options{
				Config:     map[string][]byte{rootfile.VersionsFileName: versions},
				Adapters:   []adapters.Adapter{local, absolute},
				ModulesDir: "modules",
				SplitFiles: true,
				External:   []run.External{vpc},
			}

			r, err := adding(opts, tc.lock, nil, existing)
			if err != nil {
				t.Fatal(err)
			}

			o := r.Opts
			if got := string(o.Config[rootfile.VersionsFileName]); got != string(tc.versions) {
				t.Errorf("versions.tf: got\n%s\nwant\n%s", got, tc.versions)
			}
			if !reflect.DeepEqual(o.Adapters, []adapters.Adapter{local}) {
				t.Errorf("adapters: got %+v, want only %+v", o.Adapters, local)
			}
			if o.ModulesDir != "" || o.SplitFiles {
				t.Errorf("modulesDir %q, splitFiles %v: want neither", o.ModulesDir, o.SplitFiles)
			}
			if want := []run.External{vpc, subnet}; !reflect.DeepEqual(o.External, want) {
				t.Errorf("external: got %+v, want %+v", o.External, want)
			}
			if string(opts.Config[rootfile.VersionsFileName]) != string(versions) || !reflect.DeepEqual(opts.Adapters, []adapters.Adapter{local, absolute}) || !reflect.DeepEqual(opts.External, []run.External{vpc}) || opts.ModulesDir != "modules" || !opts.SplitFiles {
				t.Errorf("the caller's options changed: %+v", opts)
			}
		})
	}
}

// An azapi resource's ID ends with the API version, which changes while
// the resource stays: Existing keys it as the lookups of what is added do.
func TestExistingKeysByIdentity(t *testing.T) {
	t.Parallel()
	id := "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Web/sites/app"
	previous := &run.Result{Imported: []rootfile.Import{{Type: "azapi_resource", Name: "app", ID: id + "?api-version=2023-04-01"}}}
	got, err := Existing(t.TempDir(), previous)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[run.ResourceKey("azapi_resource", id+"?api-version=2024-10-01")]; !ok {
		t.Errorf("not found by the identity of the resource: %v", got)
	}
}

// Merging the locals of staging into a root that has its own tags keeps
// the root's, and adds the others.
func TestMergeLocalsKeepsTheRootsTags(t *testing.T) {
	t.Parallel()
	root, staging := t.TempDir(), t.TempDir()
	writeConfig(t, root, rootfile.LocalsFileName, `locals {
  tags = { env = "prod" }
}
`)
	writeConfig(t, staging, rootfile.LocalsFileName, `locals {
  rg   = "/subscriptions/0000/resourceGroups/app"
  tags = { env = "prod" }
}
`)
	writeConfig(t, staging, rootfile.ProvidersFileName, "provider \"azurerm\" {\n  features {}\n}\n")
	rootWS := rootWorkspace(t, root)
	if err := mergeLocals(staging, rootWS); err != nil {
		t.Fatal(err)
	}
	locals := squashed(wsFile(t, rootWS, rootfile.LocalsFileName))
	if strings.Count(locals, "tags =") != 1 || !strings.Contains(locals, `rg = "/subscriptions/0000/resourceGroups/app"`) {
		t.Errorf("locals.tf:\n%s", locals)
	}
}

// Without the local the tags are in, nothing is lifted.
func TestAppliedTagsNeedTheLocal(t *testing.T) {
	t.Parallel()
	if _, ok := AppliedTags(nil, azureDefaultTags); ok {
		t.Error("tags applied without a local")
	}
}
