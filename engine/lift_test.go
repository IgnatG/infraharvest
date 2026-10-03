// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

const networkGenerated = `resource "aws_subnet" "a" {
  cidr_block = "10.0.1.0/24"
  tags = {
    Project = "shop"
    Team    = "web"
    "aws:cloudformation:stack-name" = "net"
  }
  vpc_id = "vpc-0abc1234"
}

resource "aws_subnet" "b" {
  cidr_block = "10.0.2.0/24"
  tags = {
    Project = "shop"
    Team    = "data"
    "aws:cloudformation:stack-name" = "net"
  }
  vpc_id = "vpc-0abc1234"
}

resource "aws_security_group" "web" {
  description = "Enabled"
  name        = "web"
  tags = {
    Project = "shop"
  }
  vpc_id = "vpc-0abc1234"
}

resource "aws_route_table" "main" {
  vpc_id = "vpc-0def5678"
}
`

const awsProviders = `provider "aws" {
  region = "us-east-1"
}
`

var awsDefaultTags = DefaultTags{Provider: "aws", Attribute: "tags", Block: "default_tags", ReservedPrefix: "aws:"}

func TestLiftLiterals(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, networkGenerated)

	lifted, err := liftLiterals(dir)
	if err != nil || !lifted {
		t.Fatalf("want a lift, got lifted=%v err=%v", lifted, err)
	}

	locals := readFile(t, dir, LocalsFileName)
	if !strings.Contains(locals, `vpc_id = "vpc-0abc1234"`) {
		t.Errorf("locals.tf:\n%s", locals)
	}
	generated := readFile(t, dir, GeneratedFileName)
	if strings.Count(generated, "vpc_id = local.vpc_id") != 3 {
		t.Errorf("want 3 uses of local.vpc_id:\n%s", generated)
	}
	// Used once, or not an identifier: left alone.
	for _, kept := range []string{`vpc_id = "vpc-0def5678"`, `description = "Enabled"`} {
		if !strings.Contains(generated, kept) {
			t.Errorf("%s changed:\n%s", kept, generated)
		}
	}
}

func TestLiftLiteralsNamesAreUnique(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, strings.Repeat(`resource "aws_subnet" "a" {
  vpc_id = "vpc-0abc1234"
}
`, 3)+strings.Repeat(`resource "aws_subnet" "b" {
  vpc_id = "vpc-0def5678"
}
`, 3))

	if _, err := liftLiterals(dir); err != nil {
		t.Fatal(err)
	}

	locals := readFile(t, dir, LocalsFileName)
	for _, want := range []string{`vpc_id   = "vpc-0abc1234"`, `vpc_id_2 = "vpc-0def5678"`} {
		if !strings.Contains(locals, want) {
			t.Errorf("missing %s in:\n%s", want, locals)
		}
	}
}

func TestSharedTags(t *testing.T) {
	f := writeConfig(t, t.TempDir(), GeneratedFileName, networkGenerated)

	common, tagged := sharedTags(f, awsDefaultTags)

	if tagged != 3 || len(common) != 1 || common["Project"] != "shop" {
		t.Errorf("got %v from %d resources, want Project=shop from 3", common, tagged)
	}
}

func liftTagsDir(t *testing.T) (string, *fakeTerraform) {
	t.Helper()
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, networkGenerated)
	writeConfig(t, dir, ProvidersFileName, awsProviders)
	return dir, &fakeTerraform{dir: dir}
}

func TestLiftTags(t *testing.T) {
	dir, tf := liftTagsDir(t)

	kept, err := liftTags(context.Background(), tf, dir, awsDefaultTags, changeSummary{})
	if err != nil || !kept {
		t.Fatalf("want the lift kept, got kept=%v err=%v", kept, err)
	}

	if got := readFile(t, dir, LocalsFileName); !strings.Contains(got, `Project = "shop"`) {
		t.Errorf("locals.tf:\n%s", got)
	}
	if got := readFile(t, dir, ProvidersFileName); !strings.Contains(got, "default_tags {\n    tags = local.tags\n  }") {
		t.Errorf("providers.tf:\n%s", got)
	}
	generated := readFile(t, dir, GeneratedFileName)
	if strings.Contains(generated, `Project = "shop"`) {
		t.Errorf("shared tag left on resources:\n%s", generated)
	}
	// Other tags stay; the security group had only the shared one.
	for _, kept := range []string{`Team`, `"aws:cloudformation:stack-name" = "net"`} {
		if !strings.Contains(generated, kept) {
			t.Errorf("%s removed:\n%s", kept, generated)
		}
	}
	if strings.Count(generated, "tags = {") != 2 {
		t.Errorf("want tags only on the subnets:\n%s", generated)
	}
}

// AWS records every tag in tags_all, so moving tags can change a plan: a
// lift that does is undone.
func TestLiftTagsRevertsWhenThePlanChanges(t *testing.T) {
	dir, tf := liftTagsDir(t)
	tf.plans = []fakePlan{{summary: changeSummary{Change: 3}}}

	kept, err := liftTags(context.Background(), tf, dir, awsDefaultTags, changeSummary{})
	if err != nil || kept {
		t.Fatalf("want the lift undone, got kept=%v err=%v", kept, err)
	}

	if got := readFile(t, dir, GeneratedFileName); got != networkGenerated {
		t.Errorf("generated.tf not restored:\n%s", got)
	}
	if got := readFile(t, dir, ProvidersFileName); got != awsProviders {
		t.Errorf("providers.tf not restored:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, LocalsFileName)); err == nil {
		t.Error("locals.tf not removed")
	}
}

// With secret values left, the plan can't be compared, so tags stay.
func TestLiftSkipsTagsWithoutAPlanToCompare(t *testing.T) {
	dir, tf := liftTagsDir(t)
	opts := Options{DefaultTags: &awsDefaultTags}

	err := lift(context.Background(), tf, dir, opts, []tfjson.Diagnostic{errorAt(1, "secret", "")}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(readFile(t, dir, ProvidersFileName), "default_tags") {
		t.Error("tags lifted without a plan to compare with")
	}
	// The literal lift doesn't need one.
	if !strings.Contains(readFile(t, dir, GeneratedFileName), "local.vpc_id") {
		t.Error("literals not lifted")
	}
}

func TestOmitArgumentsForEveryType(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, GeneratedFileName, `resource "aws_sqs_queue" "a" {
  name     = "a"
  region   = "us-east-1"
  tags_all = {}
}
`).path

	changed, err := omitArguments(path, map[string][]string{"*": {"region", "tags_all"}})
	if err != nil || !changed {
		t.Fatalf("want a change, got changed=%v err=%v", changed, err)
	}

	if got := readFile(t, dir, GeneratedFileName); strings.Contains(got, "region") || strings.Contains(got, "tags_all") {
		t.Errorf("not left out:\n%s", got)
	}
}
