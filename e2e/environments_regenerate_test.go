// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// regenerateConfig is environmentsConfig with dev's account split by
// tags between dev and qa, which has no resources: what has neither's
// tags goes to the shared root of the account.
const regenerateConfig = `version: 1
environments:
  dev:
    aws: { accounts: ["777777777777"] }
    tags: { Environment: dev }
  qa:
    aws: { accounts: ["777777777777"] }
    tags: { Environment: qa }
  prod:
    aws: { accounts: ["888888888888"] }
    tokens: [prd]
`

// checkMergedRegenerate goes on from TestAWSEnvironments, whose merged
// root in out, aws/us-east-1, each environment has applied into its
// workspace, with fixture applied with the audit, reports and billing
// queues and dev's orders timeout of 90; prod's workspace doesn't have
// its billing queue yet, which its next apply imports. First, dev's scratch queue, added to the
// merged root with --incremental, is deleted: the next --incremental,
// with nothing new, leaves the merged root as it is and reports the queue
// no longer in the cloud, at its address in the merged root, as dev's.
// Then dev gets the alerts feature prod has, so the merged root no longer
// counts it, and dev's reports queue loses its tags, so that, with
// regenerateConfig, it goes to the shared root of dev's account:
// --regenerate gives the merged root a moved block for each of prod's
// alerts resources and a removed block for the reports queue, which the
// shared root imports. Applied in order, the merged root as each
// environment, then the shared root, every plan has no changes.
func checkMergedRegenerate(ctx context.Context, t *testing.T, fixture *appliedFixture, importInto func(out string, more ...string), credentials func(env string), out, execPath, plugins string) {
	t.Helper()
	dir := filepath.Join(out, "aws", "us-east-1")
	vars := []tfexec.ApplyOption{tfexec.Var("audit=true"), tfexec.Var("reports=true"), tfexec.Var("dev_orders_timeout=90"), tfexec.Var("billing=true")}

	fixture.apply(ctx, t, append(slices.Clone(vars), tfexec.Var("scratch=true"))...)
	importInto(out, "--incremental")
	coverage := coverageOf(t, out)
	checkCoverage(t, "--incremental with dev's scratch queue", coverage, nil)
	checkMerged(t, out, coverage, "aws/us-east-1", "dev", "prod")
	fixture.apply(ctx, t, vars...)
	importInto(out, "--incremental")
	coverage = coverageOf(t, out)
	checkCoverage(t, "--incremental after deleting dev's scratch queue", coverage, nil)
	if m := mergeOf(t, coverage, "aws/us-east-1"); !m.Merged || !m.Unchanged {
		t.Errorf("--incremental with nothing new: merged %v, unchanged %v (%s)", m.Merged, m.Unchanged, m.Reason)
	}
	if len(coverage.NotInCloud) != 1 {
		t.Fatalf("--incremental after deleting dev's scratch queue: no longer in the cloud %+v, want the queue", coverage.NotInCloud)
	}
	gone := coverage.NotInCloud[0]
	label, _, _ := strings.Cut(strings.TrimPrefix(gone.Address, "aws_sqs_queue."), "[")
	if gone.Path != "aws/us-east-1" || gone.Environment != "dev" || !strings.HasSuffix(gone.ID, "/shop-dev-scratch") || !strings.Contains(configuration(t, dir), `resource "aws_sqs_queue" "`+label+`"`) {
		t.Errorf("no longer in the cloud: got %+v, want dev's scratch queue at its address in the merged root", gone)
	}
	if md := readFile(t, filepath.Join(out, report.Dir, "report.md")); !strings.Contains(md, "`"+gone.Address+"`") || !strings.Contains(md, "of dev") {
		t.Errorf("report.md doesn't list %s as dev's, no longer in the cloud:\n%s", gone.Address, md)
	}

	fixture.apply(ctx, t, append(slices.Clone(vars), tfexec.Var("dev_alerts=true"), tfexec.Var("reports_tagged=false"))...)
	importInto(out, "--regenerate", "--config="+writeConfig(t, regenerateConfig))
	coverage = coverageOf(t, out)
	checkCoverage(t, "--regenerate", coverage, nil)
	checkMerged(t, out, coverage, "aws/us-east-1", "dev", "prod")
	shared := "envs/shared/aws/" + environmentAccounts["dev"] + "/us-east-1"
	if i := slices.IndexFunc(coverage.Departed, func(m report.Move) bool { return strings.HasSuffix(m.ID, "/shop-dev-reports") }); i < 0 {
		t.Errorf("--regenerate: dev's reports queue isn't handed over: departed %+v", coverage.Departed)
	} else if d := coverage.Departed[i]; d.FromRoot != "aws/us-east-1" || d.ToRoot != shared || d.To == "" {
		t.Errorf("--regenerate: dev's reports queue goes from %s (%s) to %s (%s), want from aws/us-east-1 to %s", d.FromRoot, d.From, d.ToRoot, d.To, shared)
	}
	if !slices.ContainsFunc(coverage.Moved, func(m report.Move) bool {
		return m.FromRoot == "aws/us-east-1" && strings.HasSuffix(m.ID, "/shop-prd-alerts") && m.From != m.To
	}) {
		t.Errorf("--regenerate: prod's alerts queue doesn't move, now that every environment has it: moved %+v", coverage.Moved)
	}
	if len(coverage.IncompleteHandovers) != 0 {
		t.Errorf("--regenerate: incomplete handovers %+v", coverage.IncompleteHandovers)
	}
	moves := readFile(t, filepath.Join(dir, engine.MovesFileName))
	for _, want := range []string{"moved {", "removed {", "destroy = false"} {
		if !strings.Contains(moves, want) {
			t.Errorf("the merged root's %s has no %q:\n%s", engine.MovesFileName, want, moves)
		}
	}
	if readme := readFile(t, filepath.Join(dir, engine.ReadmeFileName)); !strings.Contains(readme, "## Moved blocks") || !strings.Contains(readme, "## Handed over to other roots") || !strings.Contains(readme, "`"+shared+"`") {
		t.Errorf("the merged root's README doesn't list its moves and what it hands over:\n%s", readme)
	}

	// The merged root first, as it hands the reports queue over, then the
	// shared root, which imports it.
	for _, env := range []string{"dev", "prod"} {
		credentials(env)
		tf := workspace(ctx, t, dir, execPath, plugins, env)
		apply := []tfexec.ApplyOption{tfexec.Lock(false)}
		for _, f := range mergedVarFiles(dir, env) {
			apply = append(apply, tfexec.VarFile(f))
		}
		if err := tf.Apply(ctx, apply...); err != nil {
			t.Fatalf("%s: terraform apply, regenerated: %v", env, err)
		}
	}
	credentials("dev")
	sharedDir := filepath.Join(out, filepath.FromSlash(shared))
	applyRoot(ctx, t, sharedDir, execPath, plugins, nil)
	for _, env := range []string{"dev", "prod"} {
		credentials(env)
		tf := workspace(ctx, t, dir, execPath, plugins, env)
		planFile := filepath.Join(t.TempDir(), env+".tfplan")
		plan := []tfexec.PlanOption{tfexec.Out(planFile), tfexec.Lock(false)}
		for _, f := range mergedVarFiles(dir, env) {
			plan = append(plan, tfexec.VarFile(f))
		}
		if _, err := tf.Plan(ctx, plan...); err != nil {
			t.Fatalf("%s: terraform plan, applied: %v", env, err)
		}
		p, err := tf.ShowPlanFile(ctx, planFile)
		if err != nil {
			t.Fatal(err)
		}
		checkPlannedNothing(t, "the merged root as "+env, p)
	}
	credentials("dev")
	checkPlannedNothing(t, shared, planRoot(ctx, t, sharedDir, execPath, plugins))
}
