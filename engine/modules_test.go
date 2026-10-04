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

	kept, err := liftModules(context.Background(), tf, dir, modulesDir, changeSummary{}, nil)
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

	kept, err := liftModules(context.Background(), tf, dir, modulesDir, changeSummary{}, nil)
	if err != nil || !kept {
		t.Fatalf("want the modules kept, got kept=%v err=%v", kept, err)
	}
	if strings.Join(tf.calls, ",") != "schema,init,plan" {
		t.Errorf("calls: got %v, want [schema init plan]", tf.calls)
	}
}
