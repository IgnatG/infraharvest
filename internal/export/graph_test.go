// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/report"
)

// graphConfig is a root's configuration: references straight to a
// resource, through a local and through a data source, between the
// instances of for_each resources, from a module call, and to one.
const graphConfig = `locals {
  vpc = aws_vpc.main.id
}

data "aws_iam_policy_document" "logs" {
  statement {
    resources = [module.logs.s3_bucket_arn]
  }
}

resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}

resource "aws_subnet" "a" {
  for_each   = { x = "10.0.1.0/24", y = "10.0.2.0/24" }
  vpc_id     = local.vpc
  cidr_block = each.value
}

resource "aws_route_table_association" "a" {
  for_each  = { x = "x" }
  subnet_id = aws_subnet.a[each.key].id
}

resource "aws_iam_role_policy" "logs" {
  policy = data.aws_iam_policy_document.logs.json
}

module "logs" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "4.1.0"
  tags    = { vpc = aws_vpc.main.id }
}

module "local" {
  source = "./modules/queue"
}
`

// writeGraphOut writes an output directory with one root of graphConfig,
// and a root merged into another, whose configuration is elsewhere.
func writeGraphOut(t *testing.T) string {
	t.Helper()
	out := t.TempDir()
	r := &report.Report{}
	r.Tool = report.Component{Name: "infraharvest", Version: "v0.1.0"}
	r.Provider = report.Provider{Source: "hashicorp/aws", Constraint: "~> 6.0"}
	r.Directories = []report.Directory{
		{
			Path:  "aws/123456789012/eu-west-2",
			Scope: scope,
			Imported: []report.Resource{
				{Address: "aws_vpc.main", ID: "vpc-1"},
				{Address: `aws_subnet.a["x"]`, ID: "subnet-x"},
				{Address: `aws_subnet.a["y"]`, ID: "subnet-y"},
				{Address: `aws_route_table_association.a["x"]`, ID: "rtbassoc-x"},
				{Address: "aws_iam_role_policy.logs", ID: "app:logs"},
				{Address: "module.logs.aws_s3_bucket.this[0]", ID: "logs-bucket"},
				{Address: "module.local.aws_sqs_queue.this", ID: "https://sqs.eu-west-2.amazonaws.com/123456789012/q"},
			},
			Modules: []report.Module{
				{Name: "logs", Source: "terraform-aws-modules/s3-bucket/aws", Version: "4.1.0", Resources: []string{"aws_s3_bucket.this"}},
				{Name: "local", Source: "./modules/queue", Resources: []string{"aws_sqs_queue.this"}},
				{Source: "terraform-aws-modules/vpc/aws", Declined: "the plan would change"},
			},
		},
		{
			Path:       "aws/123456789012/eu-west-1",
			Scope:      "aws/123456789012/eu-west-1",
			Imported:   []report.Resource{{Address: "aws_vpc.main", ID: "vpc-2"}},
			MergedInto: "stack",
		},
	}
	r.AddDiscovered(scope, 7)
	r.Finish(map[string]int{"aws_vpc": 2}, nil, false)
	if err := r.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(out, "aws", "123456789012", "eu-west-2")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(graphConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return out
}

// label names a node for the test: its type and ID, or the module's
// source.
func label(n graphNode) string {
	if n.Kind == nodeModule {
		return "module " + n.Source
	}
	return n.Type + " " + n.ID
}

func TestGraph(t *testing.T) {
	t.Parallel()
	out := writeGraphOut(t)
	content := build(t, Options{Out: out, Document: DocGraph})
	var doc graphDoc
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != schemaGraph || doc.Cloud != "aws" || doc.Redaction.IDsHashed {
		t.Errorf("header: %+v", doc)
	}
	var nodes []string
	for _, n := range doc.Nodes {
		nodes = append(nodes, label(n))
	}
	wantNodes := []string{
		// The declined cluster is no module call.
		"module local", "module terraform-aws-modules/s3-bucket/aws",
		"aws_iam_role_policy app:logs",
		"aws_route_table_association rtbassoc-x",
		"aws_subnet subnet-x", "aws_subnet subnet-y",
		"aws_vpc vpc-1",
		"aws_sqs_queue https://sqs.eu-west-2.amazonaws.com/123456789012/q",
		"aws_s3_bucket logs-bucket",
		// The merged root's resource is a node, with no edges.
		"aws_vpc vpc-2",
	}
	if !slices.Equal(sorted(nodes), sorted(wantNodes)) {
		t.Errorf("nodes:\n%s\nwant:\n%s", strings.Join(nodes, "\n"), strings.Join(wantNodes, "\n"))
	}
	index := map[string]int{}
	for i, n := range doc.Nodes {
		index[label(n)] = i
	}
	for _, n := range doc.Nodes {
		switch {
		case n.Type == "aws_s3_bucket":
			if n.Parent == nil || label(doc.Nodes[*n.Parent]) != "module terraform-aws-modules/s3-bucket/aws" {
				t.Errorf("the bucket's parent: %v", n.Parent)
			}
		case n.Type == "aws_sqs_queue":
			if n.Parent == nil || label(doc.Nodes[*n.Parent]) != "module local" {
				t.Errorf("the queue's parent: %v", n.Parent)
			}
		case n.Kind == nodeModule && n.Source == sourceLocal && n.Version != "":
			t.Errorf("a local module has a version: %+v", n)
		case n.Kind == nodeModule && n.Source != sourceLocal && n.Version != "4.1.0":
			t.Errorf("the registry module's version: %+v", n)
		case n.Type == "aws_vpc" && n.ID == "vpc-2" && n.Region != "eu-west-1":
			t.Errorf("the merged root's VPC: %+v", n)
		}
	}
	var edges []string
	for _, e := range doc.Edges {
		edges = append(edges, label(doc.Nodes[e.From])+" -> "+label(doc.Nodes[e.To]))
	}
	wantEdges := []string{
		// Through a local, from each instance.
		"aws_subnet subnet-x -> aws_vpc vpc-1",
		"aws_subnet subnet-y -> aws_vpc vpc-1",
		// each.key: to the same instance only.
		"aws_route_table_association rtbassoc-x -> aws_subnet subnet-x",
		// Through a data source, to a module call.
		"aws_iam_role_policy app:logs -> module terraform-aws-modules/s3-bucket/aws",
		// From a module call.
		"module terraform-aws-modules/s3-bucket/aws -> aws_vpc vpc-1",
	}
	if !slices.Equal(sorted(edges), sorted(wantEdges)) {
		t.Errorf("edges:\n%s\nwant:\n%s", strings.Join(edges, "\n"), strings.Join(wantEdges, "\n"))
	}
	for _, leak := range []string{"aws_vpc.main", "module.logs", "modules/queue", "10.0.1.0/24", "eu-west-2/main.tf", `"x"`} {
		if strings.Contains(string(content), leak) {
			t.Errorf("the graph holds %q:\n%s", leak, content)
		}
	}
}

// --hash-ids hashes a node's ID as the inventory's, so the two join.
func TestGraphHashesIDsAsTheInventory(t *testing.T) {
	t.Parallel()
	out := writeGraphOut(t)
	key := []byte("0123456789abcdef0123456789abcdef")
	var graph graphDoc
	if err := json.Unmarshal(build(t, Options{Out: out, Document: DocGraph, HashKey: key}), &graph); err != nil {
		t.Fatal(err)
	}
	inventory := decodeInventory(t, build(t, Options{Out: out, Document: DocInventory, HashKey: key}))
	ids := map[string]bool{}
	for _, r := range inventory.Resources {
		ids[r.Type+" "+r.ID] = true
	}
	if !graph.Redaction.IDsHashed {
		t.Error("ids_hashed is false")
	}
	for _, n := range graph.Nodes {
		if n.Kind != nodeResource {
			continue
		}
		if strings.Contains(n.ID, "vpc-") || len(n.ID) != 64 {
			t.Errorf("not hashed: %+v", n)
		}
		if !ids[n.Type+" "+n.ID] {
			t.Errorf("%s %s isn't in the inventory", n.Type, n.ID)
		}
	}
}

func TestShareableSource(t *testing.T) {
	t.Parallel()
	for source, want := range map[string]string{
		"terraform-aws-modules/vpc/aws":                 "terraform-aws-modules/vpc/aws",
		"app.terraform.io/acme/network/aws":             "app.terraform.io/acme/network/aws",
		"./modules/queue":                               sourceLocal,
		"../shared/queue":                               sourceLocal,
		"/home/me/modules/queue":                        sourceLocal,
		"git::https://git.internal.example/infra/x.git": sourceOther,
		"github.com/acme/private-modules//vpc":          sourceOther,
		"s3::https://bucket.s3.amazonaws.com/vpc.zip":   sourceOther,
	} {
		if got := shareableSource(source); got != want {
			t.Errorf("shareableSource(%q) = %q, want %q", source, got, want)
		}
	}
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}
