// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
	got := labelled([]Import{{Type: "aws_vpc", Name: "main", ID: "vpc-2"}, {Type: "aws_subnet", Name: "main", ID: "subnet-1"}}, Names{"aws_vpc.main": true})
	want := []Import{{Type: "aws_subnet", Name: "main", ID: "subnet-1"}, {Type: "aws_vpc", Name: "main_2", ID: "vpc-2"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestApplyTagLiftLeavesOutAppliedTags(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, networkGenerated)
	writeConfig(t, dir, ProvidersFileName, awsProviders)
	dt := awsDefaultTags
	dt.Applied = map[string]string{"Project": "shop", "Team": "data"}
	if err := applyDefaultTags(dir, dt); err != nil {
		t.Fatal(err)
	}
	changed, err := applyTagLift(dir, dt)
	if err != nil || !changed {
		t.Fatalf("got %v, %v", changed, err)
	}
	generated := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{
		`Team = "web"`, // a different value stays
		`"aws:cloudformation:stack-name" = "net"`,
	} {
		if !strings.Contains(generated, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, generated)
		}
	}
	if strings.Contains(generated, `Project = "shop"`) || strings.Contains(generated, `Team = "data"`) {
		t.Errorf("generated.tf repeats the applied tags:\n%s", generated)
	}
	if providers := squashed(readFile(t, dir, ProvidersFileName)); !strings.Contains(providers, "default_tags { tags = local.tags }") {
		t.Errorf("providers.tf doesn't apply the tags:\n%s", providers)
	}
	if locals := squashed(readFile(t, dir, LocalsFileName)); !strings.Contains(locals, `Project = "shop"`) {
		t.Errorf("locals.tf misses the tags:\n%s", locals)
	}
}

func TestMerge(t *testing.T) {
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

	added, err := merge(staging, root, existing, awsDataSources)
	if err != nil || added != AddedFileName(2) {
		t.Fatalf("got %q, %v", added, err)
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
	if added, err := merge(staging, root, existing, awsDataSources); err != nil || added != AddedFileName(3) {
		t.Fatalf("got %q, %v", added, err)
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

// A root that doesn't initialize, or whose checks fail to run, stays as it
// was: the next run adds the resources again.
func TestAddUndoesWhenTheRootDoesNotInitialize(t *testing.T) {
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
