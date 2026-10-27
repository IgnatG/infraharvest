// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

// envMain is the code of a root dev and prod share: the orders queue's
// timeout is each environment's, and var.env reaches several arguments.
const envMain = `variable "env" {
  description = "The token that stands for the environment in names."
  type        = string
}

variable "orders_timeout" {
  description = "The visibility timeout of the orders queue."
  type        = number
}

resource "aws_sqs_queue" "orders" {
  name                       = "shop-${var.env}-orders"
  visibility_timeout_seconds = var.orders_timeout
  tags = {
    Environment = var.env
  }
}

resource "aws_sqs_queue" "jobs" {
  name          = "shop-${var.env}-jobs"
  delay_seconds = 5
}
`

const (
	devVars  = "# dev's values.\nenv            = \"dev\"\norders_timeout = 30\n"
	prodVars = "env            = \"prd\"\norders_timeout = 120\n"
)

// sharedDriftRoot writes a root dev and prod share, with main and dev's var file,
// and dev's local state, and returns it and where Drift stages it.
func sharedDriftRoot(t *testing.T, main, dev string) (dir, staging string) {
	t.Helper()
	return acceptRoot(t, map[string]string{
		"main.tf":                                   main,
		"vars/dev/terraform.tfvars":                 dev,
		"vars/prod/terraform.tfvars":                prodVars,
		"terraform.tfstate.d/dev/terraform.tfstate": driftState,
	})
}

// queueUpdate is an update of the queue at address from before to after.
func queueUpdate(address string, before, after map[string]any) *tfjson.ResourceChange {
	return driftResource(address, "aws_sqs_queue", tfjson.Actions{tfjson.ActionUpdate}, before, after)
}

// ordersQueue is the orders queue with timeout and its Environment tag.
func ordersQueue(timeout float64, env string) map[string]any {
	tags := map[string]any{"Environment": env}
	return map[string]any{"visibility_timeout_seconds": timeout, "tags": tags, "tags_all": tags}
}

// envPlans are the plans of dev's queues: in the cloud, the orders queue's
// timeout and Environment tag and the jobs queue's delay changed, which
// the code reverts; the plan of the edits, verified, as ordersVerified
// has the orders queue.
func envPlans(ordersVerified *tfjson.ResourceChange) []*tfjson.Plan {
	jobs := queueUpdate("aws_sqs_queue.jobs", map[string]any{"delay_seconds": float64(10)}, map[string]any{"delay_seconds": float64(5)})
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
		queueUpdate("aws_sqs_queue.orders", ordersQueue(30, "dev"), ordersQueue(90, "qa")),
		queueUpdate("aws_sqs_queue.jobs", map[string]any{"delay_seconds": float64(5)}, map[string]any{"delay_seconds": float64(10)}),
	}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		queueUpdate("aws_sqs_queue.orders", ordersQueue(90, "qa"), ordersQueue(30, "dev")),
		jobs,
	}}
	verified := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{ordersVerified, jobs}}
	return []*tfjson.Plan{refresh, code, verified}
}

// tfvarsValues returns the variables a var file sets, as HCL writes them.
func tfvarsValues(t *testing.T, content string) map[string]string {
	t.Helper()
	f, diags := hclsyntax.ParseConfig([]byte(content), "terraform.tfvars", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parse:\n%s\n%v", content, diags)
	}
	values := map[string]string{}
	for name, attr := range f.Body.(*hclsyntax.Body).Attributes {
		rng := attr.Expr.Range()
		values[name] = content[rng.Start.Byte:rng.End.Byte]
	}
	return values
}

// For a root environments share, an accepted change a variable sets is
// written into the environment's var file, which the staged copy plans
// with, once its plan takes it; the code, and the other environments'
// var files, stay as they were, and changes in shared code, or of a
// variable other arguments use, need a hand edit.
func TestDriftWritesAVariableIntoTheEnvironmentsVarFile(t *testing.T) {
	t.Parallel()
	dir, staging := sharedDriftRoot(t, envMain, devVars)
	// The edited plan takes the timeout; the tag still reverts, as the code
	// did.
	verified := queueUpdate("aws_sqs_queue.orders", ordersQueue(90, "qa"), ordersQueue(90, "dev"))
	result, copied, _, err := acceptAll(t, dir, staging, envPlans(verified), DriftOptions{Environment: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(result)
	timeout := got["aws_sqs_queue.orders visibility_timeout_seconds"]
	if timeout.Outcome != DriftWritten {
		t.Errorf("the timeout: %s: %s", timeout.Outcome, timeout.Reason)
	}
	if s := timeout.Source; s == nil || s.File != "vars/dev/terraform.tfvars" || s.Line != 3 {
		t.Errorf("the timeout's source: %+v, want vars/dev/terraform.tfvars:3", s)
	}
	if c := got["aws_sqs_queue.orders tags.Environment"]; c.Outcome != DriftManual || !strings.Contains(c.Reason, "other arguments use too") {
		t.Errorf("the tag: %s: %s", c.Outcome, c.Reason)
	}
	if c := got["aws_sqs_queue.jobs delay_seconds"]; c.Outcome != DriftManual || !strings.Contains(c.Reason, "environments share this root's code") {
		t.Errorf("the delay: %s: %s", c.Outcome, c.Reason)
	}
	if got, want := readFile(t, dir, filepath.Join("vars", "dev", "terraform.tfvars")), strings.Replace(devVars, "= 30", "= 90", 1); got != want {
		t.Errorf("dev's terraform.tfvars:\n%s\nwant:\n%s", got, want)
	}
	if got := readFile(t, dir, filepath.Join("vars", "prod", "terraform.tfvars")); got != prodVars {
		t.Errorf("prod's terraform.tfvars changed:\n%s", got)
	}
	if got := readFile(t, dir, "main.tf"); got != envMain {
		t.Errorf("main.tf changed:\n%s", got)
	}
	staged := tfexec.PlanOption(tfexec.VarFile(filepath.Join(staging, "vars", "dev", "terraform.tfvars")))
	if len(copied.planOpts) != 3 {
		t.Fatalf("%d plans, want 3", len(copied.planOpts))
	}
	for i, opts := range copied.planOpts {
		if !hasOption(opts, staged) {
			t.Errorf("plan %d doesn't read the staged copy of dev's var file: %#v", i, opts)
		}
	}
}

// A variable dev's var file doesn't set, which its default gives dev, is
// added to the file.
func TestDriftAddsAVariableToTheEnvironmentsVarFile(t *testing.T) {
	t.Parallel()
	main := strings.Replace(envMain, "  type        = number\n", "  type        = number\n  default     = 30\n", 1)
	dir, staging := sharedDriftRoot(t, main, `env = "dev"`)
	verified := queueUpdate("aws_sqs_queue.orders", ordersQueue(90, "qa"), ordersQueue(90, "dev"))
	result, _, _, err := acceptAll(t, dir, staging, envPlans(verified), DriftOptions{Environment: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	timeout := outcomes(result)["aws_sqs_queue.orders visibility_timeout_seconds"]
	if timeout.Outcome != DriftWritten {
		t.Errorf("the timeout: %s: %s", timeout.Outcome, timeout.Reason)
	}
	if s := timeout.Source; s == nil || s.File != "vars/dev/terraform.tfvars" || s.Line != 2 {
		t.Errorf("the timeout's source: %+v, want vars/dev/terraform.tfvars:2", s)
	}
	content := readFile(t, dir, filepath.Join("vars", "dev", "terraform.tfvars"))
	if values := tfvarsValues(t, content); len(values) != 2 || values["env"] != `"dev"` || values["orders_timeout"] != "90" {
		t.Errorf("dev's terraform.tfvars:\n%s", content)
	}
	if got := readFile(t, dir, "main.tf"); got != main {
		t.Errorf("main.tf changed:\n%s", got)
	}
}

// A value the plan of the edited var file doesn't take isn't written.
func TestDriftRejectsAVarFileEditThePlanDoesntTake(t *testing.T) {
	t.Parallel()
	dir, staging := sharedDriftRoot(t, envMain, devVars)
	verified := queueUpdate("aws_sqs_queue.orders", ordersQueue(90, "qa"), ordersQueue(30, "dev"))
	result, _, _, err := acceptAll(t, dir, staging, envPlans(verified), DriftOptions{Environment: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if c := outcomes(result)["aws_sqs_queue.orders visibility_timeout_seconds"]; c.Outcome != DriftRejectedByPlan || !strings.Contains(c.Reason, "still changes visibility_timeout_seconds") {
		t.Errorf("the timeout: %s: %s", c.Outcome, c.Reason)
	}
	if got := readFile(t, dir, filepath.Join("vars", "dev", "terraform.tfvars")); got != devVars {
		t.Errorf("dev's terraform.tfvars changed:\n%s", got)
	}
}

// An environment whose backend.hcl isn't there has no state to pull: it
// says so, and initialises nothing.
func TestPullDriftStateWithoutBackendSettings(t *testing.T) {
	t.Parallel()
	dir := mergedRootDir(t, map[string]string{"vars/prod/backend.hcl": "bucket = \"acme-state\"\n"})
	var calls []string
	tf := &fakeDriftTerraform{name: "root", calls: &calls, state: driftState}

	_, err := pullDriftState(context.Background(), tf, dir, "s3", "dev")

	if err == nil || !strings.Contains(err.Error(), "the s3 backend's settings for dev") || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("got %v, want dev's missing backend settings", err)
	}
	if len(calls) != 0 {
		t.Errorf("calls: %v, want none", calls)
	}
}
