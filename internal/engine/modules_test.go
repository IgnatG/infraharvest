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
	"github.com/zclconf/go-cty/cty"
)

const twoBuckets = `resource "aws_iam_policy" "read" {
  policy = aws_s3_bucket.logs.arn
}

resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags   = local.tags
}

resource "aws_s3_bucket" "state" {
  bucket = "state"
  tags   = local.tags
}

resource "aws_s3_bucket_versioning" "logs" {
  bucket = aws_s3_bucket.logs.bucket
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.bucket
  versioning_configuration {
    status = "Enabled"
  }
}
`

var bucketSchemas = &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
	"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
		"aws_s3_bucket": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
			"id": {AttributeType: cty.String}, "arn": {AttributeType: cty.String},
			"bucket": {AttributeType: cty.String}, "tags": {AttributeType: cty.Map(cty.String)},
		}}},
		"aws_s3_bucket_versioning": {Block: &tfjson.SchemaBlock{
			Attributes:   map[string]*tfjson.SchemaAttribute{"id": {AttributeType: cty.String}, "bucket": {AttributeType: cty.String}},
			NestedBlocks: map[string]*tfjson.SchemaBlockType{"versioning_configuration": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{"status": {AttributeType: cty.String}}}}},
		}},
		"aws_iam_policy": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{"policy": {AttributeType: cty.String}}}},
	}},
}}

// moduleRoot writes a root with generated and its imports, and returns the
// root and the modules directory next to it.
func moduleRoot(t *testing.T, generated string) (string, string) {
	t.Helper()
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "123", "eu-west-2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := writeConfig(t, dir, GeneratedFileName, generated)
	var imports []Import
	for _, r := range f.resources() {
		typ, name, _ := strings.Cut(r.address, ".")
		imports = append(imports, Import{Type: typ, Name: name, ID: name})
	}
	content, err := ImportsFile(imports)
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, ImportsFileName, string(content))
	writeConfig(t, dir, VersionsFileName, string(VersionsFile(">= 1.16, < 2.0", Provider{Name: "aws", Source: "hashicorp/aws", Version: "~> 6.14"})))
	return dir, filepath.Join(out, ModulesDirName)
}

func TestModularize(t *testing.T) {
	dir, modulesDir := moduleRoot(t, twoBuckets)

	created, changed, err := modularize(dir, modulesDir, bucketSchemas)
	if err != nil || !changed || len(created) != 1 {
		t.Fatalf("want one module, got created=%v changed=%v err=%v", created, changed, err)
	}

	module := created[0]
	if !strings.HasPrefix(filepath.Base(module), "s3_bucket_") {
		t.Errorf("module named %s", filepath.Base(module))
	}
	main := readFile(t, module, "main.tf")
	for _, want := range []string{
		`resource "aws_s3_bucket" "this" {`,
		"bucket = var.bucket",
		"tags   = var.tags",
		`resource "aws_s3_bucket_versioning" "versioning" {`,
		"bucket = aws_s3_bucket.this.bucket",
		`status = "Enabled"`,
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.tf misses %q:\n%s", want, main)
		}
	}
	variables := readFile(t, module, "variables.tf")
	for _, want := range []string{`variable "bucket" {`, "type        = string", `variable "tags" {`, "type        = map(string)"} {
		if !strings.Contains(variables, want) {
			t.Errorf("variables.tf misses %q:\n%s", want, variables)
		}
	}
	versions := squashed(readFile(t, module, VersionsFileName))
	for _, want := range []string{`required_version = ">= 1.16"`, `source = "hashicorp/aws"`, `version = ">= 6.14"`} {
		if !strings.Contains(versions, want) {
			t.Errorf("versions.tf misses %q:\n%s", want, versions)
		}
	}
	outputs := readFile(t, module, "outputs.tf")
	for _, want := range []string{`output "arn" {`, "value       = aws_s3_bucket.this.arn", `output "versioning_id" {`} {
		if !strings.Contains(outputs, want) {
			t.Errorf("outputs.tf misses %q:\n%s", want, outputs)
		}
	}

	root := readFile(t, dir, GeneratedFileName)
	source := "../../../" + ModulesDirName + "/" + filepath.Base(module)
	for _, want := range []string{
		`module "logs" {`,
		`source = "` + source + `"`,
		`bucket = "logs"`,
		"tags   = local.tags",
		`module "state" {`,
		// A reference from outside uses the module's output.
		"policy = module.logs.arn",
	} {
		if !strings.Contains(root, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, root)
		}
	}
	if strings.Contains(root, `resource "aws_s3_bucket"`) {
		t.Errorf("clustered resources left in the root:\n%s", root)
	}
	imports := readFile(t, dir, ImportsFileName)
	for _, want := range []string{"to = module.logs.aws_s3_bucket.this", "to = module.state.aws_s3_bucket_versioning.versioning", "to = aws_iam_policy.read"} {
		if !strings.Contains(imports, want) {
			t.Errorf("imports.tf misses %q:\n%s", want, imports)
		}
	}
}

// A shape that occurs once gets no module: it would add nothing.
func TestModularizeNeedsRepeatedShapes(t *testing.T) {
	dir, modulesDir := moduleRoot(t, `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
}

resource "aws_s3_bucket_versioning" "logs" {
  bucket = aws_s3_bucket.logs.bucket
}
`)

	if _, changed, err := modularize(dir, modulesDir, bucketSchemas); err != nil || changed {
		t.Errorf("want no change, got changed=%v err=%v", changed, err)
	}
}

// A value that differs between clusters and refers inside them can't be a
// variable: the clusters keep their resources.
func TestModularizeDeclines(t *testing.T) {
	dir, modulesDir := moduleRoot(t, strings.Replace(strings.Replace(twoBuckets,
		"bucket = aws_s3_bucket.logs.bucket", `bucket = "${aws_s3_bucket.logs.bucket}-a"`, 1),
		"bucket = aws_s3_bucket.state.bucket", `bucket = "${aws_s3_bucket.state.bucket}-b"`, 1))

	if _, changed, err := modularize(dir, modulesDir, bucketSchemas); err != nil || changed {
		t.Errorf("want no change, got changed=%v err=%v", changed, err)
	}
}

func TestLiftModulesUndoesWhenThePlanChanges(t *testing.T) {
	dir, modulesDir := moduleRoot(t, twoBuckets)
	tf := &fakeTerraform{dir: dir, schemas: bucketSchemas, plans: []fakePlan{{summary: changeSummary{Change: 1}}}}

	kept, err := liftModules(context.Background(), tf, dir, modulesDir, changeSummary{}, nil, nil)
	if err != nil || kept {
		t.Fatalf("want the modules undone, got kept=%v err=%v", kept, err)
	}

	if got := readFile(t, dir, GeneratedFileName); got != twoBuckets {
		t.Errorf("generated.tf not restored:\n%s", got)
	}
	if entries, _ := os.ReadDir(modulesDir); len(entries) != 0 {
		t.Errorf("module left behind: %v", entries)
	}
}

func TestLiftModulesKeeps(t *testing.T) {
	dir, modulesDir := moduleRoot(t, twoBuckets)
	tf := &fakeTerraform{dir: dir, schemas: bucketSchemas}

	kept, err := liftModules(context.Background(), tf, dir, modulesDir, changeSummary{}, nil, nil)
	if err != nil || !kept {
		t.Fatalf("want the modules kept, got kept=%v err=%v", kept, err)
	}
	if strings.Join(tf.calls, ",") != "schema,init,plan,show" {
		t.Errorf("calls: got %v, want [schema init plan show]", tf.calls)
	}
}

// A child that refers to two parents of the same type goes to the first by
// address, whatever order its attributes come in.
func TestFindClustersPicksTheFirstParentByAddress(t *testing.T) {
	f := writeConfig(t, t.TempDir(), GeneratedFileName, `resource "aws_security_group" "b" {
  name = "b"
}

resource "aws_security_group" "a" {
  name = "a"
}

resource "aws_security_group_rule" "ab" {
  security_group_id        = aws_security_group.b.id
  source_security_group_id = aws_security_group.a.id
  type                     = "ingress"
}
`)

	for i := 0; i < 20; i++ {
		clusters := findClusters(f)
		if len(clusters) != 1 || clusters[0].members[0].address != "aws_security_group.a" || len(clusters[0].members) != 2 {
			t.Fatalf("want the rule clustered with aws_security_group.a, got %+v", clusters)
		}
	}
	if got := referencedAddresses(f.resources()[2].syntax.Body); strings.Join(got, ",") != "aws_security_group.a,aws_security_group.b" {
		t.Errorf("referencedAddresses: got %v", got)
	}
}

// An input fed by a secret variable is sensitive in the module too.
func TestModularizeMarksSecretInputsSensitive(t *testing.T) {
	dir, modulesDir := moduleRoot(t, strings.Replace(strings.Replace(twoBuckets,
		"bucket = \"logs\"\n", "bucket = \"logs\"\n  key    = var.aws_s3_bucket_logs_key\n", 1),
		"bucket = \"state\"\n", "bucket = \"state\"\n  key    = var.aws_s3_bucket_state_key\n", 1))

	created, changed, err := modularize(dir, modulesDir, bucketSchemas)
	if err != nil || !changed || len(created) != 1 {
		t.Fatalf("want one module, got created=%v changed=%v err=%v", created, changed, err)
	}

	variables := squashed(readFile(t, created[0], "variables.tf"))
	if !strings.Contains(variables, `variable "key" { description = "The key of the module's aws_s3_bucket." type = any sensitive = true }`) {
		t.Errorf("variables.tf misses the sensitive variable:\n%s", variables)
	}
	if strings.Count(variables, "sensitive") != 1 {
		t.Errorf("want only key sensitive:\n%s", variables)
	}
	if main := readFile(t, created[0], "main.tf"); !strings.Contains(main, "key    = var.key") {
		t.Errorf("main.tf:\n%s", main)
	}
	if root := readFile(t, dir, GeneratedFileName); !strings.Contains(root, "key    = var.aws_s3_bucket_logs_key") {
		t.Errorf("generated.tf doesn't pass the secret:\n%s", root)
	}
}

func TestLowerBound(t *testing.T) {
	for constraint, want := range map[string]string{
		"~> 6.14":        ">= 6.14",
		">= 1.5, < 2.0":  ">= 1.5",
		"1.16.5":         ">= 1.16.5",
		">= 1.5, >= 1.7": ">= 1.7",
		"> 1.5, != 1.6":  ">= 1.5",
	} {
		got, err := lowerBound(constraint)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v, want %q", constraint, got, err, want)
		}
	}
	for _, constraint := range []string{"< 2.0", "not a version"} {
		if got, err := lowerBound(constraint); err == nil {
			t.Errorf("%q: got %q, want an error", constraint, got)
		}
	}
}

// Roots imported in parallel share the modules directory: one root's undo
// leaves a module another calls, and the last user's undo removes it.
func TestReleaseModulesKeepsSharedModules(t *testing.T) {
	modulesDir := t.TempDir()
	path := filepath.Join(modulesDir, "bucket_1a2b3c4d")
	write := func(at string) error { return os.WriteFile(filepath.Join(at, "main.tf"), []byte("# module\n"), 0o644) }
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")

	if wrote, err := holdModule(a, path, write); err != nil || !wrote {
		t.Fatalf("a: wrote %v, %v", wrote, err)
	}
	if wrote, err := holdModule(b, path, write); err != nil || wrote {
		t.Fatalf("b: wrote %v, %v; want it reused", wrote, err)
	}
	if err := releaseModules(a); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "main.tf")); err != nil {
		t.Fatalf("a's undo removed the module b calls: %v", err)
	}
	if err := releaseModules(b); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the module outlived its last user: %v", err)
	}
}

// A module is reused only when its variables and outputs match too.
func TestModuleNameCoversVariablesAndOutputs(t *testing.T) {
	dir, modulesDir := moduleRoot(t, twoBuckets)
	created, _, err := modularize(dir, modulesDir, bucketSchemas)
	if err != nil || len(created) != 1 {
		t.Fatalf("got created=%v err=%v", created, err)
	}
	// The same clusters, with a schema that outputs less.
	less := &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
		"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
			"aws_s3_bucket":            {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{"id": {AttributeType: cty.String}, "arn": {AttributeType: cty.String}}}},
			"aws_s3_bucket_versioning": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{"id": {AttributeType: cty.String}}}},
		}},
	}}
	other, otherModules := moduleRoot(t, twoBuckets)
	createdLess, _, err := modularize(other, otherModules, less)
	if err != nil || len(createdLess) != 1 {
		t.Fatalf("got created=%v err=%v", createdLess, err)
	}
	if filepath.Base(created[0]) == filepath.Base(createdLess[0]) {
		t.Errorf("modules with other outputs share the name %s", filepath.Base(created[0]))
	}
}

// A lift with the same totals is still undone when a resource left in the
// root plans another change than it had, as verify undoes an edit.
func TestLiftModulesUndoesWhenAResourcePlansDifferently(t *testing.T) {
	dir, modulesDir := moduleRoot(t, twoBuckets)
	shown := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		change("aws_iam_policy.read", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"policy": "a"}, map[string]any{"policy": "b"}),
	}}
	tf := &fakeTerraform{dir: dir, schemas: bucketSchemas, shown: shown}
	changes := map[string]string{"aws_iam_policy.read": changeSignature(change("aws_iam_policy.read", tfjson.Actions{tfjson.ActionNoop}, nil, nil))}

	kept, err := liftModules(context.Background(), tf, dir, modulesDir, changeSummary{}, changes, nil)
	if err != nil || kept {
		t.Fatalf("want the modules undone, got kept=%v err=%v", kept, err)
	}
	if got := readFile(t, dir, GeneratedFileName); got != twoBuckets {
		t.Errorf("generated.tf not restored:\n%s", got)
	}
}

// A root whose modules directory is inside it, as with --layout {out},
// calls its modules by ./ paths: modules/<name> would be a registry
// address.
func TestModularizeIntoTheRootsOwnModulesDir(t *testing.T) {
	dir, _ := moduleRoot(t, twoBuckets)
	modulesDir := filepath.Join(dir, ModulesDirName)

	created, changed, err := modularize(dir, modulesDir, bucketSchemas)
	if err != nil || !changed || len(created) != 1 {
		t.Fatalf("want one module, got created=%v changed=%v err=%v", created, changed, err)
	}

	want := `source = "./` + ModulesDirName + "/" + filepath.Base(created[0]) + `"`
	if root := readFile(t, dir, GeneratedFileName); !strings.Contains(root, want) {
		t.Errorf("generated.tf misses %q:\n%s", want, root)
	}
}

func TestLocalSource(t *testing.T) {
	for _, c := range []struct{ rel, want string }{
		{filepath.Join("..", "..", "..", ModulesDirName), "../../../modules/vpc"},
		{ModulesDirName, "./modules/vpc"},
		{".", "./vpc"},
	} {
		if got := localSource(c.rel, "vpc"); got != c.want {
			t.Errorf("localSource(%q): got %q, want %q", c.rel, got, c.want)
		}
	}
}

// A generated module is laid out as it is written, before other roots can
// see it.
func TestModularizeLaysOutTheModule(t *testing.T) {
	dir, modulesDir := moduleRoot(t, twoBuckets)

	created, changed, err := modularize(dir, modulesDir, bucketSchemas)
	if err != nil || !changed || len(created) != 1 {
		t.Fatalf("want one module, got created=%v changed=%v err=%v", created, changed, err)
	}

	main := readFile(t, created[0], "main.tf")
	if out, ok := layout([]byte(main)); !ok || string(out) != main {
		t.Errorf("main.tf isn't laid out:\n%s", main)
	}
}
