// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/adapters"
)

// testAdapter maps a bucket and its versioning onto a made-up module, and
// declines buckets named "declined".
var testAdapter = adapters.Adapter{
	Source:  "example/bucket/aws",
	Version: "1.0.0",
	Anchor:  "aws_s3_bucket",
	Members: []string{"aws_s3_bucket_versioning"},
	Map: func(c adapters.Cluster) (*adapters.Call, error) {
		name, _ := adapters.Read(c.Anchor.Address(), c.Anchor.Body).Attr("bucket")
		if strings.TrimSpace(string(name.Bytes())) == `"declined"` {
			return nil, adapters.Declinef("declined on purpose")
		}
		call := &adapters.Call{
			Addresses: map[string]string{c.Anchor.Address(): "aws_s3_bucket.this[0]"},
			Outputs:   map[string]map[string]string{c.Anchor.Address(): {"arn": "arn", "bucket": "id"}},
		}
		call.Set("bucket", name)
		for _, m := range c.Members {
			call.Addresses[m.Address()] = m.Type + ".this[0]"
		}
		return call, nil
	},
}

func resourceChange(address string, actions tfjson.Actions, before, after map[string]any) *tfjson.ResourceChange {
	return &tfjson.ResourceChange{Address: address, Mode: tfjson.ManagedResourceMode, Change: &tfjson.Change{Actions: actions, Before: before, After: after}}
}

// importedPlan plans each address for import, with no change.
func importedPlan(addresses ...string) *tfjson.Plan {
	p := &tfjson.Plan{}
	for _, a := range addresses {
		p.ResourceChanges = append(p.ResourceChanges, resourceChange(a, tfjson.Actions{tfjson.ActionNoop}, nil, nil))
	}
	return p
}

// rootChanges is the root's planned changes: imports with no change.
func rootChanges() map[string]string {
	changes := map[string]string{}
	for _, a := range []string{"aws_iam_policy.read", "aws_s3_bucket.logs", "aws_s3_bucket.state", "aws_s3_bucket_versioning.logs", "aws_s3_bucket_versioning.state"} {
		changes[a] = changeSignature(resourceChange("x."+a, tfjson.Actions{tfjson.ActionNoop}, nil, nil))
	}
	return changes
}

var bothCalls = []string{
	"aws_iam_policy.read",
	"module.logs.aws_s3_bucket.this[0]", "module.logs.aws_s3_bucket_versioning.this[0]",
	"module.state.aws_s3_bucket.this[0]", "module.state.aws_s3_bucket_versioning.this[0]",
}

func TestSynthesizeKeepsCallsThatPlanTheSame(t *testing.T) {
	dir, _ := moduleRoot(t, twoBuckets)
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{importedPlan(bothCalls...)}}
	before, err := importTargets(dir)
	if err != nil {
		t.Fatal(err)
	}

	declined, _, err := synthesize(context.Background(), tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, changeSummary{}, rootChanges(), nil)
	if err != nil || len(declined) != 0 {
		t.Fatalf("want every cluster moved, got declined=%v err=%v", declined, err)
	}

	root := readFile(t, dir, GeneratedFileName)
	for _, want := range []string{
		`module "logs" {`,
		`source  = "example/bucket/aws"`,
		`version = "1.0.0"`,
		`bucket  = "logs"`,
		`module "state" {`,
		"policy = module.logs.arn",
	} {
		if !strings.Contains(squashed(root), squashed(want)) {
			t.Errorf("generated.tf misses %q:\n%s", want, root)
		}
	}
	if strings.Contains(root, `resource "aws_s3_bucket`) {
		t.Errorf("moved resources left in the root:\n%s", root)
	}
	if imports := readFile(t, dir, ImportsFileName); !strings.Contains(imports, "to = module.state.aws_s3_bucket_versioning.this[0]") {
		t.Errorf("imports.tf not retargeted:\n%s", imports)
	}
	if got := strings.Join(tf.calls, ","); got != "init,plan,show" {
		t.Errorf("calls: %s", got)
	}

	moved, err := movedAddresses(dir, before)
	if err != nil {
		t.Fatal(err)
	}
	if moved["aws_s3_bucket.logs"] != "module.logs.aws_s3_bucket.this[0]" || len(moved) != 4 {
		t.Errorf("moved: %v", moved)
	}
	calls, err := moduleCalls(dir, moved, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].Name != "logs" || calls[0].Source != "example/bucket/aws" || calls[0].Version != "1.0.0" ||
		strings.Join(calls[0].Resources, ",") != "aws_s3_bucket.logs,aws_s3_bucket_versioning.logs" {
		t.Errorf("calls: %+v", calls)
	}
}

// A call whose resources plan differently is taken back; the others stay.
func TestSynthesizeTakesBackCallsThatChangeThePlan(t *testing.T) {
	dir, _ := moduleRoot(t, twoBuckets)
	first := importedPlan(bothCalls...)
	first.ResourceChanges[3] = resourceChange("module.state.aws_s3_bucket.this[0]", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"tags": nil}, map[string]any{"tags": map[string]any{}})
	second := importedPlan("aws_iam_policy.read", "aws_s3_bucket.state", "aws_s3_bucket_versioning.state", "module.logs.aws_s3_bucket.this[0]", "module.logs.aws_s3_bucket_versioning.this[0]")
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{first, second}}

	declined, _, err := synthesize(context.Background(), tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, changeSummary{}, rootChanges(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(declined) != 1 || !strings.Contains(declined[0].Declined, "aws_s3_bucket.state would plan differently: update (tags)") {
		t.Fatalf("declined: %+v", declined)
	}
	root := readFile(t, dir, GeneratedFileName)
	if !strings.Contains(root, `module "logs" {`) || !strings.Contains(root, `resource "aws_s3_bucket" "state" {`) || strings.Contains(root, `module "state"`) {
		t.Errorf("want logs moved and state kept:\n%s", root)
	}
}

// A module may set arguments the provider keeps only in state, which
// import can't: the plan then updates them, and nothing else.
func TestSynthesizeAllowsStateOnlyUpdates(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stateOnly map[string][]string
		kept      bool
	}{
		{"state-only", map[string][]string{"aws_s3_bucket": {"force_destroy"}}, true},
		{"other", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := moduleRoot(t, twoBuckets)
			p := importedPlan(bothCalls...)
			p.ResourceChanges[1] = resourceChange(bothCalls[1], tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"force_destroy": false}, map[string]any{"force_destroy": true})
			p.ResourceChanges[1].Type = "aws_s3_bucket"
			updated := changeSummary{Change: 1}
			tf := &fakeTerraform{dir: dir, plans: []fakePlan{{summary: updated}}, showns: []*tfjson.Plan{p}}

			declined, settled, err := synthesize(context.Background(), tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}, StateOnly: tc.stateOnly}, changeSummary{}, rootChanges(), nil)
			if err != nil {
				t.Fatal(err)
			}
			kept := !strings.Contains(squashed(readFile(t, dir, GeneratedFileName)), `resource "aws_s3_bucket" "logs"`)
			if kept != tc.kept {
				t.Errorf("logs moved: %v, want %v; declined %+v", kept, tc.kept, declined)
			}
			if tc.kept && (len(declined) != 0 || settled != updated) {
				t.Errorf("declined %+v, summary %+v", declined, settled)
			}
		})
	}
}

func TestSynthesizeTakesBackCallsThatCreate(t *testing.T) {
	dir, _ := moduleRoot(t, twoBuckets)
	p := importedPlan(bothCalls...)
	p.ResourceChanges = append(p.ResourceChanges, resourceChange("module.logs.aws_s3_bucket_public_access_block.this[0]", tfjson.Actions{tfjson.ActionCreate}, nil, map[string]any{}))
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{p, importedPlan(bothCalls[0], "aws_s3_bucket.logs", "aws_s3_bucket_versioning.logs", bothCalls[3], bothCalls[4])}}

	declined, _, err := synthesize(context.Background(), tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, changeSummary{}, rootChanges(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(declined) != 1 || declined[0].Declined != "the module would also create aws_s3_bucket_public_access_block.this[0]" {
		t.Errorf("declined: %+v", declined)
	}
}

// Without the modules (no registry), nothing moves.
func TestSynthesizeWithoutTheModules(t *testing.T) {
	dir, _ := moduleRoot(t, twoBuckets)
	tf := &fakeTerraform{dir: dir, initErr: errors.New("registry unreachable")}

	declined, _, err := synthesize(context.Background(), tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, changeSummary{}, rootChanges(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(declined) != 2 || !strings.Contains(declined[0].Declined, "registry unreachable") {
		t.Errorf("declined: %+v", declined)
	}
	if got := readFile(t, dir, GeneratedFileName); got != twoBuckets {
		t.Errorf("generated.tf not restored:\n%s", got)
	}
}

// An error the plan can't pin on one call takes them all back.
func TestSynthesizeOnAPlanError(t *testing.T) {
	dir, _ := moduleRoot(t, twoBuckets)
	tf := &fakeTerraform{dir: dir, plans: []fakePlan{{diags: []tfjson.Diagnostic{{Severity: tfjson.DiagnosticSeverityError, Summary: "Cycle"}}}}}

	declined, _, err := synthesize(context.Background(), tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, changeSummary{}, rootChanges(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(declined) != 2 || declined[1].Declined != "plan error: Cycle" {
		t.Errorf("declined: %+v", declined)
	}
	if got := readFile(t, dir, GeneratedFileName); got != twoBuckets {
		t.Errorf("generated.tf not restored:\n%s", got)
	}
}

// A plan that reports neither errors nor a result tells nothing: the calls
// are taken back, not planned again.
func TestSynthesizeWhenThePlanReportsNothing(t *testing.T) {
	dir, _ := moduleRoot(t, twoBuckets)
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{nil}}

	declined, _, err := synthesize(context.Background(), tf, dir, Options{Adapters: []adapters.Adapter{testAdapter}}, changeSummary{}, rootChanges(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(declined) != 2 || declined[0].Declined != "the plan reported no result" || declined[1].Declined != "the plan reported no result" {
		t.Errorf("declined: %+v", declined)
	}
	if got := strings.Join(tf.calls, ","); got != "init,plan,show" {
		t.Errorf("calls: %s", got)
	}
	if got := readFile(t, dir, GeneratedFileName); got != twoBuckets {
		t.Errorf("generated.tf not restored:\n%s", got)
	}
}

func TestMapClustersDeclines(t *testing.T) {
	for name, tc := range map[string]struct {
		root, reason string
	}{
		"by the adapter": {
			strings.Replace(twoBuckets, `bucket = "state"`, `bucket = "declined"`, 1),
			"declined on purpose",
		},
		"a reference without an output": {
			strings.Replace(twoBuckets, "policy = aws_s3_bucket.logs.arn", "policy = aws_s3_bucket.logs.region", 1),
			"the module has no output for a reference to aws_s3_bucket.logs.region",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, _ := moduleRoot(t, tc.root)
			trials, declined, err := mapClusters(dir, []adapters.Adapter{testAdapter}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(trials) != 1 || len(declined) != 1 || declined[0].Declined != tc.reason {
				t.Errorf("trials=%d declined=%+v", len(trials), declined)
			}
		})
	}
}

func squashed(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// References become references to module outputs, or to paths into them,
// wherever they are in an expression; longer names and data sources stay.
func TestSpliceTraversal(t *testing.T) {
	replacement, err := outputTokens("net", `subnets["us-central1/app"].self_link`)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "google_compute_instance" "vm" {
  subnetwork = google_compute_subnetwork.app.self_link
  labels     = { link = "${google_compute_subnetwork.app.self_link}/x" }
  other      = google_compute_subnetwork.app.self_link_2
  data_one   = data.google_compute_subnetwork.app.self_link
  network_interface {
    subnetwork = [google_compute_subnetwork.app.self_link, google_compute_subnetwork.app_2.self_link]
  }
}
`)
	f, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range f.resources() {
		replaceReferences(r.write.Body(), []string{"google_compute_subnetwork", "app", "self_link"}, replacement)
	}
	got := string(hclwrite.Format(f.file.Bytes()))
	for _, want := range []string{
		`subnetwork = module.net.subnets["us-central1/app"].self_link`,
		`link = "${module.net.subnets["us-central1/app"].self_link}/x"`,
		"other      = google_compute_subnetwork.app.self_link_2",
		"data_one   = data.google_compute_subnetwork.app.self_link",
		`subnetwork = [module.net.subnets["us-central1/app"].self_link, google_compute_subnetwork.app_2.self_link]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// A kept call whose module uses another provider configures it in the
// root; when every call is taken back, so is the provider.
func TestSynthesizeConfiguresModuleProviders(t *testing.T) {
	withBeta := testAdapter
	withBeta.Providers = []string{"google-beta"}
	moduleProviders := map[string]Provider{"google-beta": {Name: "google-beta", Source: "hashicorp/google-beta", Config: map[string]interface{}{"project": "acme"}}}

	dir, _ := moduleRoot(t, twoBuckets)
	writeConfig(t, dir, ProvidersFileName, "provider \"aws\" {\n  region = \"eu-west-2\"\n}\n")
	// What init selected, which pins the provider as the root's is.
	writeConfig(t, dir, LockFileName, "provider \"registry.terraform.io/hashicorp/google-beta\" {\n  version = \"8.6.0\"\n}\n")
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{importedPlan(bothCalls...)}}
	if _, _, err := synthesize(context.Background(), tf, dir, Options{Adapters: []adapters.Adapter{withBeta}, ModuleProviders: moduleProviders}, changeSummary{}, rootChanges(), nil); err != nil {
		t.Fatal(err)
	}
	if got := squashed(readFile(t, dir, ProvidersFileName)); !strings.Contains(got, squashed(`provider "google-beta" { project = "acme" }`)) || !strings.Contains(got, `provider "aws"`) {
		t.Errorf("providers.tf:\n%s", got)
	}
	if got := squashed(readFile(t, dir, VersionsFileName)); !strings.Contains(got, squashed(`google-beta = { source = "hashicorp/google-beta" version = "~> 8.6" }`)) {
		t.Errorf("versions.tf:\n%s", got)
	}

	dir, _ = moduleRoot(t, twoBuckets)
	writeConfig(t, dir, ProvidersFileName, "provider \"aws\" {\n  region = \"eu-west-2\"\n}\n")
	failing := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{nil}}
	if _, _, err := synthesize(context.Background(), failing, dir, Options{Adapters: []adapters.Adapter{withBeta}, ModuleProviders: moduleProviders}, changeSummary{}, rootChanges(), nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dir, ProvidersFileName) + readFile(t, dir, VersionsFileName); strings.Contains(got, "google-beta") {
		t.Errorf("a provider of no kept call:\n%s", got)
	}
}

// networkAdapter maps a VPC, and what joins it through its subnet, onto a
// made-up module that keeps the route table's route as a resource of its
// own.
var networkAdapter = adapters.Adapter{
	Source:   "example/network/aws",
	Version:  "1.0.0",
	Anchor:   "aws_vpc",
	Members:  []string{"aws_subnet", "aws_route_table", "aws_route_table_association", "aws_nat_gateway", "aws_eip"},
	Repeated: []string{"aws_subnet", "aws_route_table_association"},
	Through:  []string{"aws_route_table_association", "aws_nat_gateway", "aws_eip"},
	Map: func(c adapters.Cluster) (*adapters.Call, error) {
		call := &adapters.Call{
			Addresses: map[string]string{c.Anchor.Address(): "aws_vpc.this[0]"},
			Outputs:   map[string]map[string]string{c.Anchor.Address(): {"id": "vpc_id"}},
			Imports:   []adapters.Import{{To: "aws_route.this[0]", Of: "aws_route_table.public", Suffix: "_0.0.0.0/0"}},
		}
		for _, m := range c.Members {
			call.Addresses[m.Address()] = m.Type + `.this["` + m.Name + `"]`
		}
		return call, nil
	},
}

const network = `resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}

resource "aws_subnet" "a" {
  cidr_block = "10.0.1.0/24"
  vpc_id     = aws_vpc.main.id
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id
}

resource "aws_route_table_association" "a" {
  route_table_id = aws_route_table.public.id
  subnet_id      = aws_subnet.a.id
}

resource "aws_nat_gateway" "main" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.a.id
}

resource "aws_eip" "nat" {
  domain = "vpc"
}

resource "aws_eip" "other" {
  domain = "vpc"
}

resource "aws_security_group" "web" {
  vpc_id = aws_vpc.main.id
}
`

// Members join through the cluster's other resources, in either
// direction, and the call imports what the module keeps separately.
func TestSynthesizeJoinsThroughMembersAndAddsImports(t *testing.T) {
	dir, _ := moduleRoot(t, network)
	trials, declined, err := mapClusters(dir, []adapters.Adapter{networkAdapter}, nil)
	if err != nil || len(declined) != 0 || len(trials) != 1 {
		t.Fatalf("trials=%d declined=%+v err=%v", len(trials), declined, err)
	}
	members := trials[0].members()
	if got := strings.Join(members, ","); got != "aws_vpc.main,aws_eip.nat,aws_nat_gateway.main,aws_route_table.public,aws_route_table_association.a,aws_subnet.a" {
		t.Fatalf("members: %s", got)
	}

	changes := map[string]string{}
	addresses := []string{"aws_eip.other", "aws_security_group.web", "module.main.aws_route.this[0]"}
	for _, m := range members {
		changes[m] = changeSignature(resourceChange("x."+m, tfjson.Actions{tfjson.ActionNoop}, nil, nil))
		addresses = append(addresses, trials[0].moduleAddress(m))
	}
	tf := &fakeTerraform{dir: dir, plans: []fakePlan{{summary: changeSummary{Import: 1}}}, showns: []*tfjson.Plan{importedPlan(addresses...)}}
	declined, summary, err := synthesize(context.Background(), tf, dir, Options{Adapters: []adapters.Adapter{networkAdapter}}, changeSummary{}, changes, nil)
	if err != nil || len(declined) != 0 {
		t.Fatalf("want the call kept, got declined=%+v err=%v", declined, err)
	}
	if summary.Import != 1 {
		t.Errorf("summary: %+v", summary)
	}
	imports := squashed(readFile(t, dir, ImportsFileName))
	for _, want := range []string{
		`to = module.main.aws_route.this[0] id = "public_0.0.0.0/0"`,
		`to = module.main.aws_eip.this["nat"] id = "nat"`,
		`to = aws_eip.other id = "other"`,
	} {
		if !strings.Contains(imports, want) {
			t.Errorf("imports.tf misses %q:\n%s", want, imports)
		}
	}
	if root := squashed(readFile(t, dir, GeneratedFileName)); !strings.Contains(root, "vpc_id = module.main.vpc_id") {
		t.Errorf("generated.tf:\n%s", root)
	}
}

// The added imports count: a plan that imports only the root's resources
// doesn't match.
func TestSynthesizeExpectsTheAddedImports(t *testing.T) {
	dir, _ := moduleRoot(t, network)
	trials, _, err := mapClusters(dir, []adapters.Adapter{networkAdapter}, nil)
	if err != nil || len(trials) != 1 {
		t.Fatal(trials, err)
	}
	changes := map[string]string{}
	addresses := []string{"aws_eip.other", "aws_security_group.web"}
	for _, m := range trials[0].members() {
		changes[m] = changeSignature(resourceChange("x."+m, tfjson.Actions{tfjson.ActionNoop}, nil, nil))
		addresses = append(addresses, trials[0].moduleAddress(m))
	}
	tf := &fakeTerraform{dir: dir, showns: []*tfjson.Plan{importedPlan(addresses...)}}
	declined, _, err := synthesize(context.Background(), tf, dir, Options{Adapters: []adapters.Adapter{networkAdapter}}, changeSummary{}, changes, nil)
	if err != nil || len(declined) != 1 || declined[0].Declined != "the plan changed outside the module calls" {
		t.Fatalf("declined=%+v err=%v", declined, err)
	}
	if strings.Contains(readFile(t, dir, ImportsFileName), "aws_route.this") {
		t.Error("imports.tf keeps the added import")
	}
}

// Members the module doesn't manage stay in the root.
func TestMapClustersLeavesWhatTheAdapterLeaves(t *testing.T) {
	leaving := networkAdapter
	leaving.Map = func(c adapters.Cluster) (*adapters.Call, error) {
		call, err := networkAdapter.Map(c)
		if err != nil {
			return nil, err
		}
		delete(call.Addresses, "aws_eip.nat")
		call.Left = []string{"aws_eip.nat"}
		return call, nil
	}
	dir, _ := moduleRoot(t, network)
	trials, declined, err := mapClusters(dir, []adapters.Adapter{leaving}, nil)
	if err != nil || len(declined) != 0 || len(trials) != 1 {
		t.Fatalf("trials=%d declined=%+v err=%v", len(trials), declined, err)
	}
	if got := strings.Join(trials[0].members(), ","); got != "aws_vpc.main,aws_nat_gateway.main,aws_route_table.public,aws_route_table_association.a,aws_subnet.a" {
		t.Errorf("members: %s", got)
	}
}

// grantsAdapter maps a project's grants, grouped by their project, onto a
// made-up module; it declines the project "declined".
var grantsAdapter = adapters.Adapter{
	Source:   "example/grants/google",
	Version:  "1.0.0",
	Anchor:   "google_project_iam_member",
	Members:  []string{"google_project_iam_member"},
	Repeated: []string{"google_project_iam_member"},
	Group: func(r adapters.Resource) (string, bool) {
		attr := r.Body.GetAttribute("project")
		if attr == nil {
			return "", false
		}
		return string(attr.Expr().BuildTokens(nil).Bytes()), true
	},
	Map: func(c adapters.Cluster) (*adapters.Call, error) {
		if strings.Contains(string(c.Anchor.Body.GetAttribute("project").Expr().BuildTokens(nil).Bytes()), "declined") {
			return nil, adapters.Declinef("declined on purpose")
		}
		call := &adapters.Call{Addresses: map[string]string{c.Anchor.Address(): `google_project_iam_member.this["` + c.Anchor.Name + `"]`}}
		for _, m := range c.Members {
			call.Addresses[m.Address()] = `google_project_iam_member.this["` + m.Name + `"]`
		}
		return call, nil
	},
}

// Resources with the same key join one cluster without referring to each
// other; one without a key stays out, and a declined group is tried once.
func TestMapClustersGroups(t *testing.T) {
	dir, _ := moduleRoot(t, `resource "google_project_iam_member" "a" {
  project = "acme"
}

resource "google_project_iam_member" "b" {
  project = "other"
}

resource "google_project_iam_member" "c" {
  project = "acme"
}

resource "google_project_iam_member" "d" {
  member = "user:x"
}

resource "google_project_iam_member" "e" {
  project = "declined"
}

resource "google_project_iam_member" "f" {
  project = "declined"
}
`)
	trials, declined, err := mapClusters(dir, []adapters.Adapter{grantsAdapter}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var clusters []string
	for _, tr := range trials {
		clusters = append(clusters, strings.Join(tr.members(), ","))
	}
	if got := strings.Join(clusters, " "); got != "google_project_iam_member.a,google_project_iam_member.c google_project_iam_member.b" {
		t.Errorf("clusters: %s", got)
	}
	if len(declined) != 1 || strings.Join(declined[0].Resources, ",") != "google_project_iam_member.e,google_project_iam_member.f" {
		t.Errorf("declined: %+v", declined)
	}
}

// A module in a directory is called by its path from the root.
func TestCallSource(t *testing.T) {
	out := t.TempDir()
	root := filepath.Join(out, "aws", "123", "eu-west-2")
	for source, want := range map[string]string{
		filepath.Join(out, "modules", "bucket"): "../../../modules/bucket",
		filepath.Join(root, "local"):            "./local",
		"git::https://example.com/m.git?ref=v1": "git::https://example.com/m.git?ref=v1",
		"terraform-aws-modules/vpc/aws":         "terraform-aws-modules/vpc/aws",
	} {
		if got, err := callSource(root, source); err != nil || got != want {
			t.Errorf("callSource(%s) = %s, %v; want %s", source, got, err, want)
		}
	}
}
