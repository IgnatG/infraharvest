// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/adapters"
	tfjson "github.com/hashicorp/terraform-json"
)

// existingRoot writes a root an earlier import generated, and applied: its
// import blocks are gone but for one into a module.
func existingRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}

module "logs" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "5.16.1"
  bucket  = "logs"
}
`)
	writeConfig(t, dir, ImportsFileName, `import {
  to = module.logs.aws_s3_bucket.this[0]
  id = "logs"
}
`)
	writeConfig(t, dir, DataFileName, `data "aws_security_group" "sg_0def5678" {
  id = "sg-0def5678"
}
`)
	writeConfig(t, dir, LocalsFileName, `locals {
  tags   = { Project = "shop" }
  vpc_id = "vpc-0abc1234"
}
`)
	writeConfig(t, dir, VariablesFileName, `variable "aws_ssm_parameter_token_value" {
  type      = string
  sensitive = true
}
`)
	writeConfig(t, dir, ProvidersFileName, `provider "aws" {
  region = "us-east-1"
  default_tags {
    tags = local.tags
  }
}
`)
	return dir
}

func TestRootNames(t *testing.T) {
	t.Parallel()
	files, err := configFiles(existingRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	want := Names{
		"aws_vpc.main":                        true,
		"module.logs":                         true,
		"data.aws_security_group.sg_0def5678": true,
		"local.tags":                          true,
		"local.vpc_id":                        true,
		"var.aws_ssm_parameter_token_value":   true,
	}
	if got := rootNames(files); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestHasConfiguration(t *testing.T) {
	t.Parallel()
	if has, err := HasConfiguration(existingRoot(t)); err != nil || !has {
		t.Errorf("a generated root: got %v, %v", has, err)
	}
	empty := t.TempDir()
	writeConfig(t, empty, ProvidersFileName, awsProviders)
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
	previous := &Result{
		Imported: []Import{
			{Type: "aws_vpc", Name: "main", ID: "vpc-0abc1234"},
			{Type: "aws_s3_bucket", Name: "logs", ID: "logs"}, // moved into module.logs
		},
		Rejected: []Rejection{{Address: "aws_ecs_service.web", ID: "apps/web"}},
	}
	got, err := Existing(dir, previous)
	if err != nil {
		t.Fatal(err)
	}
	want := map[External]string{
		{Type: "aws_vpc", ID: "vpc-0abc1234"}:     "aws_vpc.main",
		{Type: "aws_s3_bucket", ID: "logs"}:       "",
		{Type: "aws_ecs_service", ID: "apps/web"}: "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// Import blocks alone tell too.
	if got, err := Existing(dir, nil); err != nil || len(got) != 1 || got[External{Type: "aws_s3_bucket", ID: "logs"}] != "" {
		t.Errorf("without a checkpoint: got %v, %v", got, err)
	}
}

func TestAppliedTags(t *testing.T) {
	t.Parallel()
	files, err := configFiles(existingRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if tags, ok := appliedTags(files, awsDefaultTags); !ok || !reflect.DeepEqual(tags, map[string]string{"Project": "shop"}) {
		t.Errorf("through a local: got %v, %v", tags, ok)
	}

	literal := t.TempDir()
	writeConfig(t, literal, ProvidersFileName, `provider "aws" {
  default_tags {
    tags = { Team = "web" }
  }
}
`)
	files, err = configFiles(literal)
	if err != nil {
		t.Fatal(err)
	}
	if tags, ok := appliedTags(files, awsDefaultTags); !ok || !reflect.DeepEqual(tags, map[string]string{"Team": "web"}) {
		t.Errorf("literal: got %v, %v", tags, ok)
	}

	none := t.TempDir()
	writeConfig(t, none, ProvidersFileName, awsProviders)
	files, err = configFiles(none)
	if err != nil {
		t.Fatal(err)
	}
	if tags, ok := appliedTags(files, awsDefaultTags); ok {
		t.Errorf("without default tags: got %v", tags)
	}
}

func TestLabelledLeavesTakenLabels(t *testing.T) {
	t.Parallel()
	got := labelled([]Import{{Type: "aws_vpc", Name: "main", ID: "vpc-2"}, {Type: "aws_subnet", Name: "main", ID: "subnet-1"}}, Names{"aws_vpc.main": true}, false)
	want := []Import{{Type: "aws_subnet", Name: "main", ID: "subnet-1"}, {Type: "aws_vpc", Name: "main_2", ID: "vpc-2"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestApplyTagLiftLeavesOutAppliedTags(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, networkGenerated)
	writeConfig(t, dir, ProvidersFileName, awsProviders)
	dt := awsDefaultTags
	dt.Applied = map[string]string{"Project": "shop", "Team": "data"}
	if err := applyDefaultTags(dir, dt); err != nil {
		t.Fatal(err)
	}
	changed, err := applyTagLift(dir, dt, nil)
	if err != nil || !changed {
		t.Fatalf("got %v, %v", changed, err)
	}
	generated := squashed(readFile(t, dir, GeneratedFileName))
	subnets, group, _ := strings.Cut(generated, `resource "aws_security_group" "web"`)
	for _, want := range []string{
		`Team = "web"`, // a value of its own stays
		`"aws:cloudformation:stack-name" = "net"`,
	} {
		if !strings.Contains(subnets, want) {
			t.Errorf("the subnets miss %q:\n%s", want, generated)
		}
	}
	if strings.Contains(subnets, `Project = "shop"`) || strings.Contains(subnets, `Team = "data"`) {
		t.Errorf("the subnets repeat the applied tags:\n%s", generated)
	}
	// The group has no Team, which the provider would add: it keeps its
	// tags, without the provider's.
	if !strings.Contains(group, `Project = "shop"`) || !strings.Contains(group, "provider = aws.without_default_tags") {
		t.Errorf("the group without Team doesn't keep its tags on the configuration without default tags:\n%s", generated)
	}
	providers := squashed(readFile(t, dir, ProvidersFileName))
	if !strings.Contains(providers, "default_tags { tags = local.tags }") {
		t.Errorf("providers.tf doesn't apply the tags:\n%s", providers)
	}
	if strings.Count(providers, `alias = "without_default_tags"`) != 1 {
		t.Errorf("providers.tf has no configuration without default tags, or two:\n%s", providers)
	}

	// The root gets the configuration once, as the root's own provider
	// without default_tags.
	root := t.TempDir()
	writeConfig(t, root, ProvidersFileName, "provider \"aws\" {\n  region = \"eu-west-2\"\n  default_tags {\n    tags = local.tags\n  }\n}\n")
	for i, want := range []bool{true, false} {
		added, err := addRootAlias(dir, root, dt)
		if err != nil || added != want {
			t.Fatalf("run %d: added %v, %v; want %v", i+1, added, err, want)
		}
	}
	rootProviders := squashed(readFile(t, root, ProvidersFileName))
	if !strings.HasSuffix(rootProviders, `provider "aws" { alias = "without_default_tags" region = "eu-west-2" }`) || strings.Count(rootProviders, "alias") != 1 {
		t.Errorf("root's providers.tf:\n%s", rootProviders)
	}
	if locals := squashed(readFile(t, dir, LocalsFileName)); !strings.Contains(locals, `Project = "shop"`) {
		t.Errorf("locals.tf misses the tags:\n%s", locals)
	}
}

func TestMerge(t *testing.T) {
	t.Parallel()
	root := existingRoot(t)
	generatedBefore := readFile(t, root, GeneratedFileName)
	staging := t.TempDir()
	writeConfig(t, staging, GeneratedFileName, `resource "aws_subnet" "a" {
  cidr_block = "10.0.1.0/24"
  vpc_id     = data.aws_vpc.vpc_0abc1234.id
}

resource "aws_instance" "web" {
  subnet_id              = aws_subnet.a.id
  vpc_security_group_ids = [data.aws_security_group.sg_0def5678_2.id, data.aws_security_group.sg_0aaa1111.id]
  ami                    = local.ami
}
`)
	writeConfig(t, staging, DataFileName, `data "aws_vpc" "vpc_0abc1234" {
  id = "vpc-0abc1234"
}

data "aws_security_group" "sg_0def5678_2" {
  id = "sg-0def5678"
}

data "aws_security_group" "sg_0aaa1111" {
  id = "sg-0aaa1111"
}
`)
	writeConfig(t, staging, ImportsFileName, `import {
  to = aws_subnet.a
  id = "subnet-1"
}

import {
  to = aws_instance.web
  id = "i-1"
}
`)
	writeConfig(t, staging, LocalsFileName, `locals {
  ami  = "ami-0123456789abcdef0"
  tags = { Project = "shop" }
}
`)
	writeConfig(t, staging, ProvidersFileName, `provider "aws" {
  default_tags {
    tags = local.tags
  }
}
`)
	writeConfig(t, staging, VariablesFileName, `variable "aws_instance_web_user_data" {
  type      = string
  sensitive = true
}
`)
	existing := map[External]string{{Type: "aws_vpc", ID: "vpc-0abc1234"}: "aws_vpc.main"}

	written, err := merge(staging, root, existing, awsDataSources, map[string]string{})
	if err != nil || !slices.Equal(written, []string{AddedFileName(2)}) {
		t.Fatalf("got %q, %v", written, err)
	}

	if got := readFile(t, root, GeneratedFileName); got != generatedBefore {
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
	data := squashed(readFile(t, root, DataFileName))
	if !strings.Contains(data, `data "aws_security_group" "sg_0aaa1111"`) || strings.Count(data, "data ") != 2 {
		t.Errorf("data.tf: want the root's data source and the new one:\n%s", data)
	}
	imports := squashed(readFile(t, root, ImportsFileName))
	for _, want := range []string{"to = module.logs.aws_s3_bucket.this[0]", "to = aws_subnet.a", "to = aws_instance.web"} {
		if !strings.Contains(imports, want) {
			t.Errorf("imports.tf misses %q:\n%s", want, imports)
		}
	}
	if variables := readFile(t, root, VariablesFileName); strings.Count(variables, "variable ") != 2 {
		t.Errorf("variables.tf: want both variables:\n%s", variables)
	}
	locals := squashed(readFile(t, root, LocalsFileName))
	if !strings.Contains(locals, `ami = "ami-0123456789abcdef0"`) || strings.Count(locals, "tags") != 1 || strings.Count(locals, "locals") != 1 {
		t.Errorf("locals.tf: want the new local in the root's block, and the root's tags only:\n%s", locals)
	}

	// The next addition takes the next file.
	writeConfig(t, staging, GeneratedFileName, `resource "aws_sqs_queue" "jobs" {
  name = "jobs"
}
`)
	for _, name := range []string{DataFileName, ImportsFileName, LocalsFileName, VariablesFileName} {
		if err := os.Remove(filepath.Join(staging, name)); err != nil {
			t.Fatal(err)
		}
	}
	if written, err := merge(staging, root, existing, awsDataSources, map[string]string{}); err != nil || !slices.Equal(written, []string{AddedFileName(3)}) {
		t.Fatalf("got %q, %v", written, err)
	}
	if got := readFile(t, root, AddedFileName(3)); !strings.Contains(got, `resource "aws_sqs_queue" "jobs"`) {
		t.Errorf("%s:\n%s", AddedFileName(3), got)
	}
}

// dirFiles returns the files of dir by name, to compare before and after.
func dirFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() {
			files[e.Name()] = readFile(t, dir, e.Name())
		}
	}
	return files
}

const queueGenerated = "resource \"aws_sqs_queue\" \"jobs\" {\n  name = \"jobs\"\n}\n"

// addOptions are Generate's options for the staging directory.
var addOptions = Options{Config: map[string][]byte{VersionsFileName: []byte("# versions\n"), ProvidersFileName: []byte("# providers\n")}}

func TestAdd(t *testing.T) {
	t.Parallel()
	root := existingRoot(t)
	staging := filepath.Join(t.TempDir(), "staging")
	tf := &fakeTerraform{dir: staging, generated: queueGenerated}
	rootTF := &fakeTerraform{dir: root}
	existing := map[External]string{{Type: "aws_vpc", ID: "vpc-0abc1234"}: "aws_vpc.main"}

	result, err := Add(context.Background(), tf, rootTF, staging, root, []Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs"}}, addOptions, existing)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Imported) != 1 || len(result.Gate) == 0 {
		t.Errorf("result: %+v", result)
	}
	if got := readFile(t, root, AddedFileName(2)); !strings.Contains(got, `resource "aws_sqs_queue" "jobs"`) {
		t.Errorf("%s:\n%s", AddedFileName(2), got)
	}
	if imports := readFile(t, root, ImportsFileName); !strings.Contains(imports, "to = aws_sqs_queue.jobs") || !strings.Contains(imports, "to = module.logs.aws_s3_bucket.this[0]") {
		t.Errorf("imports.tf:\n%s", imports)
	}
	if got := strings.Join(rootTF.calls, ","); got != "init,fmt,validate" {
		t.Errorf("root calls: %s", got)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("staging left behind: %v", err)
	}
}

// The root's gate leaves the organisation's own modules the root calls to
// their owners, as Generate's and Verify's do, although staging, which
// isn't the root, calls none of them.
func TestAddGatesTheRootWithItsOwnModules(t *testing.T) {
	t.Parallel()
	root := existingRoot(t)
	own := t.TempDir()
	writeConfig(t, own, "main.tf", "resource \"aws_sqs_queue\" \"this\" {}\n")
	source, err := filepath.Rel(root, own)
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, root, "own.tf", "module \"own\" {\n  source = \""+filepath.ToSlash(source)+"\"\n}\n")
	staging := filepath.Join(t.TempDir(), "staging")
	tf := &fakeTerraform{dir: staging, generated: queueGenerated}
	rootTF := &fakeTerraform{dir: root}
	opts := addOptions
	opts.Adapters = []adapters.Adapter{{Source: own}}

	result, err := Add(context.Background(), tf, rootTF, staging, root, []Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs"}}, opts, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range result.Gate {
		for _, d := range c.Details {
			if strings.Contains(d, "module-structure") {
				t.Errorf("%s: %s", c.Name, d)
			}
		}
	}
}

// The root's scanners don't run where staging's plan marked a value of
// the added configuration sensitive, which only staging's plan knew.
func TestAddScannersDontSeeStagingSecrets(t *testing.T) {
	t.Parallel()
	root := existingRoot(t)
	staging := filepath.Join(t.TempDir(), "staging")
	sensitive := "staging-only-queue-name"
	shown := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{{
		Address: "aws_sqs_queue.jobs", Mode: tfjson.ManagedResourceMode,
		Change: &tfjson.Change{
			Actions:         tfjson.Actions{tfjson.ActionNoop},
			Before:          map[string]interface{}{"name": sensitive},
			BeforeSensitive: map[string]interface{}{"name": true},
		},
	}}}
	tf := &fakeTerraform{dir: staging, generated: "resource \"aws_sqs_queue\" \"jobs\" {\n  name = \"" + sensitive + "\"\n}\n", shown: shown}
	rootTF := &fakeTerraform{dir: root}
	var ran []string
	spy := Scanner{Name: "spy", Run: func(_ context.Context, dir string) ([]Finding, error) {
		ran = append(ran, dir)
		return nil, nil
	}}
	opts := addOptions
	opts.Scanners = []Scanner{spy}

	result, err := Add(context.Background(), tf, rootTF, staging, root, []Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs"}}, opts, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(ran) > 0 {
		t.Errorf("a scanner ran on %v, holding staging's secret", ran)
	}
	if c := result.Gate.check(CheckSecrets); c.Passed {
		t.Errorf("the secrets check passed: %+v", c)
	}
}

// A root applied without a backend, its state in its directory, is added
// to as any other: its checks don't take the state for one committed with
// the configuration, nor read it, and it stays as it was.
func TestAddToARootWithLocalState(t *testing.T) {
	t.Parallel()
	root := existingRoot(t)
	state := `{"resources": [{"instances": [{"attributes": {"id": "vpc-0abc1234"}}]}]}`
	if err := os.WriteFile(filepath.Join(root, "terraform.tfstate"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(t.TempDir(), "staging")
	tf := &fakeTerraform{dir: staging, generated: queueGenerated}
	rootTF := &fakeTerraform{dir: root}
	existing := map[External]string{{Type: "aws_vpc", ID: "vpc-0abc1234"}: "aws_vpc.main"}

	result, err := Add(context.Background(), tf, rootTF, staging, root, []Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs"}}, addOptions, existing)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range result.Gate {
		for _, d := range c.Details {
			if strings.Contains(d, "tfstate") {
				t.Errorf("%s: %s", c.Name, d)
			}
		}
	}
	if got := readFile(t, root, "terraform.tfstate"); got != state {
		t.Errorf("the root's state changed: %s", got)
	}
}

// A root that doesn't initialize, or whose checks fail to run, stays as it
// was: the next run adds the resources again.
func TestAddUndoesWhenTheRootDoesNotInitialize(t *testing.T) {
	t.Parallel()
	root := existingRoot(t)
	before := dirFiles(t, root)
	staging := filepath.Join(t.TempDir(), "staging")
	tf := &fakeTerraform{dir: staging, generated: queueGenerated}
	rootTF := &fakeTerraform{dir: root, initErr: errors.New("no provider")}

	_, err := Add(context.Background(), tf, rootTF, staging, root, []Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs"}}, addOptions, nil)
	if err == nil || !strings.Contains(err.Error(), "terraform init: no provider") {
		t.Fatalf("want the init error, got %v", err)
	}

	if after := dirFiles(t, root); !reflect.DeepEqual(after, before) {
		t.Errorf("root changed:\nbefore %v\nafter %v", before, after)
	}
}

func TestResultWith(t *testing.T) {
	t.Parallel()
	previous := &Result{Imported: []Import{{Type: "aws_vpc", Name: "main", ID: "vpc-1"}}, Gate: Gate{{Name: CheckFormat}}}
	added := &Result{Imported: []Import{{Type: "aws_subnet", Name: "a", ID: "subnet-1"}}, Gate: Gate{{Name: CheckFormat, Passed: true}}}
	got := previous.With(added)
	if len(got.Imported) != 2 || !got.Gate.Passed() || len(previous.Imported) != 1 {
		t.Errorf("got %+v", got)
	}
	var none *Result
	if none.With(added) != added {
		t.Error("without an earlier result: want what was added")
	}
}

// A root whose resources refer to local.tags: what is added refers to it
// too, and the local stays root's own.
func TestAddedResourcesReferToTheRootsTags(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeConfig(t, root, LocalsFileName, `locals {
  tags = { env = "prod" }
}
`)
	files, err := configFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	applied, ok := appliedTags(files, azureDefaultTags)
	if !ok || !reflect.DeepEqual(applied, map[string]string{"env": "prod"}) {
		t.Fatalf("got %v, %v", applied, ok)
	}

	staging := t.TempDir()
	writeConfig(t, staging, GeneratedFileName, azureGenerated)
	writeConfig(t, staging, ProvidersFileName, "provider \"azurerm\" {\n  features {}\n}\n")
	dt := azureDefaultTags
	dt.Applied = applied
	if err := applyDefaultTags(staging, dt); err != nil {
		t.Fatal(err)
	}
	changed, err := applyTagLift(staging, dt, nil)
	if err != nil || !changed {
		t.Fatalf("got %v, %v", changed, err)
	}
	if generated := squashed(readFile(t, staging, GeneratedFileName)); strings.Count(generated, "local.tags") != 3 {
		t.Errorf("generated.tf:\n%s", generated)
	}
	if providers := readFile(t, staging, ProvidersFileName); strings.Contains(providers, "tags") {
		t.Errorf("providers.tf:\n%s", providers)
	}

	writeConfig(t, staging, LocalsFileName, `locals {
  rg   = "/subscriptions/0000/resourceGroups/app"
  tags = { env = "prod" }
}
`)
	if err := mergeLocals(staging, root); err != nil {
		t.Fatal(err)
	}
	locals := squashed(readFile(t, root, LocalsFileName))
	if strings.Count(locals, "tags =") != 1 || !strings.Contains(locals, `rg = "/subscriptions/0000/resourceGroups/app"`) {
		t.Errorf("locals.tf:\n%s", locals)
	}

	// Without the local, nothing is lifted.
	if _, ok := appliedTags(nil, azureDefaultTags); ok {
		t.Error("tags applied without a local")
	}
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
	writeConfig(t, staging, GeneratedFileName, `resource "aws_instance" "web" {
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

	written, err := merge(staging, root, nil, awsDataSources, fileOf)
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

	files := (&Result{Files: []string{"network.tf"}}).With(&Result{Files: written})
	if !slices.Equal(files.Files, []string{AddedFileName(2), "network.tf", "sqs.tf"}) {
		t.Errorf("the files of both: %v", files.Files)
	}
}

// A root that doesn't initialize keeps the files of the types as they were.
func TestAddUndoesWhatItAddedToTheFilesOfTheTypes(t *testing.T) {
	t.Parallel()
	root := existingRoot(t)
	writeConfig(t, root, "sqs.tf", "resource \"aws_sqs_queue\" \"old\" {\n  name = \"old\"\n}\n")
	before := dirFiles(t, root)
	staging := filepath.Join(t.TempDir(), "staging")
	tf := &fakeTerraform{dir: staging, generated: queueGenerated}
	rootTF := &fakeTerraform{dir: root, initErr: errors.New("no provider")}

	_, err := Add(context.Background(), tf, rootTF, staging, root, []Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs", File: "sqs.tf"}}, addOptions, nil)
	if err == nil {
		t.Fatal("want the init error")
	}
	if after := dirFiles(t, root); !reflect.DeepEqual(after, before) {
		t.Errorf("root changed:\nbefore %v\nafter %v", before, after)
	}
}

// googleStagingProviders writes into staging the providers.tf and
// versions.tf Generate writes for a module call that creates resources
// with google-beta (see writeModuleProviders), pinned as pinModuleProviders
// pins it.
func googleStagingProviders(t *testing.T, staging string) {
	t.Helper()
	writeConfig(t, staging, ProvidersFileName, `provider "google" {
  project = "acme-prod"
}

provider "google-beta" {
  project = "acme-prod"
}
`)
	writeConfig(t, staging, VersionsFileName, `terraform {
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
	writeConfig(t, root, ProvidersFileName, rootProviders)
	writeConfig(t, root, VersionsFileName, `terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 7.1"
    }
  }
}
`)
	writeConfig(t, root, GeneratedFileName, "resource \"google_pubsub_topic\" \"orders\" {\n  name = \"orders\"\n}\n")
	staging := t.TempDir()
	writeConfig(t, staging, GeneratedFileName, `module "main" {
  source       = "terraform-google-modules/network/google"
  version      = "13.0.0"
  network_name = "main"
  project_id   = "acme-prod"
}
`)
	googleStagingProviders(t, staging)

	if _, err := merge(staging, root, nil, nil, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	providers := squashed(readFile(t, root, ProvidersFileName))
	if !strings.HasPrefix(providers, squashed(rootProviders)) || !strings.Contains(providers, `provider "google-beta" { project = "acme-prod" }`) || strings.Count(providers, "provider ") != 2 {
		t.Errorf("providers.tf: want the root's provider and google-beta:\n%s", providers)
	}
	versions := squashed(readFile(t, root, VersionsFileName))
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
	if _, err := merge(staging, root, nil, nil, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	after := dirFiles(t, root)
	for _, name := range []string{ProvidersFileName, VersionsFileName} {
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
	writeConfig(t, staging, GeneratedFileName, "module \"main\" {\n  source = \"terraform-google-modules/network/google\"\n}\n")
	googleStagingProviders(t, staging)

	if _, err := merge(staging, root, nil, nil, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	versions := squashed(readFile(t, root, VersionsFileName))
	if !strings.Contains(versions, `terraform { required_providers { google-beta = { source = "hashicorp/google-beta" version = "~> 7.2" } } }`) || strings.Contains(versions, "hashicorp/google\"") {
		t.Errorf("versions.tf:\n%s", versions)
	}
	if providers := squashed(readFile(t, root, ProvidersFileName)); providers != `provider "google-beta" { project = "acme-prod" }` {
		t.Errorf("providers.tf:\n%s", providers)
	}
}

// A root that doesn't initialize gets its versions.tf back, without the
// provider the added resources required.
func TestAddUndoesTheProvidersItAdded(t *testing.T) {
	t.Parallel()
	root := existingRoot(t)
	writeConfig(t, root, VersionsFileName, "terraform {\n  required_providers {\n    aws = {\n      source = \"hashicorp/aws\"\n    }\n  }\n}\n")
	before := dirFiles(t, root)
	staging := filepath.Join(t.TempDir(), "staging")
	tf := &fakeTerraform{dir: staging, generated: queueGenerated}
	rootTF := &fakeTerraform{dir: root, initErr: errors.New("no provider")}
	opts := Options{Config: map[string][]byte{
		VersionsFileName:  []byte("terraform {\n  required_providers {\n    aws = {\n      source = \"hashicorp/aws\"\n    }\n    awscc = {\n      source  = \"hashicorp/awscc\"\n      version = \"~> 1.0\"\n    }\n  }\n}\n"),
		ProvidersFileName: []byte("provider \"aws\" {\n  region = \"us-east-1\"\n}\n\nprovider \"awscc\" {\n  region = \"us-east-1\"\n}\n"),
	}}

	if _, err := Add(context.Background(), tf, rootTF, staging, root, []Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs"}}, opts, nil); err == nil {
		t.Fatal("want the init error")
	}
	if after := dirFiles(t, root); !reflect.DeepEqual(after, before) {
		t.Errorf("root changed:\nbefore %v\nafter %v", before, after)
	}

	// Initialized, it keeps them.
	staging = filepath.Join(t.TempDir(), "staging")
	tf = &fakeTerraform{dir: staging, generated: queueGenerated}
	if _, err := Add(context.Background(), tf, &fakeTerraform{dir: root}, staging, root, []Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs"}}, opts, nil); err != nil {
		t.Fatal(err)
	}
	if versions := squashed(readFile(t, root, VersionsFileName)); !strings.Contains(versions, `awscc = { source = "hashicorp/awscc" version = "~> 1.0" }`) {
		t.Errorf("versions.tf:\n%s", versions)
	}
	if providers := readFile(t, root, ProvidersFileName); strings.Count(providers, "provider ") != 2 || !strings.Contains(providers, `provider "awscc"`) {
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
	writeConfig(t, root, GeneratedFileName, "resource \"google_pubsub_topic\" \"orders\" {\n  name = \"orders\"\n}\n")
	writeConfig(t, root, DataFileName, `data "google_compute_network" "main" {
  name    = "main"
  project = "acme-prod"
}
`)
	subnetwork := `description = "The self link of google_compute_subnetwork.app, which the root google/acme-prod/us-central1 manages. The default is its value when this root was imported."
  type        = string
  default     = "https://www.googleapis.com/compute/v1/projects/acme-prod/regions/us-central1/subnetworks/app"`
	writeConfig(t, root, VariablesFileName, `variable "app_subnetwork_self_link" {
  `+subnetwork+`
}

variable "token" {
  type      = string
  sensitive = true
}
`)
	staging := t.TempDir()
	writeConfig(t, staging, GeneratedFileName, `resource "google_compute_router" "edge" {
  name       = "edge"
  network    = data.google_compute_network.main_2.self_link
  subnetwork = var.app_subnetwork_self_link_2
  token      = var.token_2
  other      = data.google_compute_network.other.self_link
}
`)
	writeConfig(t, staging, DataFileName, `data "google_compute_network" "main_2" {
  name    = "main"
  project = "acme-prod"
}

data "google_compute_network" "other" {
  name    = "other"
  project = "acme-prod"
}
`)
	writeConfig(t, staging, VariablesFileName, `variable "app_subnetwork_self_link_2" {
  `+subnetwork+`
}

variable "token_2" {
  type      = string
  sensitive = true
}
`)

	written, err := merge(staging, root, nil, nil, map[string]string{})
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
	data := readFile(t, root, DataFileName)
	if strings.Count(data, "data ") != 2 || strings.Contains(data, "main_2") || !strings.Contains(data, `data "google_compute_network" "other"`) {
		t.Errorf("data.tf: want the root's lookup and the new one:\n%s", data)
	}
	variables := readFile(t, root, VariablesFileName)
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
	for _, b := range f.syntax.Blocks {
		keys[b.Labels[len(b.Labels)-1]] = declarationKey(b, f.src)
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

// The lock file staging holds, such as root's, is still there when the
// additions are generated, and the providers staging requires accept it.
func TestAddGeneratesWithTheLockInStaging(t *testing.T) {
	t.Parallel()
	root := existingRoot(t)
	staging := filepath.Join(t.TempDir(), "staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	lock := "provider \"registry.terraform.io/hashicorp/aws\" {\n  version = \"6.30.1\"\n}\n"
	if err := os.WriteFile(filepath.Join(staging, LockFileName), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := addOptions
	opts.Config = map[string][]byte{
		VersionsFileName:  VersionsFile("", Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.67"}),
		ProvidersFileName: []byte("# providers\n"),
	}
	tf := &fakeTerraform{dir: staging, generated: queueGenerated}
	var lockAtInit, versionsAtInit string
	tf.onInit = func() {
		lockAtInit = readFile(t, staging, LockFileName)
		versionsAtInit = readFile(t, staging, VersionsFileName)
	}
	rootTF := &fakeTerraform{dir: root}

	if _, err := Add(context.Background(), tf, rootTF, staging, root, []Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs"}}, opts, nil); err != nil {
		t.Fatal(err)
	}

	if lockAtInit != lock {
		t.Errorf("lock at init:\n%s\nwant\n%s", lockAtInit, lock)
	}
	if !strings.Contains(versionsAtInit, `"~> 6.30"`) {
		t.Errorf("versions.tf at init doesn't accept the lock:\n%s", versionsAtInit)
	}
	if got := string(opts.Config[VersionsFileName]); !strings.Contains(got, `"~> 6.67"`) {
		t.Errorf("the caller's options changed:\n%s", got)
	}
}

// What moved and removed blocks take resources from is a name of the
// root, which an incremental import gives nothing it adds: Terraform
// rejects a moved block from an address the configuration declares.
func TestRootNamesHaveWhatMovedBlocksMoveFrom(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, t.TempDir(), MovesFileName, `moved {
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
	want := Names{"aws_s3_bucket.logs": true, "aws_subnet.a": true, "module.old": true}
	if got := rootNames([]*hclFile{f}); !reflect.DeepEqual(got, want) {
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
	versions := VersionsFile(">= 1.5, < 2.0", Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.67"})
	lock := []byte(`provider "registry.terraform.io/hashicorp/aws" {
  version     = "6.30.1"
  constraints = "~> 6.30"
}
`)
	pinned := VersionsFile(">= 1.5, < 2.0", Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.30"})
	local := adapters.Adapter{Source: "../modules/queue"}
	absolute := adapters.Adapter{Source: filepath.Join(t.TempDir(), "queue")}
	vpc := External{Type: "aws_vpc", ID: "vpc-0abc1234"}
	subnet := External{Type: "aws_subnet", ID: "subnet-0abc1234"}
	existing := map[External]string{vpc: "aws_vpc.main", subnet: "aws_subnet.a"}
	for name, tc := range map[string]struct {
		lock     []byte
		versions []byte
	}{
		"with a lock":    {lock: lock, versions: pinned},
		"without a lock": {versions: versions},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			opts := Options{
				Config:     map[string][]byte{VersionsFileName: versions},
				Adapters:   []adapters.Adapter{local, absolute},
				ModulesDir: "modules",
				SplitFiles: true,
				External:   []External{vpc},
			}

			r, err := adding(opts, tc.lock, nil, existing)
			if err != nil {
				t.Fatal(err)
			}

			o := r.opts
			if got := string(o.Config[VersionsFileName]); got != string(tc.versions) {
				t.Errorf("versions.tf: got\n%s\nwant\n%s", got, tc.versions)
			}
			if !reflect.DeepEqual(o.Adapters, []adapters.Adapter{local}) {
				t.Errorf("adapters: got %+v, want only %+v", o.Adapters, local)
			}
			if o.ModulesDir != "" || o.SplitFiles {
				t.Errorf("modulesDir %q, splitFiles %v: want neither", o.ModulesDir, o.SplitFiles)
			}
			if want := []External{vpc, subnet}; !reflect.DeepEqual(o.External, want) {
				t.Errorf("external: got %+v, want %+v", o.External, want)
			}
			if string(opts.Config[VersionsFileName]) != string(versions) || !reflect.DeepEqual(opts.Adapters, []adapters.Adapter{local, absolute}) || !reflect.DeepEqual(opts.External, []External{vpc}) || opts.ModulesDir != "modules" || !opts.SplitFiles {
				t.Errorf("the caller's options changed: %+v", opts)
			}
		})
	}
}
