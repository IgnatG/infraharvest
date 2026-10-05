// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var awsDataSources = map[string]DataSource{
	"aws_iam_role":       {Type: "aws_iam_role", Argument: "name"},
	"aws_route_table":    {Type: "aws_route_table", Argument: "route_table_id"},
	"aws_security_group": {Type: "aws_security_group", Argument: "id"},
	"aws_vpc":            {Type: "aws_vpc", Argument: "id"},
}

func TestAddDataSources(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_lb" "web" {
  security_groups = [aws_security_group.web.id, "sg-0def5678"]
}

resource "aws_security_group" "web" {
  vpc_id      = "vpc-0abc1234"
  description = "admin"
}

resource "aws_lambda_function" "f" {
  role = "admin"
}
`)
	external := []External{
		{Type: "aws_vpc", ID: "vpc-0abc1234"},
		{Type: "aws_security_group", ID: "sg-0def5678"},
		{Type: "aws_route_table", ID: "rtb-0aaa1111"}, // nothing refers to it
		{Type: "aws_iam_role", ID: "admin"},           // a name: only arguments named role
		{Type: "aws_sqs_queue", ID: "q"},              // no data source
	}

	changed, err := addDataSources(dir, external, awsDataSources, nil)
	if err != nil || !changed {
		t.Fatalf("want data sources, got %v, %v", changed, err)
	}

	generated := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{
		"security_groups = [aws_security_group.web.id, data.aws_security_group.sg_0def5678.id]",
		"vpc_id = data.aws_vpc.vpc_0abc1234.id",
		`description = "admin"`,
		"role = data.aws_iam_role.admin.id",
	} {
		if !strings.Contains(generated, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, generated)
		}
	}
	data := squashed(readFile(t, dir, DataFileName))
	for _, want := range []string{
		`data "aws_security_group" "sg_0def5678" { id = "sg-0def5678" }`,
		`data "aws_vpc" "vpc_0abc1234" { id = "vpc-0abc1234" }`,
		`data "aws_iam_role" "admin" { name = "admin" }`,
	} {
		if !strings.Contains(data, want) {
			t.Errorf("data.tf misses %q:\n%s", want, data)
		}
	}
	if strings.Contains(data, "rtb-0aaa1111") {
		t.Errorf("data.tf reads a resource nothing refers to:\n%s", data)
	}
}

// Two IDs with the same label are named in one order, whatever order the
// caller lists them in.
func TestAddDataSourcesNamesInOneOrder(t *testing.T) {
	for _, reversed := range []bool{false, true} {
		dir := t.TempDir()
		writeConfig(t, dir, GeneratedFileName, `resource "aws_lambda_function" "a" {
  role = "admin-x"
}

resource "aws_lambda_function" "b" {
  role = "admin_x"
}
`)
		external := []External{{Type: "aws_iam_role", ID: "admin-x"}, {Type: "aws_iam_role", ID: "admin_x"}}
		if reversed {
			external[0], external[1] = external[1], external[0]
		}

		changed, err := addDataSources(dir, external, awsDataSources, nil)
		if err != nil || !changed {
			t.Fatalf("reversed=%v: want data sources, got %v, %v", reversed, changed, err)
		}

		data := squashed(readFile(t, dir, DataFileName))
		for _, want := range []string{
			`data "aws_iam_role" "admin_x" { name = "admin-x" }`,
			`data "aws_iam_role" "admin_x_2" { name = "admin_x" }`,
		} {
			if !strings.Contains(data, want) {
				t.Errorf("reversed=%v: data.tf misses %q:\n%s", reversed, want, data)
			}
		}
	}
}

func TestAddDataSourcesWithoutReferences(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, "resource \"aws_vpc\" \"a\" {\n  cidr_block = \"10.0.0.0/16\"\n}\n")

	changed, err := addDataSources(dir, []External{{Type: "aws_vpc", ID: "vpc-0abc1234"}}, awsDataSources, nil)
	if err != nil || changed {
		t.Fatalf("want no change, got %v, %v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, DataFileName)); !os.IsNotExist(err) {
		t.Errorf("data.tf written: %v", err)
	}
}
