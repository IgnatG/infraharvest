// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
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
	"aws_codecommit_repository.app": {"id": "app"},
	"aws_sfn_state_machine.flow":    {"id": "arn:aws:states:us-east-1:1:stateMachine:flow", "arn": "arn:aws:states:us-east-1:1:stateMachine:flow"},
}

// addReferences is addReferencesSkipping for every resource: it reports
// whether it changed anything.
func addReferences(dir string, values map[string]map[string]any) (bool, error) {
	edited, err := addReferencesSkipping(dir, values, nil)
	return len(edited) > 0, err
}

func TestReferenceIndex(t *testing.T) {
	t.Parallel()
	index := referenceIndex(importedValues)

	for value, want := range map[string][]referenceTarget{
		"vpc-0abc1234": {{address: "aws_vpc.main", attribute: "id"}},
		"arn:aws:ec2:us-east-1:1:vpc/vpc-0abc1234": {{address: "aws_vpc.main", attribute: "arn"}},
		// The versioning resource's id is the bucket's name too.
		"logs":                    {{address: "aws_s3_bucket.logs", attribute: "bucket"}, {address: "aws_s3_bucket_versioning.logs", attribute: "id"}},
		"arn:aws:iam::1:role/app": {{address: "aws_iam_role.app", attribute: "arn"}},
		// id and ARN of one resource: the ARN.
		"arn:aws:states:us-east-1:1:stateMachine:flow": {{address: "aws_sfn_state_machine.flow", attribute: "arn"}},
		// Every resource called app, by address.
		"app": {{address: "aws_codecommit_repository.app", attribute: "id"}, {address: "aws_ecr_repository.app", attribute: "id"}, {address: "aws_iam_role.app", attribute: "name"}},
	} {
		if got := index[value]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %+v, want %+v", value, got, want)
		}
	}
}

func TestUniqueTarget(t *testing.T) {
	t.Parallel()
	index := referenceIndex(importedValues)
	for _, tc := range []struct {
		argument, value, want string
	}{
		{"vpc_id", "vpc-0abc1234", "aws_vpc.main"},
		// The bucket is its versioning's parent.
		{"bucket", "logs", "aws_s3_bucket.logs"},
		{"name", "logs", ""},
		// Named after the role's type, not the repositories'.
		{"role", "app", "aws_iam_role.app"},
		// Two repositories: no telling which is meant.
		{"repository", "app", ""},
		{"name", "app", ""},
	} {
		got, ok := uniqueTarget(tc.argument, tc.value, index[tc.value])
		if ok != (tc.want != "") || got.address != tc.want {
			t.Errorf("%s = %q: got %+v, %v, want %q", tc.argument, tc.value, got, ok, tc.want)
		}
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

resource "aws_ecr_lifecycle_policy" "app" {
  repository = "app"
}

resource "aws_sfn_state_machine" "flow" {
  role_arn = "arn:aws:iam::1:role/app"
}
`

func TestAddReferences(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, referencingGenerated)

	changed, err := addReferences(dir, importedValues)
	if err != nil || !changed {
		t.Fatalf("want references, got changed=%v err=%v", changed, err)
	}

	got := readFile(t, dir, rootfile.GeneratedFileName)
	for _, want := range []string{
		"vpc_id = aws_vpc.main.id",
		// Lists keep literals that refer to nothing imported.
		`security_groups = [aws_security_group.db.id, "sg-0external"]`,
		// Named after the type: the bucket's name refers to the bucket.
		"bucket = aws_s3_bucket.logs.bucket",
		"role_arn = aws_iam_role.app.arn",
		// Named after the role's type: not the repositories called app.
		"role = aws_iam_role.app.name",
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
		// Ambiguous: two repositories are called app.
		`repository = "app"`,
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q changed:\n%s", kept, got)
		}
	}
}

// A role and its instance profile share their name as id: the arguments
// named role refer to the role, the names stay.
func TestAddReferencesToTheOwnerNamedAfterTheArgument(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "aws_iam_instance_profile" "ci" {
  name = "ci"
  role = "ci"
}

resource "aws_iam_role" "ci" {
  name = "ci"
}

resource "aws_iam_role_policy" "ci" {
  name = "ci"
  role = "ci"
}

resource "aws_iam_role_policy_attachment" "ci" {
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
  role       = "ci"
}
`)
	values := map[string]map[string]any{
		"aws_iam_role.ci":                   {"id": "ci", "name": "ci", "arn": "arn:aws:iam::1:role/ci"},
		"aws_iam_instance_profile.ci":       {"id": "ci", "name": "ci", "arn": "arn:aws:iam::1:instance-profile/ci"},
		"aws_iam_role_policy.ci":            {"id": "ci:ci", "name": "ci"},
		"aws_iam_role_policy_attachment.ci": {"id": "ci-20260101"},
	}

	changed, err := addReferences(dir, values)
	if err != nil || !changed {
		t.Fatalf("want references, got changed=%v err=%v", changed, err)
	}

	got := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	if strings.Count(got, "role = aws_iam_role.ci.name") != 3 {
		t.Errorf("want every role argument to refer to the role:\n%s", got)
	}
	if strings.Count(got, `name = "ci"`) != 3 {
		t.Errorf("want the names left as they are:\n%s", got)
	}
}

func TestDependenciesRejectCycles(t *testing.T) {
	t.Parallel()
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

// postProcess plans once for the imported values, reads the schemas to
// find defaults among the constants, then plans once for all the steps,
// which keeps them (see verifySteps).
func TestPostProcessAddsReferences(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, referencingGenerated)
	tf := &fakeTerraform{dir: dir, shown: plannedImports(importedValues)}

	if _, err := postProcess(context.Background(), tf, dir, generating(Options{}), nil, nil); err != nil {
		t.Fatal(err)
	}

	if want := []string{"plan", "show", "schema", "plan", "show"}; !reflect.DeepEqual(tf.calls, want) {
		t.Errorf("calls: got %v, want %v", tf.calls, want)
	}
	if got := readFile(t, dir, rootfile.GeneratedFileName); !strings.Contains(got, "aws_vpc.main.id") {
		t.Errorf("no references:\n%s", got)
	}
}

func TestPostProcessUndoesReferencesThatChangeThePlan(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, referencingGenerated)
	// The imported values, then the plan of every step at once, then that
	// of the references alone.
	changed := fakePlan{summary: tfrun.ChangeSummary{Change: 1}}
	tf := &fakeTerraform{dir: dir, shown: plannedImports(importedValues), plans: []fakePlan{{}, changed, changed}}

	if _, err := postProcess(context.Background(), tf, dir, generating(Options{}), nil, nil); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, dir, rootfile.GeneratedFileName); strings.Contains(got, "aws_vpc.main.id") {
		t.Errorf("references kept although the plan changed:\n%s", got)
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

// Azure resources name their parents: an argument <type>_name refers to
// the resource of that type with that name whose ID starts the resource's
// own, not to one of the same name in another resource group.
func TestAddReferencesToParentsByName(t *testing.T) {
	t.Parallel()
	const sub = "/subscriptions/00000000-0000-0000-0000-000000000001"
	values := map[string]map[string]any{
		"azurerm_resource_group.app":         {"id": sub + "/resourceGroups/app", "name": "app"},
		"azurerm_resource_group.other":       {"id": sub + "/resourceGroups/other", "name": "other"},
		"azurerm_virtual_network.main":       {"id": sub + "/resourceGroups/app/providers/Microsoft.Network/virtualNetworks/main", "name": "main"},
		"azurerm_virtual_network.other_main": {"id": sub + "/resourceGroups/other/providers/Microsoft.Network/virtualNetworks/main", "name": "main"},
		// Resource Graph's IDs may have another case.
		"azurerm_subnet.web":                     {"id": sub + "/resourcegroups/APP/providers/Microsoft.Network/virtualNetworks/main/subnets/web", "name": "web"},
		"azurerm_network_security_group.fe":      {"id": sub + "/resourceGroups/app/providers/Microsoft.Network/networkSecurityGroups/fe", "name": "fe"},
		"azurerm_network_security_rule.fe_https": {"id": sub + "/resourceGroups/app/providers/Microsoft.Network/networkSecurityGroups/fe/securityRules/https", "name": "https"},
	}
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "azurerm_subnet" "web" {
  name                 = "web"
  resource_group_name  = "app"
  virtual_network_name = "main"
}

resource "azurerm_network_security_rule" "fe_https" {
  name                        = "https"
  network_security_group_name = "fe"
  resource_group_name         = "app"
}

resource "azurerm_virtual_network" "other_main" {
  name                = "main"
  resource_group_name = "other"
  # A name of no parent stays.
  dns_name = "main"
}
`)
	if changed, err := addReferences(dir, values); err != nil || !changed {
		t.Fatalf("want references, got changed=%v err=%v", changed, err)
	}
	got := readFile(t, dir, rootfile.GeneratedFileName)
	for _, want := range []string{
		"resource_group_name  = azurerm_resource_group.app.name",
		"virtual_network_name = azurerm_virtual_network.main.name",
		"network_security_group_name = azurerm_network_security_group.fe.name",
		"resource_group_name = azurerm_resource_group.other.name",
		`dns_name = "main"`,
		`name = "web"`,
	} {
		if !strings.Contains(strings.Join(strings.Fields(got), " "), strings.Join(strings.Fields(want), " ")) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// Google resources refer to one another by self link.
func TestAddReferencesBySelfLink(t *testing.T) {
	t.Parallel()
	const network = "https://www.googleapis.com/compute/v1/projects/acme/global/networks/main"
	values := map[string]map[string]any{
		"google_compute_network.main":   {"id": "projects/acme/global/networks/main", "name": "main", "self_link": network},
		"google_compute_subnetwork.app": {"id": "projects/acme/regions/us-central1/subnetworks/app", "name": "app", "self_link": "https://www.googleapis.com/compute/v1/projects/acme/regions/us-central1/subnetworks/app"},
	}
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "google_compute_subnetwork" "app" {
  name    = "app"
  network = "`+network+`"
}

resource "google_compute_firewall" "https" {
  name    = "https"
  network = "`+network+`"
}
`)
	if changed, err := addReferences(dir, values); err != nil || !changed {
		t.Fatalf("want references, got changed=%v err=%v", changed, err)
	}
	if got := readFile(t, dir, rootfile.GeneratedFileName); strings.Count(got, "network = google_compute_network.main.self_link") != 2 {
		t.Errorf("want both networks referenced:\n%s", got)
	}
	if index := referenceIndex(values); !reflect.DeepEqual(index[network], []referenceTarget{{address: "google_compute_network.main", attribute: "self_link"}}) {
		t.Errorf("index: %+v", index[network])
	}
}

// Literals in the objects of a list, such as a route table's routes, are
// references too; the other attributes stay as they were.
func TestAddReferencesInListsOfObjects(t *testing.T) {
	t.Parallel()
	values := map[string]map[string]any{
		"aws_internet_gateway.main": {"id": "igw-0abc12345", "arn": "arn:aws:ec2:us-east-1:123:internet-gateway/igw-0abc12345"},
	}
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "aws_route_table" "public" {
  route = [{
    cidr_block     = "0.0.0.0/0"
    gateway_id     = "igw-0abc12345"
    nat_gateway_id = ""
  }]
  vpc_id = "vpc-0abc1234"
}
`)
	if changed, err := addReferences(dir, values); err != nil || !changed {
		t.Fatalf("want references, got changed=%v err=%v", changed, err)
	}
	got := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	if !strings.Contains(got, `route = [{ cidr_block = "0.0.0.0/0" gateway_id = aws_internet_gateway.main.id nat_gateway_id = "" }]`) {
		t.Errorf("generated.tf:\n%s", got)
	}
}

// A service account's email and IAM member identify it: grants and
// settings that name it refer to it.
func TestAddReferencesToServiceAccounts(t *testing.T) {
	t.Parallel()
	const email = "ci@acme.iam.gserviceaccount.com"
	values := map[string]map[string]any{
		"google_service_account.ci": {"id": "projects/acme/serviceAccounts/" + email, "email": email, "member": "serviceAccount:" + email, "name": "projects/acme/serviceAccounts/" + email},
		// A grant has its member too, which doesn't make it the account.
		"google_project_iam_member.ci": {"id": "acme/roles/pubsub.subscriber/serviceAccount:" + email, "member": "serviceAccount:" + email},
	}
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "google_project_iam_member" "ci" {
  member  = "serviceAccount:`+email+`"
  project = "acme"
  role    = "roles/pubsub.subscriber"
}

resource "google_pubsub_subscription" "worker" {
  name  = "worker"
  topic = "events"
  push_config {
    push_endpoint = "https://example.com/push"
    oidc_token {
      service_account_email = "`+email+`"
    }
  }
}
`)
	if changed, err := addReferences(dir, values); err != nil || !changed {
		t.Fatalf("want references, got changed=%v err=%v", changed, err)
	}
	got := readFile(t, dir, rootfile.GeneratedFileName)
	for _, want := range []string{"member  = google_service_account.ci.member", "service_account_email = google_service_account.ci.email"} {
		if !strings.Contains(got, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, got)
		}
	}
}
