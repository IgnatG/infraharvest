// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import "testing"

func TestSortResources(t *testing.T) {
	dir := t.TempDir()
	f := writeConfig(t, dir, GeneratedFileName, `# __generated__ by Terraform
# Please review these resources.

# __generated__ by Terraform from "b"
resource "aws_vpc" "b" {
  cidr_block = "10.1.0.0/16"
}

# __generated__ by Terraform from "a"
resource "aws_vpc" "a" {
  cidr_block = "10.0.0.0/16"
}

resource "aws_subnet" "c" {
}
`)

	changed, err := sortResources(f.path)
	if err != nil || !changed {
		t.Fatalf("want a change, got changed=%v err=%v", changed, err)
	}

	want := `resource "aws_subnet" "c" {
}

# __generated__ by Terraform from "a"
resource "aws_vpc" "a" {
  cidr_block = "10.0.0.0/16"
}

# __generated__ by Terraform from "b"
resource "aws_vpc" "b" {
  cidr_block = "10.1.0.0/16"
}
`
	if got := readFile(t, dir, GeneratedFileName); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if changed, err := sortResources(f.path); err != nil || changed {
		t.Errorf("sorting again: changed=%v err=%v", changed, err)
	}
}
