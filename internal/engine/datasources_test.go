// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

var awsDataSources = map[string]DataSource{
	"aws_iam_role":       {Type: "aws_iam_role", Argument: "name"},
	"aws_route_table":    {Type: "aws_route_table", Argument: "route_table_id"},
	"aws_security_group": {Type: "aws_security_group", Argument: "id"},
	"aws_vpc":            {Type: "aws_vpc", Argument: "id"},
}

// addDataSources is addDataSourcesSkipping for every resource: it
// reports whether it changed anything.
func addDataSources(dir string, external []External, sources map[string]DataSource, taken Names) (bool, error) {
	edited, err := addDataSourcesSkipping(dir, external, sources, taken, nil)
	return len(edited) > 0, err
}

func TestAddDataSources(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "aws_lb" "web" {
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

	generated := squashed(readFile(t, dir, rootfile.GeneratedFileName))
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
	data := squashed(readFile(t, dir, rootfile.DataFileName))
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
	t.Parallel()
	for _, reversed := range []bool{false, true} {
		dir := t.TempDir()
		writeConfig(t, dir, rootfile.GeneratedFileName, `resource "aws_lambda_function" "a" {
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

		data := squashed(readFile(t, dir, rootfile.DataFileName))
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

// A data source is named after the resource's name, without the words of
// its type, and after its ID where it has no name; the same resource
// listed twice is read once, by its name; a name the root uses goes to
// what has it.
func TestAddDataSourcesNamedByResourceName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "aws_security_group" "web" {
  vpc_id = "vpc-0abc1234"
}

resource "aws_security_group_rule" "web" {
  security_group_id = "sg-0def5678"
}
`)
	external := []External{
		{Type: "aws_vpc", ID: "vpc-0abc1234"},
		{Type: "aws_vpc", ID: "vpc-0abc1234", Name: "shop-vpc"},
		{Type: "aws_security_group", ID: "sg-0def5678", Name: "sg-0def5678"},
	}

	changed, err := addDataSources(dir, external, awsDataSources, Names{"data.aws_vpc.shop": true})
	if err != nil || !changed {
		t.Fatalf("want data sources, got %v, %v", changed, err)
	}

	generated := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	for _, want := range []string{
		"vpc_id = data.aws_vpc.shop_2.id",
		"security_group_id = data.aws_security_group.sg_0def5678.id",
	} {
		if !strings.Contains(generated, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, generated)
		}
	}
	data := squashed(readFile(t, dir, rootfile.DataFileName))
	if want := `data "aws_vpc" "shop_2" { id = "vpc-0abc1234" }`; !strings.Contains(data, want) {
		t.Errorf("data.tf misses %q:\n%s", want, data)
	}
	if n := strings.Count(data, `data "aws_vpc"`); n != 1 {
		t.Errorf("want the VPC read once, got %d:\n%s", n, data)
	}
}

func TestDataLabel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		e    External
		want string
	}{
		{External{Type: "aws_vpc", ID: "vpc-0abc1234"}, "vpc_0abc1234"},
		{External{Type: "aws_vpc", ID: "vpc-0abc1234", Name: "vpc-0abc1234"}, "vpc_0abc1234"},
		{External{Type: "aws_vpc", ID: "vpc-0abc1234", Name: "shop-prod-vpc"}, "shop_prod"},
		{External{Type: "aws_iam_role", ID: "AdminRole", Name: "AdminRole"}, "admin_role"},
		{External{Type: "aws_kms_key", ID: "1234abcd", Name: "ebs-key"}, "ebs"},
		{External{Type: "aws_subnet", ID: "subnet-1", Name: "10.0.1.0"}, "r_10_0_1_0"},
	} {
		if got := dataLabel(tc.e); got != tc.want {
			t.Errorf("dataLabel(%+v) = %q, want %q", tc.e, got, tc.want)
		}
	}
}

func TestAddDataSourcesWithoutReferences(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, "resource \"aws_vpc\" \"a\" {\n  cidr_block = \"10.0.0.0/16\"\n}\n")

	changed, err := addDataSources(dir, []External{{Type: "aws_vpc", ID: "vpc-0abc1234"}}, awsDataSources, nil)
	if err != nil || changed {
		t.Fatalf("want no change, got %v, %v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, rootfile.DataFileName)); !os.IsNotExist(err) {
		t.Errorf("data.tf written: %v", err)
	}
}
