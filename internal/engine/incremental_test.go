// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/incremental"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/run"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// existingRoot writes a root an earlier import generated, and applied: its
// import blocks are gone but for one into a module.
func existingRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}

module "logs" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "5.16.1"
  bucket  = "logs"
}
`)
	writeConfig(t, dir, rootfile.ImportsFileName, `import {
  to = module.logs.aws_s3_bucket.this[0]
  id = "logs"
}
`)
	writeConfig(t, dir, rootfile.DataFileName, `data "aws_security_group" "sg_0def5678" {
  id = "sg-0def5678"
}
`)
	writeConfig(t, dir, rootfile.LocalsFileName, `locals {
  tags   = { Project = "shop" }
  vpc_id = "vpc-0abc1234"
}
`)
	writeConfig(t, dir, hclfile.VariablesFileName, `variable "aws_ssm_parameter_token_value" {
  type      = string
  sensitive = true
}
`)
	writeConfig(t, dir, rootfile.ProvidersFileName, `provider "aws" {
  region = "us-east-1"
  default_tags {
    tags = local.tags
  }
}
`)
	return dir
}

func TestLabelledLeavesTakenLabels(t *testing.T) {
	t.Parallel()
	got := labelled([]rootfile.Import{{Type: "aws_vpc", Name: "main", ID: "vpc-2"}, {Type: "aws_subnet", Name: "main", ID: "subnet-1"}}, run.Names{"aws_vpc.main": true}, false)
	want := []rootfile.Import{{Type: "aws_subnet", Name: "main", ID: "subnet-1"}, {Type: "aws_vpc", Name: "main_2", ID: "vpc-2"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestApplyTagLiftLeavesOutAppliedTags(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
	writeConfig(t, dir, rootfile.ProvidersFileName, awsProviders)
	dt := awsDefaultTags
	dt.Applied = map[string]string{"Project": "shop", "Team": "data"}
	ws := rootWorkspace(t, dir)
	if err := applyDefaultTags(ws, dt); err != nil {
		t.Fatal(err)
	}
	flushed(t, ws)
	changed, err := applyTagLift(ws, dt, nil)
	if err != nil || !changed {
		t.Fatalf("got %v, %v", changed, err)
	}
	flushed(t, ws)
	generated := squashed(readFile(t, dir, rootfile.GeneratedFileName))
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
	providers := squashed(readFile(t, dir, rootfile.ProvidersFileName))
	if !strings.Contains(providers, "default_tags { tags = local.tags }") {
		t.Errorf("providers.tf doesn't apply the tags:\n%s", providers)
	}
	if strings.Count(providers, `alias = "without_default_tags"`) != 1 {
		t.Errorf("providers.tf has no configuration without default tags, or two:\n%s", providers)
	}

	// The root gets the configuration once, as the root's own provider
	// without default_tags.
	root := t.TempDir()
	writeConfig(t, root, rootfile.ProvidersFileName, "provider \"aws\" {\n  region = \"eu-west-2\"\n  default_tags {\n    tags = local.tags\n  }\n}\n")
	rootWS := rootWorkspace(t, root)
	for i, want := range []bool{true, false} {
		added, err := addRootAlias(dir, rootWS, dt)
		if err != nil || added != want {
			t.Fatalf("run %d: added %v, %v; want %v", i+1, added, err, want)
		}
	}
	rootProviders := squashed(wsFile(t, rootWS, rootfile.ProvidersFileName))
	if !strings.HasSuffix(rootProviders, `provider "aws" { alias = "without_default_tags" region = "eu-west-2" }`) || strings.Count(rootProviders, "alias") != 1 {
		t.Errorf("root's providers.tf:\n%s", rootProviders)
	}
	if locals := squashed(readFile(t, dir, rootfile.LocalsFileName)); !strings.Contains(locals, `Project = "shop"`) {
		t.Errorf("locals.tf misses the tags:\n%s", locals)
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
var addOptions = run.Options{Config: map[string][]byte{rootfile.VersionsFileName: []byte("# versions\n"), rootfile.ProvidersFileName: []byte("# providers\n")}}

func TestAdd(t *testing.T) {
	t.Parallel()
	root := existingRoot(t)
	staging := filepath.Join(t.TempDir(), "staging")
	tf := &fakeTerraform{dir: staging, generated: queueGenerated}
	rootTF := &fakeTerraform{dir: root}
	existing := map[run.External]string{{Type: "aws_vpc", ID: "vpc-0abc1234"}: "aws_vpc.main"}

	result, err := Add(context.Background(), tf, rootTF, staging, root, []rootfile.Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs"}}, addOptions, existing)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Imported) != 1 || len(result.Gate) == 0 {
		t.Errorf("result: %+v", result)
	}
	if got := readFile(t, root, incremental.AddedFileName(2)); !strings.Contains(got, `resource "aws_sqs_queue" "jobs"`) {
		t.Errorf("%s:\n%s", incremental.AddedFileName(2), got)
	}
	if imports := readFile(t, root, rootfile.ImportsFileName); !strings.Contains(imports, "to = aws_sqs_queue.jobs") || !strings.Contains(imports, "to = module.logs.aws_s3_bucket.this[0]") {
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

	result, err := Add(context.Background(), tf, rootTF, staging, root, []rootfile.Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs"}}, opts, nil)
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
// the added configuration sensitive, which only staging's plan knew; its
// secrets check fails then, and the root is left as it was.
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
	spy := gate.Scanner{Name: "spy", Run: func(_ context.Context, dir string) ([]gate.Finding, error) {
		ran = append(ran, dir)
		return nil, nil
	}}
	opts := addOptions
	opts.Scanners = []gate.Scanner{spy}
	before, err := rootFileContents(root)
	if err != nil {
		t.Fatal(err)
	}

	_, err = Add(context.Background(), tf, rootTF, staging, root, []rootfile.Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs"}}, opts, nil)
	if err == nil || !strings.Contains(err.Error(), gate.CheckSecrets+" failed") {
		t.Errorf("err = %v, want the secrets check failed", err)
	}

	if len(ran) > 0 {
		t.Errorf("a scanner ran on %v, holding staging's secret", ran)
	}
	after, err := rootFileContents(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("the root isn't left as it was: %v", slices.Sorted(maps.Keys(after)))
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
	existing := map[run.External]string{{Type: "aws_vpc", ID: "vpc-0abc1234"}: "aws_vpc.main"}

	result, err := Add(context.Background(), tf, rootTF, staging, root, []rootfile.Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs"}}, addOptions, existing)
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

	_, err := Add(context.Background(), tf, rootTF, staging, root, []rootfile.Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs"}}, addOptions, nil)
	if err == nil || !strings.Contains(err.Error(), "terraform init: no provider") {
		t.Fatalf("want the init error, got %v", err)
	}

	if after := dirFiles(t, root); !reflect.DeepEqual(after, before) {
		t.Errorf("root changed:\nbefore %v\nafter %v", before, after)
	}
}

// A root whose resources refer to local.tags: what is added refers to it
// too, and the local stays root's own.
func TestAddedResourcesReferToTheRootsTags(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeConfig(t, root, rootfile.LocalsFileName, `locals {
  tags = { env = "prod" }
}
`)
	files, err := hclfile.ConfigFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	applied, ok := incremental.AppliedTags(files, azureDefaultTags)
	if !ok || !reflect.DeepEqual(applied, map[string]string{"env": "prod"}) {
		t.Fatalf("got %v, %v", applied, ok)
	}

	staging := t.TempDir()
	writeConfig(t, staging, rootfile.GeneratedFileName, azureGenerated)
	writeConfig(t, staging, rootfile.ProvidersFileName, "provider \"azurerm\" {\n  features {}\n}\n")
	dt := azureDefaultTags
	dt.Applied = applied
	stagingWS := rootWorkspace(t, staging)
	if err := applyDefaultTags(stagingWS, dt); err != nil {
		t.Fatal(err)
	}
	flushed(t, stagingWS)
	changed, err := applyTagLift(stagingWS, dt, nil)
	if err != nil || !changed {
		t.Fatalf("got %v, %v", changed, err)
	}
	flushed(t, stagingWS)
	if generated := squashed(readFile(t, staging, rootfile.GeneratedFileName)); strings.Count(generated, "local.tags") != 3 {
		t.Errorf("generated.tf:\n%s", generated)
	}
	if providers := readFile(t, staging, rootfile.ProvidersFileName); strings.Contains(providers, "tags") {
		t.Errorf("providers.tf:\n%s", providers)
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

	_, err := Add(context.Background(), tf, rootTF, staging, root, []rootfile.Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs", File: "sqs.tf"}}, addOptions, nil)
	if err == nil {
		t.Fatal("want the init error")
	}
	if after := dirFiles(t, root); !reflect.DeepEqual(after, before) {
		t.Errorf("root changed:\nbefore %v\nafter %v", before, after)
	}
}

// A root that doesn't initialize gets its versions.tf back, without the
// provider the added resources required.
func TestAddUndoesTheProvidersItAdded(t *testing.T) {
	t.Parallel()
	root := existingRoot(t)
	writeConfig(t, root, rootfile.VersionsFileName, "terraform {\n  required_providers {\n    aws = {\n      source = \"hashicorp/aws\"\n    }\n  }\n}\n")
	before := dirFiles(t, root)
	staging := filepath.Join(t.TempDir(), "staging")
	tf := &fakeTerraform{dir: staging, generated: queueGenerated}
	rootTF := &fakeTerraform{dir: root, initErr: errors.New("no provider")}
	opts := run.Options{Config: map[string][]byte{
		rootfile.VersionsFileName:  []byte("terraform {\n  required_providers {\n    aws = {\n      source = \"hashicorp/aws\"\n    }\n    awscc = {\n      source  = \"hashicorp/awscc\"\n      version = \"~> 1.0\"\n    }\n  }\n}\n"),
		rootfile.ProvidersFileName: []byte("provider \"aws\" {\n  region = \"us-east-1\"\n}\n\nprovider \"awscc\" {\n  region = \"us-east-1\"\n}\n"),
	}}

	if _, err := Add(context.Background(), tf, rootTF, staging, root, []rootfile.Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs"}}, opts, nil); err == nil {
		t.Fatal("want the init error")
	}
	if after := dirFiles(t, root); !reflect.DeepEqual(after, before) {
		t.Errorf("root changed:\nbefore %v\nafter %v", before, after)
	}

	// Initialized, it keeps them.
	staging = filepath.Join(t.TempDir(), "staging")
	tf = &fakeTerraform{dir: staging, generated: queueGenerated}
	if _, err := Add(context.Background(), tf, &fakeTerraform{dir: root}, staging, root, []rootfile.Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs"}}, opts, nil); err != nil {
		t.Fatal(err)
	}
	if versions := squashed(readFile(t, root, rootfile.VersionsFileName)); !strings.Contains(versions, `awscc = { source = "hashicorp/awscc" version = "~> 1.0" }`) {
		t.Errorf("versions.tf:\n%s", versions)
	}
	if providers := readFile(t, root, rootfile.ProvidersFileName); strings.Count(providers, "provider ") != 2 || !strings.Contains(providers, `provider "awscc"`) {
		t.Errorf("providers.tf:\n%s", providers)
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
	if err := os.WriteFile(filepath.Join(staging, tfrun.LockFileName), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := addOptions
	opts.Config = map[string][]byte{
		rootfile.VersionsFileName:  rootfile.VersionsFile("", rootfile.Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.67"}),
		rootfile.ProvidersFileName: []byte("# providers\n"),
	}
	tf := &fakeTerraform{dir: staging, generated: queueGenerated}
	var lockAtInit, versionsAtInit string
	tf.onInit = func() {
		lockAtInit = readFile(t, staging, tfrun.LockFileName)
		versionsAtInit = readFile(t, staging, rootfile.VersionsFileName)
	}
	rootTF := &fakeTerraform{dir: root}

	if _, err := Add(context.Background(), tf, rootTF, staging, root, []rootfile.Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "jobs"}}, opts, nil); err != nil {
		t.Fatal(err)
	}

	if lockAtInit != lock {
		t.Errorf("lock at init:\n%s\nwant\n%s", lockAtInit, lock)
	}
	if !strings.Contains(versionsAtInit, `"~> 6.30"`) {
		t.Errorf("versions.tf at init doesn't accept the lock:\n%s", versionsAtInit)
	}
	if got := string(opts.Config[rootfile.VersionsFileName]); !strings.Contains(got, `"~> 6.67"`) {
		t.Errorf("the caller's options changed:\n%s", got)
	}
}
