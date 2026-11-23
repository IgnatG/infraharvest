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
	// The merged root's environment is dev; the other root is kept apart, as
	// the environment prod.
	r.Merges = []report.Merge{{
		Path:         "stack",
		Environments: []string{"dev", "prod"},
		Merged:       true,
		Roots:        []report.MergedRoot{{Env: "dev", Path: "aws/123456789012/eu-west-1"}},
		Apart:        []report.MergedRoot{{Env: "prod", Path: "aws/123456789012/eu-west-2"}},
	}}
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
	for _, leak := range []string{"modules/queue", "10.0.1.0/24", "eu-west-2/main.tf"} {
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

// graphKey hashes what a test expects hashed.
var graphKey = []byte("0123456789abcdef0123456789abcdef")

func decodeGraph(t *testing.T, content []byte) graphDoc {
	t.Helper()
	var doc graphDoc
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// nodeOf returns the node with the label.
func nodeOf(t *testing.T, doc graphDoc, name string) graphNode {
	t.Helper()
	for _, n := range doc.Nodes {
		if label(n) == name {
			return n
		}
	}
	t.Fatalf("no node %q", name)
	return graphNode{}
}

// A node has its environment, if its root has one, and its address as the
// configuration names it; for an environment merged into a root it shares,
// that is its own root's address, from before the merge.
func TestGraphEnvironmentsAndAddresses(t *testing.T) {
	t.Parallel()
	doc := decodeGraph(t, build(t, Options{Out: writeGraphOut(t), Document: DocGraph}))
	if doc.Redaction.Addresses != addressesPlain {
		t.Errorf("redaction: %+v", doc.Redaction)
	}
	for name, want := range map[string]struct{ env, address, module string }{
		"aws_vpc vpc-1":                              {"prod", "aws_vpc.main", ""},
		"aws_subnet subnet-x":                        {"prod", `aws_subnet.a["x"]`, ""},
		"aws_s3_bucket logs-bucket":                  {"prod", "module.logs.aws_s3_bucket.this[0]", ""},
		"module local":                               {"prod", "", "module.local"},
		"module terraform-aws-modules/s3-bucket/aws": {"prod", "", "module.logs"},
		// A merged root's resource: its environment, and its own root's address.
		"aws_vpc vpc-2": {"dev", "aws_vpc.main", ""},
	} {
		n := nodeOf(t, doc, name)
		if n.Environment != want.env || n.Address != want.address || n.ModuleAddress != want.module {
			t.Errorf("%s: environment %q, address %q, module address %q; want %+v", name, n.Environment, n.Address, n.ModuleAddress, want)
		}
	}
}

// Without environments in the report, no node has one.
func TestGraphWithoutEnvironments(t *testing.T) {
	t.Parallel()
	out := writeGraphOut(t)
	r, err := report.Read(out)
	if err != nil {
		t.Fatal(err)
	}
	r.Merges = nil
	if err := r.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	content := build(t, Options{Out: out, Document: DocGraph})
	if strings.Contains(string(content), `"environment"`) {
		t.Errorf("a node has an environment:\n%s", content)
	}
	for _, n := range decodeGraph(t, content).Nodes {
		if n.Environment != "" {
			t.Errorf("%+v", n)
		}
	}
}

// --hash-ids hashes the addresses with the key that hashes the IDs, as the
// OCSF inventory does, so a node's address is its event's address, and a
// resource in a module has its module call's address as its event has it;
// environments stay readable.
func TestGraphHashesAddressesAsTheOCSFInventory(t *testing.T) {
	t.Parallel()
	out := writeGraphOut(t)
	content := build(t, Options{Out: out, Document: DocGraph, HashKey: graphKey})
	doc := decodeGraph(t, content)
	if doc.Redaction.Addresses != addressesHashed || !doc.Redaction.IDsHashed {
		t.Errorf("redaction: %+v", doc.Redaction)
	}
	for _, leak := range []string{"aws_vpc.main", "aws_subnet.a", "module.logs", "module.local", "aws_s3_bucket.this"} {
		if strings.Contains(string(content), leak) {
			t.Errorf("the graph holds %q:\n%s", leak, content)
		}
	}
	events := decodeOCSF(t, build(t, Options{Out: out, Document: DocOCSFInventory, HashKey: graphKey}))
	red := newRedactor(nil, false, graphKey)
	var joined int
	for _, n := range doc.Nodes {
		if n.Environment == "" {
			t.Errorf("no environment: %+v", n)
		}
		if n.Kind == nodeModule {
			want := red.hash("module.logs")
			if n.Source == sourceLocal {
				want = red.hash("module.local")
			}
			if n.ModuleAddress != want {
				t.Errorf("module address of %s: %q, want %q", label(n), n.ModuleAddress, want)
			}
			continue
		}
		if n.Address == "" {
			continue
		}
		iac := eventOf(t, events, n.ID).Resources[0].IaC
		if n.Address != iac.Address {
			t.Errorf("%s: address %q, the OCSF inventory has %q", label(n), n.Address, iac.Address)
		}
		if n.Parent != nil && doc.Nodes[*n.Parent].ModuleAddress != iac.ModuleAddress {
			t.Errorf("%s: module address %q, the OCSF inventory has %q", label(n), doc.Nodes[*n.Parent].ModuleAddress, iac.ModuleAddress)
		}
		joined++
	}
	if joined == 0 {
		t.Error("no node has an address")
	}
	if got := nodeOf(t, doc, "aws_vpc "+red.hash("vpc-1")).Address; got != red.hash("aws_vpc.main") {
		t.Errorf("address: %q", got)
	}
}

// --drop-addresses leaves the addresses out, with or without a key, which
// it wins over; the environments stay.
func TestGraphDropsAddresses(t *testing.T) {
	t.Parallel()
	out := writeGraphOut(t)
	plain := decodeGraph(t, build(t, Options{Out: out, Document: DocGraph}))
	for name, opts := range map[string]Options{
		"plain":  {Out: out, Document: DocGraph, DropAddresses: true},
		"hashed": {Out: out, Document: DocGraph, DropAddresses: true, HashKey: graphKey},
	} {
		content := build(t, opts)
		doc := decodeGraph(t, content)
		if doc.Redaction.Addresses != addressesDropped {
			t.Errorf("%s: redaction: %+v", name, doc.Redaction)
		}
		if strings.Contains(string(content), `"address"`) || strings.Contains(string(content), `"module_address"`) || strings.Contains(string(content), "aws_vpc.main") {
			t.Errorf("%s: the graph holds an address:\n%s", name, content)
		}
		if len(doc.Nodes) != len(plain.Nodes) || len(doc.Edges) != len(plain.Edges) {
			t.Errorf("%s: %d nodes and %d edges, want %d and %d", name, len(doc.Nodes), len(doc.Edges), len(plain.Nodes), len(plain.Edges))
		}
		for i, n := range doc.Nodes {
			if n.Environment != plain.Nodes[i].Environment {
				t.Errorf("%s: environment of %s: %q", name, label(n), n.Environment)
			}
		}
	}
}

// A document made before the fields existed still passes the schema, and
// the new fields don't pass where they don't belong.
func TestGraphSchemaAddsOnlyOptionalFields(t *testing.T) {
	t.Parallel()
	good := build(t, Options{Out: writeGraphOut(t), Document: DocGraph, HashKey: graphKey})
	mutate := func(f func(doc map[string]any, nodes []any)) error {
		var doc map[string]any
		if err := json.Unmarshal(good, &doc); err != nil {
			t.Fatal(err)
		}
		f(doc, doc["nodes"].([]any))
		content, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return validate(DocGraph, content)
	}
	strip := func(doc map[string]any, nodes []any) {
		delete(doc["redaction"].(map[string]any), "addresses")
		for _, n := range nodes {
			for _, k := range []string{"environment", "address", "module_address"} {
				delete(n.(map[string]any), k)
			}
		}
	}
	if err := mutate(func(map[string]any, []any) {}); err != nil {
		t.Errorf("a document with the new fields: %v", err)
	}
	if err := mutate(strip); err != nil {
		t.Errorf("a document without the new fields: %v", err)
	}
	// The schema takes the root of a node, of either kind, before anything
	// writes one.
	withRoot := func(_ map[string]any, nodes []any) {
		for _, n := range nodes {
			n.(map[string]any)["root"] = "aws/123456789012/eu-west-2"
		}
	}
	if err := mutate(withRoot); err != nil {
		t.Errorf("a document with a root on each node: %v", err)
	}
	if err := mutate(func(doc map[string]any, nodes []any) {
		strip(doc, nodes)
		withRoot(doc, nodes)
	}); err != nil {
		t.Errorf("a document with a root and no addresses: %v", err)
	}
	// A module call is the second node, and a resource of a root with an
	// address the last.
	for name, f := range map[string]func(doc map[string]any, nodes []any){
		"empty environment":   func(_ map[string]any, nodes []any) { nodes[1].(map[string]any)["environment"] = "" },
		"empty root":          func(_ map[string]any, nodes []any) { nodes[1].(map[string]any)["root"] = "" },
		"empty address":       func(_ map[string]any, nodes []any) { nodes[len(nodes)-1].(map[string]any)["address"] = "" },
		"address on a module": func(_ map[string]any, nodes []any) { nodes[1].(map[string]any)["address"] = "module.x" },
		"module address on a resource": func(_ map[string]any, nodes []any) {
			nodes[len(nodes)-1].(map[string]any)["module_address"] = "module.x"
		},
		"other addresses": func(doc map[string]any, _ []any) { doc["redaction"].(map[string]any)["addresses"] = "some" },
	} {
		if err := mutate(f); err == nil {
			t.Errorf("%s: passed", name)
		}
	}
}

// The same output gives the same bytes, with each redaction.
func TestGraphIsDeterministic(t *testing.T) {
	t.Parallel()
	out := writeGraphOut(t)
	for _, opts := range []Options{{}, {HashKey: graphKey}, {DropAddresses: true}} {
		opts.Out, opts.Document = out, DocGraph
		if a, b := build(t, opts), build(t, opts); string(a) != string(b) {
			t.Errorf("two builds differ:\n%s\n%s", a, b)
		}
	}
}

// An address, or an environment, that looks like a credential stops the
// export, unless the addresses are left out.
func TestGraphRefusesCredentialAddresses(t *testing.T) {
	t.Parallel()
	out := writeGraphOut(t)
	r, err := report.Read(out)
	if err != nil {
		t.Fatal(err)
	}
	r.Directories[1].Imported[0].Address = "aws_vpc." + credentialTag
	if err := r.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(Options{Out: out, Document: DocGraph, Now: fixedNow}); err == nil {
		t.Error("an address that looks like a credential was written")
	}
	if _, err := Build(Options{Out: out, Document: DocGraph, Now: fixedNow, DropAddresses: true}); err != nil {
		t.Errorf("dropped: %v", err)
	}
	r.Merges[0].Apart[0].Env = credentialTag
	if err := r.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(Options{Out: out, Document: DocGraph, Now: fixedNow, DropAddresses: true}); err == nil {
		t.Error("an environment that looks like a credential was written")
	}
}

// The nodes of an environment merged into a root it shares have the address
// of the environment's own root, from before the merge, with its module
// call's, as the OCSF inventory's iac.address and iac.module_address have
// them, plain, hashed or dropped, so a finding from either export lands on
// the same node.
func TestGraphMergedEnvironmentsHaveTheirOwnAddresses(t *testing.T) {
	t.Parallel()
	out := writeGraphOut(t)
	r, err := report.Read(out)
	if err != nil {
		t.Fatal(err)
	}
	merged := &r.Directories[slices.IndexFunc(r.Directories, func(d report.Directory) bool { return d.MergedInto != "" })]
	merged.Imported = append(merged.Imported, report.Resource{Address: `module.net.aws_nat_gateway.this["a"]`, ID: "nat-2"})
	merged.Modules = []report.Module{{Name: "net", Source: "terraform-aws-modules/vpc/aws", Version: "6.0.0", Resources: []string{"aws_nat_gateway.this"}}}
	if err := r.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	red := newRedactor(nil, false, graphKey)
	for name, opts := range map[string]Options{
		"plain":   {Out: out},
		"hashed":  {Out: out, HashKey: graphKey},
		"dropped": {Out: out, DropAddresses: true},
	} {
		graphOpts, ocsfOpts := opts, opts
		graphOpts.Document, ocsfOpts.Document = DocGraph, DocOCSFInventory
		doc := decodeGraph(t, build(t, graphOpts))
		events := decodeOCSF(t, build(t, ocsfOpts))
		want := map[string]struct{ address, module string }{
			"aws_vpc":         {"aws_vpc.main", ""},
			"aws_nat_gateway": {`module.net.aws_nat_gateway.this["a"]`, "module.net"},
		}
		checked := 0
		for _, n := range doc.Nodes {
			if n.Environment != "dev" || n.Kind != nodeResource {
				continue
			}
			checked++
			iac := eventOf(t, events, n.ID).Resources[0].IaC
			if n.Address != iac.Address {
				t.Errorf("%s: %s: address %q, the OCSF inventory has %q", name, label(n), n.Address, iac.Address)
			}
			var module string
			if n.Parent != nil {
				module = doc.Nodes[*n.Parent].ModuleAddress
				if module != iac.ModuleAddress {
					t.Errorf("%s: %s: module address %q, the OCSF inventory has %q", name, label(n), module, iac.ModuleAddress)
				}
			}
			if opts.DropAddresses {
				if n.Address != "" || module != "" {
					t.Errorf("%s: %s: an address was written: %q %q", name, label(n), n.Address, module)
				}
				continue
			}
			resource := want[n.Type]
			wantAddress, wantModule := resource.address, resource.module
			if opts.HashKey != nil {
				wantAddress = red.hash(wantAddress)
				if wantModule != "" {
					wantModule = red.hash(wantModule)
				}
			}
			if resource.address == "" || n.Address != wantAddress || module != wantModule {
				t.Errorf("%s: %s: address %q and module address %q, want %q and %q", name, label(n), n.Address, module, wantAddress, wantModule)
			}
		}
		if checked != 2 {
			t.Errorf("%s: %d nodes of the merged environment, want 2", name, checked)
		}
	}
}
