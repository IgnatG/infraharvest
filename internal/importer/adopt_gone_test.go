// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/managed"
)

// writeGoneStack writes, in out/aws/eu-west-2, a root dev and prod share,
// with no checkpoint, as in a fresh checkout: a queue both have, orders,
// and one of dev alone, audit, with each environment's imports.tfvars.
func writeGoneStack(t *testing.T, out string) string {
	t.Helper()
	stack := filepath.Join(out, "aws", "eu-west-2")
	writeRootFiles(t, stack, map[string]string{
		"variables.tf": "variable \"env\" {\n  type = string\n}\n\nvariable \"deploy_audit\" {\n  type    = bool\n  default = false\n}\n",
		"locals.tf":    "locals {\n  naming_prefix = \"shop-${var.env}\"\n}\n",
		"sqs.tf":       "resource \"aws_sqs_queue\" \"orders\" {\n  name = \"${local.naming_prefix}-orders\"\n}\n\nresource \"aws_sqs_queue\" \"audit\" {\n  count = var.deploy_audit ? 1 : 0\n\n  name = \"${local.naming_prefix}-audit\"\n}\n",
	})
	writeRootFiles(t, filepath.Join(stack, "vars"), map[string]string{"common.tfvars": "# none\n"})
	writeRootFiles(t, filepath.Join(stack, "vars", "dev"), map[string]string{
		"terraform.tfvars": "env          = \"dev\"\ndeploy_audit = true\n",
		"imports.tfvars":   "import_ids = {\n  \"aws_sqs_queue.orders\"   = \"q-dev-orders\"\n  \"aws_sqs_queue.audit[0]\" = \"q-dev-audit\"\n}\n",
	})
	writeRootFiles(t, filepath.Join(stack, "vars", "prod"), map[string]string{
		"terraform.tfvars": "env = \"prd\"\n",
		"imports.tfvars":   "import_ids = {\n  \"aws_sqs_queue.orders\" = \"q-prd-orders\"\n}\n",
	})
	return stack
}

// goneMember returns the member of env the import generated afresh in
// out, a root of the queues of ids, by their labels there, whose listing
// lists every queue, and found those of listed.
func goneMember(t *testing.T, out, env, account string, ids map[string]string, listed ...string) mergeMember {
	t.Helper()
	dir := filepath.Join(out, "envs", env, "aws", account, "eu-west-2")
	var queues, imports strings.Builder
	result := &engine.Result{}
	for _, label := range slices.Sorted(maps.Keys(ids)) {
		fmt.Fprintf(&queues, "resource \"aws_sqs_queue\" %q {\n  name = %q\n}\n\n", label, ids[label])
		fmt.Fprintf(&imports, "import {\n  to = aws_sqs_queue.%s\n  id = %q\n}\n\n", label, ids[label])
		result.Addresses = append(result.Addresses, engine.Managed{Type: "aws_sqs_queue", ID: ids[label], Address: "aws_sqs_queue." + label})
	}
	writeRootFiles(t, dir, map[string]string{engine.VersionsFileName: "# versions\n", "sqs.tf": queues.String(), engine.ImportsFileName: imports.String()})
	here := &listedHere{ids: map[string]bool{}, types: map[string]bool{"aws_sqs_queue": true}}
	for _, id := range listed {
		here.ids[strings.ToLower(id)] = true
	}
	return mergeMember{env: env, dir: dir, stack: filepath.Join(out, "aws", "eu-west-2"), scope: "aws/" + account + "/eu-west-2", result: result, here: here}
}

// checkKeptGone checks that keepGone put back into m, under out, the
// queue of the merged root at label, of ID id, as m's root would have it,
// named name, and recorded it no longer in the cloud there.
func checkKeptGone(t *testing.T, run *Run, out string, m mergeMember, label, id, name string) {
	t.Helper()
	sqs := readTestFile(t, filepath.Join(m.dir, "sqs.tf"))
	if !strings.Contains(sqs, `resource "aws_sqs_queue" "`+label+`"`) || !strings.Contains(sqs, `"`+name+`"`) || strings.Contains(sqs, "count") || strings.Contains(sqs, "var.") || strings.Contains(sqs, "local.") {
		t.Errorf("%s's sqs.tf:\n%s", m.env, sqs)
	}
	if imports := readTestFile(t, filepath.Join(m.dir, engine.ImportsFileName)); !strings.Contains(imports, "aws_sqs_queue."+label+"\n") || !strings.Contains(imports, `"`+id+`"`) {
		t.Errorf("%s's imports.tf:\n%s", m.env, imports)
	}
	if got := run.goneFrom(m.dir); !reflect.DeepEqual(got, []string{"aws_sqs_queue." + label}) {
		t.Errorf("%s: gone %v", m.env, got)
	}
	if !slices.ContainsFunc(resultResources(m.result), func(r engine.Managed) bool { return r.ID == id && r.Address == "aws_sqs_queue."+label }) {
		t.Errorf("%s's result: %+v", m.env, m.result)
	}
	if g := run.report.NotInCloud; len(g) != 1 || g[0].Path != relativePath(out, m.dir) || g[0].ID != id || g[0].Scope != m.scope {
		t.Errorf("not in the cloud: %+v", g)
	}
}

// readTestFile returns the content of the file at path.
func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// From a fresh checkout, with nothing new, dev's own queue, gone from the
// cloud, is put back into dev's root generated afresh, as the merged root
// has it, so that merging again keeps it; where the listing can't tell,
// it isn't, and the merge refuses to leave it out (see leftOutOfMerge).
func TestKeepGoneFromAFreshCheckout(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	stack := writeGoneStack(t, out)
	run := NewRun()
	run.options = Options{Out: out, Incremental: true}
	dev := goneMember(t, out, "dev", "111111111111", map[string]string{"shop_dev_orders": "q-dev-orders"}, "q-dev-orders")
	prod := goneMember(t, out, "prod", "222222222222", map[string]string{"shop_prd_orders": "q-prd-orders"}, "q-prd-orders")
	members := []mergeMember{dev, prod}
	prev, why, err := run.adoptedCheckpoint(stack, members)
	if err != nil || why != "" {
		t.Fatalf("%q %v", why, err)
	}
	if why, err := run.keepGone(stack, prev, members); err != nil || why != "" {
		t.Fatalf("%q %v", why, err)
	}
	checkKeptGone(t, run, out, members[0], "audit", "q-dev-audit", "shop-dev-audit")
	if sqs := readTestFile(t, filepath.Join(prod.dir, "sqs.tf")); strings.Contains(sqs, "audit") {
		t.Errorf("prod's root gets dev's queue:\n%s", sqs)
	}

	// Where the listing doesn't list every queue, the queue isn't gone.
	other := t.TempDir()
	stack = writeGoneStack(t, other)
	dev = goneMember(t, other, "dev", "111111111111", map[string]string{"shop_dev_orders": "q-dev-orders"}, "q-dev-orders")
	dev.here.types = map[string]bool{}
	run = NewRun()
	run.options = Options{Out: other, Incremental: true}
	members = []mergeMember{dev, goneMember(t, other, "prod", "222222222222", map[string]string{"shop_prd_orders": "q-prd-orders"}, "q-prd-orders")}
	if why, err := run.keepGone(stack, prev, members); err != nil || why != "" || len(run.report.NotInCloud) != 0 {
		t.Errorf("listed in part: %q %v %+v", why, err, run.report.NotInCloud)
	}
	if sqs := readTestFile(t, filepath.Join(dev.dir, "sqs.tf")); strings.Contains(sqs, "audit") {
		t.Errorf("listed in part, dev's root gets the queue:\n%s", sqs)
	}
}

// From a fresh checkout, the queue dev and prod share, gone from dev's
// cloud, is put back into dev's root as prod gets a new queue, which the
// merge keeps apart as prod's.
func TestKeepGoneWithWhatIsNew(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	stack := writeGoneStack(t, out)
	run := NewRun()
	run.options = Options{Out: out, Incremental: true}
	dev := goneMember(t, out, "dev", "111111111111", map[string]string{"shop_dev_audit": "q-dev-audit"}, "q-dev-audit")
	prod := goneMember(t, out, "prod", "222222222222", map[string]string{"shop_prd_orders": "q-prd-orders", "shop_prd_billing": "q-prd-billing"}, "q-prd-orders", "q-prd-billing")
	members := []mergeMember{dev, prod}
	prev, why, err := run.adoptedCheckpoint(stack, members)
	if err != nil || why != "" {
		t.Fatalf("%q %v", why, err)
	}
	if got := newSinceMerge(prod.result, prev.Imports["prod"]); !reflect.DeepEqual(got, []string{"aws_sqs_queue.shop_prd_billing"}) {
		t.Errorf("new since the merge: %v", got)
	}
	if why, err := run.keepGone(stack, prev, members); err != nil || why != "" {
		t.Fatalf("%q %v", why, err)
	}
	checkKeptGone(t, run, out, members[0], "orders", "q-dev-orders", "shop-dev-orders")
	if got := newSinceMerge(members[1].result, prev.Imports["prod"]); !reflect.DeepEqual(got, []string{"aws_sqs_queue.shop_prd_billing"}) {
		t.Errorf("new since the merge, kept: %v", got)
	}
}

// From a fresh checkout, once prod's imports.tfvars is deleted after its
// first apply, what prod's own state has, read with --managed-state, is
// the merged root's: its own queue, which this run doesn't list, is put
// back into prod's root rather than left out.
func TestKeepGoneFromManagedState(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	stack := writeGoneStack(t, out)
	writeRootFiles(t, filepath.Join(stack, "vars", "prod"), map[string]string{
		"terraform.tfvars": "env          = \"prd\"\ndeploy_audit = true\n",
		"backend.hcl":      "bucket = \"acme-state\"\nkey = \"envs/prod/aws/222222222222/eu-west-2/terraform.tfstate\"\nregion = \"eu-west-2\"\n",
	})
	if err := os.Remove(filepath.Join(stack, "vars", "prod", "imports.tfvars")); err != nil {
		t.Fatal(err)
	}
	state := managed.Resources{}
	content := `{"version": 4, "resources": [
	  {"mode": "managed", "type": "aws_sqs_queue", "name": "orders", "instances": [{"attributes": {"id": "q-prd-orders"}}]},
	  {"mode": "managed", "type": "aws_sqs_queue", "name": "audit", "instances": [{"index_key": 0, "attributes": {"id": "q-prd-audit", "arn": "arn:aws:sqs:eu-west-2:222222222222:shop-prd-audit"}}]}
	]}`
	if err := state.Parse([]byte(content), "s3://acme-state/envs/prod/aws/222222222222/eu-west-2/terraform.tfstate"); err != nil {
		t.Fatal(err)
	}
	run := NewRun()
	run.options = Options{Out: out, Incremental: true, ManagedState: []string{"backend"}}
	run.shared.managed = map[string]managed.Resources{"s3://acme-state/?region=eu-west-2": state}
	dev := goneMember(t, out, "dev", "111111111111", map[string]string{"shop_dev_orders": "q-dev-orders", "shop_dev_audit": "q-dev-audit"}, "q-dev-orders", "q-dev-audit")
	prod := goneMember(t, out, "prod", "222222222222", map[string]string{"shop_prd_orders": "q-prd-orders"}, "q-prd-orders")
	members := []mergeMember{dev, prod}
	prev, why, err := run.adoptedCheckpoint(stack, members)
	if err != nil || why != "" {
		t.Fatalf("%q %v", why, err)
	}
	if got := prev.Imports["prod"]["aws_sqs_queue.audit[0]"]; got != "q-prd-audit" {
		t.Errorf("prod's imports: %v", prev.Imports["prod"])
	}
	if why, err := run.keepGone(stack, prev, members); err != nil || why != "" {
		t.Fatalf("%q %v", why, err)
	}
	checkKeptGone(t, run, out, members[1], "audit", "q-prd-audit", "shop-prd-audit")
}
