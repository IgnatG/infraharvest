// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package incremental

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/run"
)

const awsProviders = `provider "aws" {
  region = "us-east-1"
}
`

var awsDefaultTags = run.DefaultTags{Provider: "aws", Attribute: "tags", Block: "default_tags", ReservedPrefix: "aws:"}

func writeConfig(t *testing.T, dir, name, content string) *hclfile.File {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := hclfile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func squashed(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// rootWorkspace opens the workspace of the root in dir, for a test.
func rootWorkspace(t testing.TB, dir string) *run.Workspace {
	t.Helper()
	ws, err := run.OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

// flushed writes ws to its directory, for a test that reads the files there.
func flushed(t testing.TB, ws *run.Workspace) {
	t.Helper()
	if err := ws.Flush(); err != nil {
		t.Fatal(err)
	}
}

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

var awsDataSources = map[string]run.DataSource{
	"aws_iam_role":       {Type: "aws_iam_role", Argument: "name"},
	"aws_route_table":    {Type: "aws_route_table", Argument: "route_table_id"},
	"aws_security_group": {Type: "aws_security_group", Argument: "id"},
	"aws_vpc":            {Type: "aws_vpc", Argument: "id"},
}

var azureDefaultTags = run.DefaultTags{Provider: "azurerm", Attribute: "tags", ReservedPrefix: "hidden-"}

// wsFile returns the file name of ws, which it must have.
func wsFile(t testing.TB, ws *run.Workspace, name string) string {
	t.Helper()
	content, ok := ws.File(name)
	if !ok {
		t.Fatalf("the workspace has no %s", name)
	}
	return string(content)
}
