// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
)

// writeRootDir writes the files of a finished AWS root of environment
// env, whose names hold token, in account, into a new directory, and
// returns it: a queue whose visibility timeout is timeout, named through
// a local, a bucket, the provider's default tags from local.tags, a data
// source, and more, more resources.
func writeRootDir(t *testing.T, token, account, timeout, more string) string {
	t.Helper()
	dir := t.TempDir()
	imports := `import {
  to = aws_sqs_queue.orders
  id = "https://sqs.eu-west-2.amazonaws.com/` + account + `/shop-` + token + `-orders"
}

import {
  to = aws_s3_bucket.logs
  id = "shop-` + token + `-logs"
}
`
	files := map[string]string{
		VersionsFileName: `terraform {
  required_version = ">= 1.5, < 2.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.14"
    }
  }
}
`,
		ProvidersFileName: `provider "aws" {
  region              = "eu-west-2"
  allowed_account_ids = ["` + account + `"]

  assume_role {
    role_arn = "arn:aws:iam::` + account + `:role/deploy"
  }

  default_tags {
    tags = local.tags
  }
}
`,
		LocalsFileName: `locals {
  tags = {
    Environment = "` + token + `"
    Project     = "shop"
  }
  orders_name = "shop-` + token + `-orders"
}
`,
		"sqs.tf": `resource "aws_sqs_queue" "orders" {
  name                       = local.orders_name
  visibility_timeout_seconds = ` + timeout + `
}
` + more,
		"s3.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "shop-` + token + `-logs"
  tags = {
    Network = data.aws_vpc.shared.id
  }
}
`,
		DataFileName: `data "aws_vpc" "shared" {
  id = "vpc-0123456789abcdef0"
}
`,
		ImportsFileName: imports,
		LockFileName:    "# lock\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// mergeFake is a Terraform runner of one environment that plans the
// merged root in dir as Terraform would: each import the environment's
// imports.tfvars names, with no change, but for those of changed, which
// its next plans update, as many as each says.
type mergeFake struct {
	env, dir string
	changed  map[string]int
	// overrides are what the credentials override file held at each plan.
	overrides *[]string
}

func (f *mergeFake) Init(context.Context, ...tfexec.InitOption) error { return nil }

func (f *mergeFake) PlanJSON(_ context.Context, w io.Writer, _ ...tfexec.PlanOption) (bool, error) {
	content, _ := os.ReadFile(filepath.Join(f.dir, credentialsOverrideFileName))
	*f.overrides = append(*f.overrides, f.env+":"+string(content))
	_, err := io.WriteString(w, `{"type":"change_summary","changes":{"add":0,"change":0,"remove":0,"import":0}}`+"\n")
	return true, err
}

func (f *mergeFake) Validate(context.Context) (*tfjson.ValidateOutput, error) {
	return &tfjson.ValidateOutput{Valid: true}, nil
}

func (f *mergeFake) ProvidersSchema(context.Context) (*tfjson.ProviderSchemas, error) {
	return rootSchemas, nil
}

func (f *mergeFake) ShowPlanFile(context.Context, string, ...tfexec.ShowOption) (*tfjson.Plan, error) {
	path := filepath.Join(f.dir, varsDirName, f.env, importVarsFileName)
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	file, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, diags
	}
	ids, diags := file.Body.(*hclsyntax.Body).Attributes[importIDsVariableName].Expr.Value(nil)
	if diags.HasErrors() {
		return nil, diags
	}
	p := &tfjson.Plan{}
	for it := ids.ElementIterator(); it.Next(); {
		address, id := it.Element()
		change := &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionNoop}, Importing: &tfjson.Importing{ID: id.AsString()}}
		if f.changed[address.AsString()] > 0 {
			f.changed[address.AsString()]--
			change.Actions = tfjson.Actions{tfjson.ActionUpdate}
			change.Before = map[string]interface{}{"visibility_timeout_seconds": 30}
			change.After = map[string]interface{}{"visibility_timeout_seconds": 60}
		}
		p.ResourceChanges = append(p.ResourceChanges, &tfjson.ResourceChange{Address: address.AsString(), Mode: tfjson.ManagedResourceMode, Change: change})
	}
	return p, nil
}

func (f *mergeFake) FormatCheck(context.Context, ...tfexec.FormatOption) (bool, []string, error) {
	return true, nil, nil
}

func (f *mergeFake) SetStdout(io.Writer) {}

// mergeRoots returns the dev and prod roots of a stack, written by
// writeRootDir, to merge into dir, each planning with a mergeFake that
// changes what changed says, by environment.
func mergeRoots(t *testing.T, dir string, changed map[string]map[string]int, overrides *[]string) []MergeRoot {
	t.Helper()
	root := func(env, token, account, timeout string) MergeRoot {
		d := writeRootDir(t, token, account, timeout, "")
		fake := &mergeFake{env: env, dir: dir, changed: changed[env], overrides: overrides}
		return MergeRoot{
			Env: env, Tokens: []string{token}, Scope: account, Region: "eu-west-2", Dir: d,
			Provider: Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.14", Config: map[string]interface{}{
				"region":              "eu-west-2",
				"allowed_account_ids": []string{account},
				"assume_role":         map[string]interface{}{"role_arn": "arn:aws:iam::" + account + ":role/deploy"},
			}},
			Terraform: func(string) (Terraform, error) { return fake, nil },
		}
	}
	return []MergeRoot{root("dev", "dev", "111111111111", "30"), root("prod", "prd", "222222222222", "60")}
}

func TestReadEnvRoot(t *testing.T) {
	t.Parallel()
	dir := writeRootDir(t, "prd", "222222222222", "60", "")
	root, reason, err := readEnvRoot(MergeRoot{Env: "prod", Tokens: []string{"prd"}, Dir: dir, Provider: Provider{Name: "aws"}}, t.TempDir())
	if err != nil || reason != "" {
		t.Fatalf("readEnvRoot: %v %s", err, reason)
	}
	if got := root.importIDs["aws_s3_bucket.logs"]; got != "shop-prd-logs" {
		t.Errorf("import ID of aws_s3_bucket.logs: %q", got)
	}
	if root.files["aws_sqs_queue.orders"] != "sqs.tf" || root.files["aws_s3_bucket.logs"] != "s3.tf" {
		t.Errorf("files: %v", root.files)
	}
	if root.requiredVersion != ">= 1.5, < 2.0" {
		t.Errorf("required_version: %q", root.requiredVersion)
	}
	if root.defaultTags["Environment"] != "prd" || root.defaultTags["Project"] != "shop" {
		t.Errorf("default tags: %v", root.defaultTags)
	}
	// The local is put in: merging compares values.
	if got := string(root.file.file.Bytes()); !strings.Contains(got, `name                       = "shop-prd-orders"`) {
		t.Errorf("the queue's name isn't the local's value:\n%s", got)
	}
	if blocks := dataBlocksOf(root.data); len(blocks) != 1 || blocks[0].address != "aws_vpc.shared" {
		t.Errorf("data sources: %+v", blocks)
	}
	if string(root.backend) != "" {
		t.Errorf("backend: %q", root.backend)
	}

	for name, content := range map[string]string{
		MovesFileName: "moved {\n  from = aws_sqs_queue.old\n  to   = aws_sqs_queue.orders\n}\n",
		"outputs.tf":  "output \"queue\" {\n  value = aws_sqs_queue.orders.id\n}\n",
	} {
		other := writeRootDir(t, "prd", "222222222222", "60", "")
		if err := os.WriteFile(filepath.Join(other, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, reason, err := readEnvRoot(MergeRoot{Env: "prod", Dir: other, Provider: Provider{Name: "aws"}}, t.TempDir()); err != nil || reason == "" {
			t.Errorf("%s: want a reason not to merge, got %q %v", name, reason, err)
		}
	}
}

func TestReadEnvRootModuleSources(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	origin := filepath.Join(out, "envs", "dev", "aws", "111111111111", "eu-west-2")
	premerge := filepath.Join(out, ".infraharvest", "premerge", "envs", "dev", "aws", "111111111111", "eu-west-2")
	if err := os.MkdirAll(premerge, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(premerge, "main.tf"), []byte(`module "queue" {
  source = "../../../../../modules/sqs_queue_0123abcd"
  name   = "shop-dev-orders"
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	root, _, err := readEnvRoot(MergeRoot{Env: "dev", Dir: premerge, Origin: origin, Provider: Provider{Name: "aws"}}, filepath.Join(out, "aws", "eu-west-2"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(root.file.file.Bytes()); !strings.Contains(got, `"../../modules/sqs_queue_0123abcd"`) {
		t.Errorf("the module's source isn't relative to the merged root:\n%s", got)
	}
}

func TestMergeRoots(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "aws", "eu-west-2")
	var overrides []string
	result, err := MergeRoots(context.Background(), dir, mergeRoots(t, dir, nil, &overrides), MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != "" || !result.Passed() {
		t.Fatalf("not merged: %s %+v", result.Reason, result.Environments)
	}
	if result.Shared != 2 || len(result.Residue) != 0 {
		t.Errorf("shared %d, residue %+v", result.Shared, result.Residue)
	}
	for _, name := range []string{ProvidersFileName, VariablesFileName, ImportsFileName, "sqs.tf", "s3.tf", DataFileName, LockFileName, "vars/dev/terraform.tfvars", "vars/prod/imports.tfvars"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, credentialsOverrideFileName)); err == nil {
		t.Errorf("the credentials override is left behind")
	}
	if len(overrides) != 2 || !strings.Contains(overrides[0], "arn:aws:iam::111111111111:role/deploy") || !strings.Contains(overrides[1], "arn:aws:iam::222222222222:role/deploy") {
		t.Errorf("each environment plans with its own role: %q", overrides)
	}
	providers := readFile(t, dir, ProvidersFileName)
	for _, want := range []string{"region              = var.aws_region", "Environment = var.env", `Project     = "shop"`} {
		if !strings.Contains(providers, want) {
			t.Errorf("providers.tf misses %q:\n%s", want, providers)
		}
	}
	if strings.Contains(providers, "assume_role") {
		t.Errorf("providers.tf assumes a role:\n%s", providers)
	}
	if data := readFile(t, dir, DataFileName); !strings.Contains(data, `id = "vpc-0123456789abcdef0"`) {
		t.Errorf("data.tf misses the roots' data source:\n%s", data)
	}
	if got := result.Environments[1].Imports["aws_sqs_queue.orders"]; got != "https://sqs.eu-west-2.amazonaws.com/222222222222/shop-prd-orders" {
		t.Errorf("prod's import of the queue: %q", got)
	}
}

// stateCounter counts the plans of a Terraform runner, and those given a
// state file (-state).
type stateCounter struct {
	Terraform
	plans, stateless *int
}

func (c *stateCounter) PlanJSON(ctx context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	*c.plans++
	for _, opt := range opts {
		if _, ok := opt.(*tfexec.StateOption); ok {
			*c.stateless++
		}
	}
	return c.Terraform.PlanJSON(ctx, w, opts...)
}

// The merged root's plans read no state, such as the workspace states of
// environments applied without a backend: each plans with an empty state
// outside the root, removed after, and the root's states stay as they
// were.
func TestMergeRootsPlansWithoutLocalState(t *testing.T) {
	temp := t.TempDir()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, temp)
	}
	dir := filepath.Join(t.TempDir(), "aws", "eu-west-2")
	states := map[string]string{"terraform.tfstate": "default", "terraform.tfstate.d/dev/terraform.tfstate": "dev"}
	for name, content := range states {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var overrides []string
	roots := mergeRoots(t, dir, nil, &overrides)
	plans, stateless := 0, 0
	for i := range roots {
		next := roots[i].Terraform
		roots[i].Terraform = func(d string) (Terraform, error) {
			tf, err := next(d)
			return &stateCounter{Terraform: tf, plans: &plans, stateless: &stateless}, err
		}
	}

	result, err := MergeRoots(context.Background(), dir, roots, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if result.Reason != "" {
		t.Fatalf("not merged: %s", result.Reason)
	}
	if plans == 0 || stateless != plans {
		t.Errorf("%d of %d plans were given a state, want all", stateless, plans)
	}
	if left, err := filepath.Glob(filepath.Join(temp, emptyStatePattern+"*")); err != nil || len(left) > 0 {
		t.Errorf("empty states left: %v, %v", left, err)
	}
	for name, want := range states {
		if got := readFile(t, dir, filepath.FromSlash(name)); got != want {
			t.Errorf("%s: %q, want it as it was", name, got)
		}
	}
}

func TestMergeRootsResidue(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "aws", "eu-west-2")
	var overrides []string
	// prod's first plan of the shared queue would update it.
	changed := map[string]map[string]int{"prod": {"aws_sqs_queue.orders": 1}}
	result, err := MergeRoots(context.Background(), dir, mergeRoots(t, dir, changed, &overrides), MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != "" {
		t.Fatalf("not merged: %s", result.Reason)
	}
	if len(result.Residue) != 2 {
		t.Fatalf("residue: %+v", result.Residue)
	}
	for _, res := range result.Residue {
		if res.Address != "aws_sqs_queue.orders" || !strings.Contains(res.Reason, "prod") {
			t.Errorf("residue %+v", res)
		}
	}
	if got := readFile(t, dir, "vars/prod/imports.tfvars"); !strings.Contains(got, `"aws_sqs_queue.orders_prod[0]"`) {
		t.Errorf("prod imports its own queue:\n%s", got)
	}
}

func TestMergeRootsFallBack(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "aws", "eu-west-2")
	var overrides []string
	// prod's plans of the queue always update it.
	changed := map[string]map[string]int{"prod": {"aws_sqs_queue.orders": 9, "aws_sqs_queue.orders_prod[0]": 9}}
	result, err := MergeRoots(context.Background(), dir, mergeRoots(t, dir, changed, &overrides), MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason == "" || !strings.Contains(result.Reason, "orders_prod") {
		t.Fatalf("want the stack kept apart, with why: %+v", result)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Errorf("%s is left behind", dir)
	}
}

func TestMergeRootsRefusesDataOfOneEnvironment(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "aws", "eu-west-2")
	var overrides []string
	roots := mergeRoots(t, dir, nil, &overrides)
	if err := os.WriteFile(filepath.Join(roots[1].Dir, DataFileName), []byte("data \"aws_vpc\" \"shared\" {\n  id = \"vpc-0123456789abcdef0\"\n}\n\ndata \"aws_vpc\" \"prod\" {\n  id = \"vpc-0fedcba9876543210\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := MergeRoots(context.Background(), dir, roots, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Reason, "data.aws_vpc.prod") {
		t.Errorf("reason: %q", result.Reason)
	}
}

func TestReplaceLocals(t *testing.T) {
	t.Parallel()
	tokens := exprTokens(`merge(local.tags, { Name = local.name }, local.other)`)
	got := string(replaceLocals(tokens, map[string]cty.Value{"tags": cty.MapVal(map[string]cty.Value{"a": cty.StringVal("b")}), "name": cty.StringVal("x")}).Bytes())
	if !strings.Contains(got, `"x"`) || !strings.Contains(got, "local.other") || strings.Contains(got, "local.tags") {
		t.Errorf("got %s", got)
	}
}

// A resource an incremental import added since the stack merged stays
// its environment's, matched with no other's, and what the merged root
// had keeps its address.
func TestMergeStackIsolated(t *testing.T) {
	t.Parallel()
	queue := func(env, name string) string {
		return "resource \"aws_sqs_queue\" \"" + name + "\" {\n  name = \"shop-" + env + "-" + name + "\"\n}\n"
	}
	ids := func(env string, names ...string) map[string]string {
		m := map[string]string{}
		for _, n := range names {
			m["aws_sqs_queue."+n] = "https://sqs.eu-west-2.amazonaws.com/1/shop-" + env + "-" + n
		}
		return m
	}
	dev := testEnvRoot(t, "dev", nil, "111111111111", queue("dev", "orders")+queue("dev", "audit"), ids("dev", "orders", "audit"))
	prod := testEnvRoot(t, "prod", []string{"prd"}, "222222222222", queue("prd", "orders")+queue("prd", "audit"), ids("prd", "orders", "audit"))
	// Without isolation, the audit queues match.
	if m := mergeStack("aws", []envRoot{dev, prod}, envSchemas); len(m.resources) != 2 {
		t.Fatalf("resources: %d", len(m.resources))
	}
	m := mergeStackWith("aws", []envRoot{dev, prod}, envSchemas, &mergeControls{isolated: map[string]bool{mergeKey(1, "aws_sqs_queue.audit"): true}})
	addresses := map[string]string{}
	for _, r := range m.resources {
		for e, member := range r.members {
			if member != nil {
				addresses[m.envs[e]+" "+member.block.address] = r.address() + " " + r.residue
			}
		}
	}
	if got := addresses["prod aws_sqs_queue.audit"]; !strings.HasPrefix(got, "aws_sqs_queue.audit_prod ") || !strings.Contains(got, "incremental") {
		t.Errorf("prod's new queue: %q", got)
	}
	if got := addresses["dev aws_sqs_queue.audit"]; !strings.HasPrefix(got, "aws_sqs_queue.audit ") {
		t.Errorf("dev's queue: %q", got)
	}
	if got := addresses["prod aws_sqs_queue.orders"]; got != "aws_sqs_queue.orders " {
		t.Errorf("the shared queue: %q", got)
	}
}

func TestMergedImportIDs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if envs, err := MergedEnvironments(dir); err != nil || len(envs) != 0 {
		t.Errorf("a root of its own: %v %v", envs, err)
	}
	for _, env := range []string{"dev", "prod"} {
		if err := os.MkdirAll(filepath.Join(dir, varsDirName, env), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, varsDirName, env, envVarsFileName), []byte("env = \""+env+"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, varsDirName, "prod", importVarsFileName), []byte(`import_ids = {
  "aws_sqs_queue.orders"            = "https://sqs.eu-west-2.amazonaws.com/2/shop-prd-orders"
  "aws_sqs_queue.this[\"refunds\"]" = "https://sqs.eu-west-2.amazonaws.com/2/shop-prd-refunds"
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	envs, err := MergedEnvironments(dir)
	if err != nil || strings.Join(envs, ",") != "dev,prod" {
		t.Fatalf("environments: %v %v", envs, err)
	}
	ids, err := MergedImportIDs(dir, "prod")
	if err != nil || len(ids) != 2 || ids[`aws_sqs_queue.this["refunds"]`] != "https://sqs.eu-west-2.amazonaws.com/2/shop-prd-refunds" {
		t.Errorf("prod's import IDs: %v %v", ids, err)
	}
	if ids, err := MergedImportIDs(dir, "dev"); err != nil || ids != nil {
		t.Errorf("dev, without imports.tfvars: %v %v", ids, err)
	}
	if got := AddressType(`module.logs.aws_s3_bucket.this[0]`); got != "aws_s3_bucket" {
		t.Errorf("AddressType: %q", got)
	}
}

// VerifyMerged plans a merged root as one environment, with its var
// files, and expects its imports.
func TestVerifyMerged(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "aws", "eu-west-2")
	var overrides []string
	if result, err := MergeRoots(context.Background(), dir, mergeRoots(t, dir, nil, &overrides), MergeOptions{}); err != nil || result.Reason != "" {
		t.Fatalf("merge: %v %+v", err, result)
	}
	gate, err := VerifyMerged(context.Background(), &mergeFake{env: "prod", dir: dir, overrides: &overrides}, dir, "prod", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !gate.Passed() {
		t.Errorf("gate: %+v", gate)
	}
	// Once applied, imports.tf goes, and nothing is imported.
	if err := os.Remove(filepath.Join(dir, ImportsFileName)); err != nil {
		t.Fatal(err)
	}
	gate, err = VerifyMerged(context.Background(), &mergeFake{env: "prod", dir: dir, overrides: &overrides}, dir, "prod", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if plan := gate.check(CheckPlan); plan.Passed {
		t.Errorf("a plan that imports what imports.tf no longer has passes: %+v", plan)
	}
}

// TestMergeRootsUnplanned merges a stack of which the run has dev's root
// only as an earlier import set it aside, with no Terraform runner, and
// prod's provider as RootProvider reads it from its root: prod runs the
// whole gate, and dev is merged, but not planned.
func TestMergeRootsUnplanned(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "aws", "eu-west-2")
	var overrides []string
	roots := mergeRoots(t, dir, nil, &overrides)
	roots[0].Terraform = nil
	provider, err := RootProvider(roots[1].Dir, "aws")
	if err != nil {
		t.Fatal(err)
	}
	roots[1].Provider = provider
	result, err := MergeRoots(context.Background(), dir, roots, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != "" || !result.Passed() {
		t.Fatalf("not merged: %s %+v", result.Reason, result.Environments)
	}
	if !result.Environments[0].Unplanned || result.Environments[1].Unplanned {
		t.Errorf("unplanned: dev %v, prod %v", result.Environments[0].Unplanned, result.Environments[1].Unplanned)
	}
	if g := result.Environments[0].Gate; len(g) != 1 || !g[0].Skipped || !strings.Contains(strings.Join(g[0].Details, " "), "verify --environment dev") {
		t.Errorf("dev's gate: %+v", g)
	}
	if len(overrides) != 1 || !strings.Contains(overrides[0], "arn:aws:iam::222222222222:role/deploy") {
		t.Errorf("only prod plans, with its own role: %q", overrides)
	}
	if got := readFile(t, dir, "vars/dev/imports.tfvars"); !strings.Contains(got, "shop-dev-orders") {
		t.Errorf("dev's imports.tfvars:\n%s", got)
	}

	roots[1].Terraform = nil
	if _, err := MergeRoots(context.Background(), filepath.Join(t.TempDir(), "aws", "eu-west-2"), roots, MergeOptions{}); err == nil {
		t.Error("merged with no environment to plan as")
	}
}

func TestRootProvider(t *testing.T) {
	t.Parallel()
	// A provider block with an alias isn't the root's provider.
	aliased := "\nprovider \"aws\" {\n  alias  = \"global\"\n  region = \"us-east-1\"\n}\n"
	p, err := RootProvider(writeRootDir(t, "prd", "222222222222", "60", aliased), "aws")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "aws" || p.Source != "hashicorp/aws" || p.Version != "~> 6.14" {
		t.Errorf("provider %s, source %q, version %q", p.Name, p.Source, p.Version)
	}
	if p.Config["region"] != "eu-west-2" || fmt.Sprint(p.Config["allowed_account_ids"]) != "[222222222222]" {
		t.Errorf("config: %#v", p.Config)
	}
	if role, ok := p.Config["assume_role"].(map[string]interface{}); !ok || role["role_arn"] != "arn:aws:iam::222222222222:role/deploy" {
		t.Errorf("assume_role: %#v", p.Config["assume_role"])
	}
	if _, err := RootProvider(t.TempDir(), "aws"); err == nil {
		t.Error("a directory without a provider block has a provider")
	}
}
