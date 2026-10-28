// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
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
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)

	lifted, err := liftLiterals(dir, nil)
	if err != nil || !lifted {
		t.Fatalf("want a lift, got lifted=%v err=%v", lifted, err)
	}

	locals := readFile(t, dir, rootfile.LocalsFileName)
	if !strings.Contains(locals, `vpc_id = "vpc-0abc1234"`) {
		t.Errorf("locals.tf:\n%s", locals)
	}
	generated := readFile(t, dir, rootfile.GeneratedFileName)
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

// An AWS-managed policy reads better by its ARN than as a local.
func TestLiftLiteralsLeavesManagedPolicies(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, strings.Repeat(`resource "aws_iam_role_policy_attachment" "a" {
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}
`, 3))

	if lifted, err := liftLiterals(dir, nil); err != nil || lifted {
		t.Errorf("want no lift, got lifted=%v err=%v", lifted, err)
	}
}

func TestLiftLiteralsNamesAreUnique(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, strings.Repeat(`resource "aws_subnet" "a" {
  vpc_id = "vpc-0abc1234"
}
`, 3)+strings.Repeat(`resource "aws_subnet" "b" {
  vpc_id = "vpc-0def5678"
}
`, 3))

	if _, err := liftLiterals(dir, nil); err != nil {
		t.Fatal(err)
	}

	locals := readFile(t, dir, rootfile.LocalsFileName)
	for _, want := range []string{`vpc_id   = "vpc-0abc1234"`, `vpc_id_2 = "vpc-0def5678"`} {
		if !strings.Contains(locals, want) {
			t.Errorf("missing %s in:\n%s", want, locals)
		}
	}
}

func TestCommonTags(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, t.TempDir(), rootfile.GeneratedFileName, networkGenerated)
	resources, ok := taggedResources(f, awsDefaultTags, nil)
	if !ok || len(resources) != 3 {
		t.Fatalf("got %+v, %v, want the 3 resources with tags", resources, ok)
	}
	if common := commonTags(resources, awsDefaultTags.ReservedPrefix); !reflect.DeepEqual(common, map[string]string{"Project": "shop"}) {
		t.Errorf("got %v, want Project=shop", common)
	}

	tags := func(m ...map[string]string) []taggedResource {
		var list []taggedResource
		for i, set := range m {
			list = append(list, taggedResource{address: fmt.Sprintf("aws_x.r%d", i), tags: set})
		}
		return list
	}
	for name, tt := range map[string]struct {
		resources []taggedResource
		want      map[string]string
	}{
		// Most, not all: one resource's other value doesn't stop it.
		"most": {tags(map[string]string{"env": "prod"}, map[string]string{"env": "prod"}, map[string]string{"env": "dev"}), map[string]string{"env": "prod"}},
		// Resources without tags of their own don't count.
		"untagged": {tags(map[string]string{"env": "prod"}, map[string]string{"env": "prod"}, nil, map[string]string{}, map[string]string{"aws:x": "y"}), map[string]string{"env": "prod"}},
		"half":     {tags(map[string]string{"env": "prod"}, map[string]string{"env": "prod"}, map[string]string{"env": "dev"}, map[string]string{"env": "dev"}), map[string]string{}},
		"one":      {tags(map[string]string{"env": "prod"}), map[string]string{}},
	} {
		if got := commonTags(tt.resources, "aws:"); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: got %v, want %v", name, got, tt.want)
		}
	}
}

// liftTagsVerified lifts tags in dir as postProcess does, against an empty
// baseline.
func liftTagsVerified(tf *fakeTerraform, dir string) (bool, error) {
	return verify(context.Background(), tf, dir, Baseline{}, nil, func() (bool, error) { return applyTagLift(dir, awsDefaultTags, nil) },
		rootfile.GeneratedFileName, rootfile.ProvidersFileName, rootfile.LocalsFileName)
}

func liftTagsDir(t *testing.T) (string, *fakeTerraform) {
	t.Helper()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, networkGenerated)
	writeConfig(t, dir, rootfile.ProvidersFileName, awsProviders)
	return dir, &fakeTerraform{dir: dir}
}

func TestLiftTags(t *testing.T) {
	t.Parallel()
	dir, tf := liftTagsDir(t)

	kept, err := liftTagsVerified(tf, dir)
	if err != nil || !kept {
		t.Fatalf("want the lift kept, got kept=%v err=%v", kept, err)
	}

	if got := readFile(t, dir, rootfile.LocalsFileName); !strings.Contains(got, `Project = "shop"`) {
		t.Errorf("locals.tf:\n%s", got)
	}
	if got := readFile(t, dir, rootfile.ProvidersFileName); !strings.Contains(got, "default_tags {\n    tags = local.tags\n  }") {
		t.Errorf("providers.tf:\n%s", got)
	}
	generated := readFile(t, dir, rootfile.GeneratedFileName)
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
	t.Parallel()
	dir, tf := liftTagsDir(t)
	tf.plans = []fakePlan{{summary: tfrun.ChangeSummary{Change: 3}}}

	kept, err := liftTagsVerified(tf, dir)
	if err != nil || kept {
		t.Fatalf("want the lift undone, got kept=%v err=%v", kept, err)
	}

	if got := readFile(t, dir, rootfile.GeneratedFileName); got != networkGenerated {
		t.Errorf("generated.tf not restored:\n%s", got)
	}
	if got := readFile(t, dir, rootfile.ProvidersFileName); got != awsProviders {
		t.Errorf("providers.tf not restored:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, rootfile.LocalsFileName)); err == nil {
		t.Error("locals.tf not removed")
	}
}

// Without a plan to compare with, tags stay where they are.
func TestPostProcessWithoutAPlanToCompare(t *testing.T) {
	t.Parallel()
	dir, tf := liftTagsDir(t)
	tf.plans = []fakePlan{{diags: []tfjson.Diagnostic{errorAt(1, "Invalid value", "")}}}
	opts := Options{DefaultTags: &awsDefaultTags}

	_, err := postProcess(context.Background(), tf, dir, generating(opts), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(readFile(t, dir, rootfile.ProvidersFileName), "default_tags") {
		t.Error("tags lifted without a plan to compare with")
	}
	// The literal lift doesn't need one.
	if !strings.Contains(readFile(t, dir, rootfile.GeneratedFileName), "local.vpc_id") {
		t.Error("literals not lifted")
	}
}

func TestOmitArgumentsForEveryType(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeConfig(t, dir, rootfile.GeneratedFileName, `resource "aws_sqs_queue" "a" {
  name     = "a"
  region   = "us-east-1"
  tags_all = {}
}
`).Path

	changed, err := OmitArguments(path, map[string][]string{"*": {"region", "tags_all"}})
	if err != nil || !changed {
		t.Fatalf("want a change, got changed=%v err=%v", changed, err)
	}

	if got := readFile(t, dir, rootfile.GeneratedFileName); strings.Contains(got, "region") || strings.Contains(got, "tags_all") {
		t.Errorf("not left out:\n%s", got)
	}
}

// The Google provider applies labels through its default_labels argument.
func TestLiftLabelsIntoAnArgument(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	generated := `resource "google_storage_bucket" "a" {
  labels = {
    env  = "prod"
    team = "data"
  }
  location = "EU"
  name     = "a"
}

resource "google_storage_bucket" "b" {
  labels = {
    env                  = "prod"
    goog-managed-by      = "x"
  }
  location = "EU"
  name     = "b"
}
`
	for name, content := range map[string]string{rootfile.GeneratedFileName: generated, rootfile.ProvidersFileName: "provider \"google\" {\n  project = \"p\"\n}\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dt := DefaultTags{Provider: "google", Attribute: "labels", Block: "default_labels", ReservedPrefix: "goog-", Argument: true}
	changed, err := applyTagLift(dir, dt, nil)
	if err != nil || !changed {
		t.Fatalf("changed %v, %v", changed, err)
	}
	if got := readFile(t, dir, rootfile.ProvidersFileName); !strings.Contains(got, "default_labels = local.labels") || strings.Contains(got, "default_labels {") {
		t.Errorf("providers.tf:\n%s", got)
	}
	if got := readFile(t, dir, rootfile.LocalsFileName); !strings.Contains(got, `env = "prod"`) {
		t.Errorf("locals.tf:\n%s", got)
	}
	if got := readFile(t, dir, rootfile.GeneratedFileName); strings.Contains(got, `env`) || !strings.Contains(got, `team`) || !strings.Contains(got, "goog-managed-by") {
		t.Errorf("generated.tf:\n%s", got)
	}

	// An incremental import reads them back.
	files := []*hclfile.File{}
	for _, name := range []string{rootfile.ProvidersFileName, rootfile.LocalsFileName} {
		f, err := hclfile.Load(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	if tags, ok := appliedTags(files, dt); !ok || tags["env"] != "prod" || len(tags) != 1 {
		t.Errorf("applied labels: %v, %v", tags, ok)
	}
}

const azureGenerated = `resource "azurerm_resource_group" "app" {
  location = "westeurope"
  name     = "app"
  tags = {
    env  = "prod"
    team = "web"
  }
}

resource "azurerm_virtual_network" "app" {
  address_space       = ["10.0.0.0/16"]
  location            = "westeurope"
  name                = "app"
  resource_group_name = "app"
  tags = {
    env = "prod"
  }
}

resource "azurerm_network_security_group" "app" {
  location            = "westeurope"
  name                = "app"
  resource_group_name = "app"
  tags = {
    env                 = "prod"
    "hidden-link:/apps" = "Resource"
  }
}

resource "azurerm_subnet" "app" {
  address_prefixes     = ["10.0.1.0/24"]
  name                 = "app"
  resource_group_name  = "app"
  virtual_network_name = "app"
}
`

var azureDefaultTags = DefaultTags{Provider: "azurerm", Attribute: "tags", ReservedPrefix: "hidden-"}

// azurerm has no provider-wide tags: the resources refer to the local, and
// module calls copy their tags expression.
func TestLiftTagsIntoALocalTheResourcesReferTo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, azureGenerated)

	changed, err := applyTagLift(dir, azureDefaultTags, nil)
	if err != nil || !changed {
		t.Fatalf("changed %v, %v", changed, err)
	}

	if got := squashed(readFile(t, dir, rootfile.LocalsFileName)); !strings.Contains(got, `tags = { env = "prod" }`) {
		t.Errorf("locals.tf:\n%s", got)
	}
	generated := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	for _, want := range []string{
		`tags = merge(local.tags, { team = "web" })`,
		`resource_group_name = "app" tags = local.tags }`,
		`tags = merge(local.tags, { "hidden-link:/apps" = "Resource" })`,
	} {
		if !strings.Contains(generated, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, generated)
		}
	}
	if strings.Count(generated, "tags =") != 3 {
		t.Errorf("the untagged subnet got tags:\n%s", generated)
	}
	if _, err := os.Stat(filepath.Join(dir, rootfile.ProvidersFileName)); err == nil {
		t.Error("providers.tf written")
	}
}

// Tags the cloud sets itself are never the shared ones.
func TestLiftTagsLeavesReservedTags(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "azurerm_resource_group" "a" {
  location = "westeurope"
  name     = "a"
  tags     = { "hidden-title" = "A" }
}

resource "azurerm_resource_group" "b" {
  location = "westeurope"
  name     = "b"
  tags     = { "hidden-title" = "A" }
}
`)

	changed, err := applyTagLift(dir, azureDefaultTags, nil)
	if err != nil || changed {
		t.Fatalf("want nothing lifted, got %v, %v", changed, err)
	}
}

// taggedSchemas describe types the AWS provider tags, with tags and
// tags_all, and one it doesn't.
var taggedSchemas = &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
	"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
		"aws_subnet": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
			"tags":     {AttributeType: cty.Map(cty.String), Optional: true},
			"tags_all": {AttributeType: cty.Map(cty.String), Optional: true, Computed: true},
		}}},
		"aws_iam_user": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
			"name":     {AttributeType: cty.String, Required: true},
			"tags":     {AttributeType: cty.Map(cty.String), Optional: true},
			"tags_all": {AttributeType: cty.Map(cty.String), Optional: true, Computed: true},
		}}},
		"aws_route_table_association": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
			"subnet_id": {AttributeType: cty.String, Optional: true},
		}}},
	}},
}}

// The tags most resources share go to default_tags even when some
// resources lack them, such as an untagged user, which default_tags would
// tag: those use a configuration of the provider without default_tags.
// A resource's own value of a shared tag stays, over the provider's.
func TestLiftTagsMostResourcesShare(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "aws_iam_user" "ci" {
  name = "ci"
}

resource "aws_route_table_association" "a" {
  subnet_id = "subnet-1"
}

resource "aws_subnet" "a" {
  tags = {
    Project = "shop"
    Team    = "web"
  }
}

resource "aws_subnet" "b" {
  tags = {
    Project = "shop"
    Team    = "web"
  }
}

resource "aws_subnet" "c" {
  tags = {
    Project = "legacy"
    Team    = "web"
  }
}
`)
	writeConfig(t, dir, rootfile.ProvidersFileName, awsProviders)

	changed, err := applyTagLift(dir, awsDefaultTags, taggedSchemas)
	if err != nil || !changed {
		t.Fatalf("changed %v, %v", changed, err)
	}
	if got := squashed(readFile(t, dir, rootfile.LocalsFileName)); !strings.Contains(got, `Project = "shop"`) || !strings.Contains(got, `Team = "web"`) {
		t.Errorf("locals.tf:\n%s", got)
	}
	providers := squashed(readFile(t, dir, rootfile.ProvidersFileName))
	for _, want := range []string{
		`provider "aws" { region = "us-east-1" default_tags { tags = local.tags } }`,
		`provider "aws" { alias = "without_default_tags" region = "us-east-1" }`,
	} {
		if !strings.Contains(providers, want) {
			t.Errorf("providers.tf misses %q:\n%s", want, providers)
		}
	}
	generated := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	for _, want := range []string{
		`name = "ci" provider = aws.without_default_tags }`,
		`resource "aws_subnet" "c" { tags = { Project = "legacy" } }`,
		`resource "aws_subnet" "a" { }`,
		`subnet_id = "subnet-1" }`,
	} {
		if !strings.Contains(generated, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, generated)
		}
	}
	if strings.Count(generated, "provider =") != 1 {
		t.Errorf("only the user uses the other configuration:\n%s", generated)
	}

	// An incremental import reads the default configuration's tags.
	files := []*hclfile.File{}
	for _, name := range []string{rootfile.ProvidersFileName, rootfile.LocalsFileName} {
		f, err := hclfile.Load(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	if tags, ok := appliedTags(files, awsDefaultTags); !ok || len(tags) != 2 {
		t.Errorf("applied tags: %v, %v", tags, ok)
	}
}

// Where the plan rejects default_tags, postProcess lifts the tags as for a
// provider without them: the resources that have the shared tags refer to
// the local, and the others keep theirs.
func TestLiftTagsIntoTheLocalAsTheFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "aws_iam_user" "a" {
  name = "a"
}

resource "aws_iam_user" "b" {
  name = "b"
}

resource "aws_subnet" "a" {
  tags = {
    Project = "shop"
  }
}

resource "aws_subnet" "b" {
  tags = {
    Project = "shop"
    Team    = "web"
  }
}
`)
	writeConfig(t, dir, rootfile.ProvidersFileName, awsProviders)

	local := awsDefaultTags
	local.Block = ""
	changed, err := applyTagLift(dir, local, taggedSchemas)
	if err != nil || !changed {
		t.Fatalf("changed %v, %v", changed, err)
	}
	if got := readFile(t, dir, rootfile.ProvidersFileName); got != awsProviders {
		t.Errorf("providers.tf changed:\n%s", got)
	}
	generated := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	for _, want := range []string{`tags = local.tags`, `tags = merge(local.tags, { Team = "web" })`} {
		if !strings.Contains(generated, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, generated)
		}
	}
	if strings.Contains(generated, "provider =") {
		t.Errorf("generated.tf:\n%s", generated)
	}
}
