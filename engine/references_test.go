// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

// importedValues are what a plan of the imports reports, by address.
var importedValues = map[string]map[string]any{
	"aws_vpc.main":                  {"id": "vpc-0abc1234", "arn": "arn:aws:ec2:us-east-1:1:vpc/vpc-0abc1234"},
	"aws_subnet.a":                  {"id": "subnet-0aaa1111"},
	"aws_security_group.web":        {"id": "sg-0aaa1111", "arn": "arn:aws:ec2:us-east-1:1:security-group/sg-0aaa1111"},
	"aws_security_group.db":         {"id": "sg-0bbb2222"},
	"aws_s3_bucket.logs":            {"id": "logs", "bucket": "logs", "arn": "arn:aws:s3:::logs"},
	"aws_s3_bucket_versioning.logs": {"id": "logs"},
	"aws_iam_role.app":              {"id": "app", "name": "app", "arn": "arn:aws:iam::1:role/app"},
	"aws_ecr_repository.app":        {"id": "app"},
	"aws_sfn_state_machine.flow":    {"id": "arn:aws:states:us-east-1:1:stateMachine:flow", "arn": "arn:aws:states:us-east-1:1:stateMachine:flow"},
}

func TestReferenceIndex(t *testing.T) {
	index := referenceIndex(importedValues)

	for value, want := range map[string]referenceTarget{
		"vpc-0abc1234": {"aws_vpc.main", "id"},
		"arn:aws:ec2:us-east-1:1:vpc/vpc-0abc1234": {"aws_vpc.main", "arn"},
		// The versioning resource's id is the bucket's name too.
		"logs":                    {"aws_s3_bucket.logs", "bucket"},
		"arn:aws:iam::1:role/app": {"aws_iam_role.app", "arn"},
		// id and ARN of one resource: the ARN.
		"arn:aws:states:us-east-1:1:stateMachine:flow": {"aws_sfn_state_machine.flow", "arn"},
	} {
		if got := index[value]; got != want {
			t.Errorf("%s: got %+v, want %+v", value, got, want)
		}
	}
	// A role and a repository both called app: no telling which is meant.
	if got, ok := index["app"]; ok {
		t.Errorf("ambiguous value indexed: %+v", got)
	}
}

const referencingGenerated = `resource "aws_subnet" "a" {
  vpc_id = "vpc-0abc1234"
}

resource "aws_security_group" "web" {
  name   = "logs"
  vpc_id = "vpc-0abc1234"
  ingress {
    security_groups = ["sg-0bbb2222", "sg-0external"]
  }
}

resource "aws_security_group" "db" {
  vpc_id = "vpc-0abc1234"
  ingress {
    security_groups = ["sg-0aaa1111"]
  }
}

resource "aws_s3_bucket" "logs" {
  bucket = "logs"
}

resource "aws_s3_bucket_versioning" "logs" {
  bucket = "logs"
}

resource "aws_iam_role_policy_attachment" "app" {
  role = "app"
}

resource "aws_sfn_state_machine" "flow" {
  role_arn = "arn:aws:iam::1:role/app"
}
`

func TestAddReferences(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, referencingGenerated)

	changed, err := addReferences(dir, importedValues)
	if err != nil || !changed {
		t.Fatalf("want references, got changed=%v err=%v", changed, err)
	}

	got := readFile(t, dir, GeneratedFileName)
	for _, want := range []string{
		"vpc_id = aws_vpc.main.id",
		// Lists keep literals that refer to nothing imported.
		`security_groups = [aws_security_group.db.id, "sg-0external"]`,
		// Named after the type: the bucket's name refers to the bucket.
		"bucket = aws_s3_bucket.logs.bucket",
		"role_arn = aws_iam_role.app.arn",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Count(got, "vpc_id = aws_vpc.main.id") != 3 {
		t.Errorf("want every vpc_id referenced:\n%s", got)
	}
	for _, kept := range []string{
		// web refers to db, so db can't refer back to web.
		`security_groups = ["sg-0aaa1111"]`,
		// The bucket doesn't refer to itself.
		"resource \"aws_s3_bucket\" \"logs\" {\n  bucket = \"logs\"",
		// A name isn't an ID: only arguments named after the type refer.
		`name   = "logs"`,
		// Ambiguous.
		`role = "app"`,
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q changed:\n%s", kept, got)
		}
	}
}

func TestDependenciesRejectCycles(t *testing.T) {
	g := dependencies{}
	for _, edge := range [][2]string{{"a", "b"}, {"b", "c"}} {
		if !g.add(edge[0], edge[1]) {
			t.Fatalf("%v rejected", edge)
		}
	}
	if g.add("c", "a") {
		t.Error("c -> a closes a cycle")
	}
	if !g.add("a", "c") {
		t.Error("a -> c is no cycle")
	}
}

// postProcess plans once for the imported values, then once per step it
// keeps or undoes.
func TestPostProcessAddsReferences(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, referencingGenerated)
	tf := &fakeTerraform{dir: dir, shown: plannedImports(importedValues)}

	if err := postProcess(context.Background(), tf, dir, Options{}, nil); err != nil {
		t.Fatal(err)
	}

	if want := []string{"plan", "show", "plan"}; !reflect.DeepEqual(tf.calls, want) {
		t.Errorf("calls: got %v, want %v", tf.calls, want)
	}
	if got := readFile(t, dir, GeneratedFileName); !strings.Contains(got, "aws_vpc.main.id") {
		t.Errorf("no references:\n%s", got)
	}
}

func TestPostProcessUndoesReferencesThatChangeThePlan(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, referencingGenerated)
	tf := &fakeTerraform{dir: dir, shown: plannedImports(importedValues), plans: []fakePlan{{}, {summary: changeSummary{Change: 1}}}}

	if err := postProcess(context.Background(), tf, dir, Options{}, nil); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, dir, GeneratedFileName); strings.Contains(got, "aws_vpc.main.id") {
		t.Errorf("references kept although the plan changed:\n%s", got)
	}
}

func TestPlaceholders(t *testing.T) {
	vars := placeholders([]Secret{{Variable: "a"}, {Variable: "b", ty: ssmSchemas.Schemas["registry.terraform.io/hashicorp/aws"].ResourceSchemas["aws_ssm_parameter"].Block.Attributes["value"].AttributeType}})
	if len(vars) != 2 {
		t.Errorf("want a value per secret, got %d", len(vars))
	}
}

func plannedImports(values map[string]map[string]any) *tfjson.Plan {
	p := &tfjson.Plan{}
	for address, attrs := range values {
		p.ResourceChanges = append(p.ResourceChanges, &tfjson.ResourceChange{
			Address: address,
			Mode:    tfjson.ManagedResourceMode,
			Change:  &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionNoop}, Before: attrs, After: attrs},
		})
	}
	return p
}
