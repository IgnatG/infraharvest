// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// environmentAccounts are the accounts testdata/aws-environments creates
// its resources in, which no other test uses, by environment.
var environmentAccounts = map[string]string{"dev": "777777777777", "prod": "888888888888"}

// environmentsConfig maps environmentAccounts to their environments,
// prod's names holding prd.
const environmentsConfig = `version: 1
environments:
  dev:
    aws: { accounts: ["777777777777"] }
  prod:
    aws: { accounts: ["888888888888"] }
    tokens: [prd]
`

// TestAWSEnvironments imports testdata/aws-environments, in two accounts
// the configuration file maps to dev and prod, at once: the roots of the
// two environments merge into one root they share, in aws/us-east-1,
// with what differs in vars/<env>/. The queue of another size in each
// environment becomes a variable, prod's alerts feature a deploy_
// variable, and the queue configured too differently to merge stays
// each environment's. Planned as each environment, with its own
// credentials and var files, the merged root imports what that
// environment has and changes nothing. A layout that puts the
// environments' roots in the merged root's directory keeps them apart,
// and the report says why. --resume keeps the merged root as it is, and
// --incremental adds a queue dev gets since, as dev's, every environment
// still planning with no changes; with dev alone, it merges prod again as
// its root was kept, unplanned. Applied into each environment's
// workspace, the merged root is checked for drift as each environment,
// and a change dev accepts is written into dev's var file. From a fresh
// checkout of the output, without .infraharvest and with the import
// blocks deleted, --incremental with --managed-state on the environments'
// workspaces adds a queue prod gets since into the merged root, and
// nothing else. Imported again with an S3 backend, the merged root's
// drift is checked with each environment's backend settings (see
// checkEnvironmentsDrift). After that, the merged root is checked for
// what is no longer in the cloud, and generated again (see
// checkMergedRegenerate).
func TestAWSEnvironments(t *testing.T) {
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	fixture := seedApplied(ctx, t, "aws-environments", execPath, plugins)
	config := writeConfig(t, environmentsConfig)

	accounts := environmentAccounts["dev"] + "," + environmentAccounts["prod"]
	importInto := func(out string, more ...string) {
		t.Helper()
		root := cli.NewCmdRoot()
		root.SetArgs(append([]string{
			"import", "aws",
			"--config=" + config,
			"--engine=" + engineName,
			"--terraform-path=" + execPath,
			"--accounts=" + accounts,
			"--parallel=2",
			"--regions=us-east-1",
			"--services=sqs,sns",
			"--out=" + out,
			"--all",
		}, more...))
		if err := root.ExecuteContext(ctx); err != nil {
			t.Fatalf("infraharvest import %v: %v", more, err)
		}
	}
	// Each environment plans with its own account's credentials.
	credentials := func(env string) { t.Setenv("AWS_ACCESS_KEY_ID", environmentAccounts[env]) }
	defer t.Setenv("AWS_ACCESS_KEY_ID", "test")

	out := t.TempDir()
	importInto(out)
	coverage := coverageOf(t, out)
	checkCoverage(t, "the import", coverage, nil)
	merge := checkMerged(t, out, coverage, "aws/us-east-1", "dev", "prod")
	dir := filepath.Join(out, "aws", "us-east-1")
	for _, env := range []string{"dev", "prod"} {
		var residue []string
		for _, r := range merge.Residue {
			if r.Env == env {
				residue = append(residue, r.Address)
			}
		}
		if len(residue) != 1 || !strings.Contains(residue[0], "events") {
			t.Errorf("%s's residue: %v, want its events queue", env, residue)
		}
	}
	files := configuration(t, dir)
	for _, want := range []string{
		"region = var.aws_region",
		"Environment = var.env",
		`count = var.deploy_alerts ? 1 : 0`,
		"visibility_timeout_seconds = var.",
		`naming_prefix = "shop-${var.env}"`,
		// Each environment's events queue is named from a local too.
		`_name = "${local.naming_prefix}-events"`,
	} {
		if !strings.Contains(strings.Join(strings.Fields(files), " "), strings.Join(strings.Fields(want), " ")) {
			t.Errorf("the merged root misses %q:\n%s", want, files)
		}
	}
	for _, name := range []string{`"shop-dev-events"`, `"shop-prd-events"`} {
		if strings.Contains(files, name) {
			t.Errorf("the merged root names a queue %s, not from a local:\n%s", name, files)
		}
	}
	if strings.Contains(files, "assume_role") {
		t.Errorf("the merged root assumes a role of its own:\n%s", files)
	}
	prodVars := readFile(t, filepath.Join(dir, "vars", "prod", "terraform.tfvars"))
	if !strings.Contains(prodVars, `env = "prd"`) || !strings.Contains(prodVars, "deploy_alerts") {
		t.Errorf("prod's terraform.tfvars:\n%s", prodVars)
	}
	imported := checkMergedPlans(ctx, t, dir, execPath, plugins, credentials, "dev", "prod")
	if imported["dev"]["aws_sqs_queue"] != 5 || imported["prod"]["aws_sqs_queue"] != 6 || imported["prod"]["aws_sns_topic"] != 1 || imported["dev"]["aws_sns_topic"] != 0 {
		t.Errorf("imported: %v", imported)
	}

	// Resumed, the merged root stays as it is, and nothing is merged
	// again.
	before := outputFiles(t, out)
	importInto(out, "--resume")
	if m := mergeOf(t, coverageOf(t, out), "aws/us-east-1"); !m.Merged || !m.Unchanged {
		t.Errorf("--resume: merged %v, unchanged %v (%s)", m.Merged, m.Unchanged, m.Reason)
	}
	after := outputFiles(t, out)
	for name, content := range before {
		if strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tfvars") {
			if after[name] != content {
				t.Errorf("--resume changed %s", name)
			}
		}
	}

	// dev gets a queue, which --incremental adds as dev's.
	fixture.apply(ctx, t, tfexec.Var("audit=true"))
	importInto(out, "--incremental")
	coverage = coverageOf(t, out)
	checkCoverage(t, "--incremental", coverage, nil)
	merge = checkMerged(t, out, coverage, "aws/us-east-1", "dev", "prod")
	if !slices.ContainsFunc(merge.Residue, func(r report.MergeResidue) bool { return r.Env == "dev" && strings.Contains(r.MergedAddress, "audit") }) || !strings.Contains(readFile(t, filepath.Join(dir, "vars", "dev", "imports.tfvars")), "shop-dev-audit") {
		t.Errorf("--incremental didn't add dev's new queue:\n%s", readFile(t, filepath.Join(dir, "vars", "dev", "imports.tfvars")))
	}
	if prod := readFile(t, filepath.Join(dir, "vars", "prod", "imports.tfvars")); strings.Contains(prod, "shop-dev-audit") {
		t.Errorf("prod imports dev's new queue:\n%s", prod)
	}
	if files := configuration(t, dir); strings.Contains(files, `"shop-dev-audit"`) || !strings.Contains(files, `"${local.naming_prefix}-audit"`) {
		t.Errorf("--incremental: dev's new queue isn't named from a local:\n%s", files)
	}
	imported = checkMergedPlans(ctx, t, dir, execPath, plugins, credentials, "dev", "prod")
	if imported["dev"]["aws_sqs_queue"] != 6 || imported["prod"]["aws_sqs_queue"] != 6 {
		t.Errorf("--incremental: imported %v", imported)
	}

	// An incremental import of dev alone merges prod again from its kept
	// root, as it was, without planning as prod, and says so; prod's plan
	// stays the same.
	fixture.apply(ctx, t, tfexec.Var("audit=true"), tfexec.Var("reports=true"))
	accounts = environmentAccounts["dev"]
	importInto(out, "--incremental")
	accounts = environmentAccounts["dev"] + "," + environmentAccounts["prod"]
	coverage = coverageOf(t, out)
	checkCoverage(t, "--incremental of dev", coverage, nil)
	merge = checkMerged(t, out, coverage, "aws/us-east-1", "dev", "prod")
	if strings.Join(merge.Unplanned, ",") != "prod" {
		t.Errorf("--incremental of dev: unplanned %v, want prod", merge.Unplanned)
	}
	if dev := readFile(t, filepath.Join(dir, "vars", "dev", "imports.tfvars")); !strings.Contains(dev, "shop-dev-reports") {
		t.Errorf("--incremental of dev didn't add dev's reports queue:\n%s", dev)
	}
	imported = checkMergedPlans(ctx, t, dir, execPath, plugins, credentials, "dev", "prod")
	if imported["dev"]["aws_sqs_queue"] != 7 || imported["prod"]["aws_sqs_queue"] != 6 {
		t.Errorf("--incremental of dev: imported %v", imported)
	}

	// Each environment applies the merged root into its workspace, as the
	// README says for a root without a backend. Then dev's queue changes in
	// the cloud: drift as dev finds it, and accepts it into dev's var file
	// alone, as the environments share the code; drift as prod finds none.
	for _, env := range []string{"dev", "prod"} {
		credentials(env)
		tf, err := engine.NewTerraform(dir, execPath, plugins, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := tf.Init(ctx); err != nil {
			t.Fatalf("%s: terraform init: %v", env, err)
		}
		if err := tf.WorkspaceNew(ctx, env); err != nil {
			t.Fatalf("terraform workspace new %s: %v", env, err)
		}
		var vars []tfexec.ApplyOption
		for _, name := range []string{"common.tfvars", env + "/terraform.tfvars", env + "/imports.tfvars"} {
			vars = append(vars, tfexec.VarFile(filepath.Join(dir, "vars", filepath.FromSlash(name))))
		}
		if err := tf.Apply(ctx, vars...); err != nil {
			t.Fatalf("%s: terraform apply: %v", env, err)
		}
	}
	fixture.apply(ctx, t, tfexec.Var("audit=true"), tfexec.Var("reports=true"), tfexec.Var("dev_orders_timeout=90"))
	drift := driftRun{ctx: ctx, engineName: engineName, execPath: execPath, out: out}
	code := codeFiles(t, out)
	credentials("dev")
	d := drift.mustExit(t, report.ExitDrift, "--detailed-exitcode", "--environment=dev")
	if len(d.Roots) != 1 || d.Roots[0].Path != "aws/us-east-1" || d.Roots[0].Environment != "dev" || d.Roots[0].Resources == 0 {
		t.Errorf("drift as dev: roots %+v", d.Roots)
	}
	timeout := driftChange(t, "drift as dev", d, "aws_sqs_queue", "visibility_timeout_seconds")
	checkDriftChange(t, "drift as dev", timeout, "30", "90", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	if timeout.Environment != "dev" {
		t.Errorf("drift as dev: the change is of %q", timeout.Environment)
	}
	// Accepted, the timeout dev's var file gives its variable takes the
	// cloud's value; the code and prod's var files stay as they were.
	files = configuration(t, dir)
	variable := regexp.MustCompile(`visibility_timeout_seconds\s*=\s*var\.(\w+)`).FindStringSubmatch(files)
	if variable == nil {
		t.Fatalf("no variable sets the orders queue's timeout:\n%s", files)
	}
	devVars := "aws/us-east-1/vars/dev/terraform.tfvars"
	varFiles := map[string]string{}
	for name, content := range outputFiles(t, out) {
		if strings.HasSuffix(name, ".tfvars") || strings.HasSuffix(name, ".hcl") {
			varFiles[name] = content
		}
	}
	// Dev's timeout is the variable's default, or dev's var file sets it.
	if set := regexp.MustCompile(`(?m)^\s*` + variable[1] + `\s*=\s*(\S+)\s*$`).FindStringSubmatch(varFiles[devVars]); set != nil && set[1] != "30" {
		t.Fatalf("dev's terraform.tfvars sets %s to %s, want 30:\n%s", variable[1], set[1], varFiles[devVars])
	}
	d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode", "--environment=dev", "--accept=all")
	accepted := driftChange(t, "drift as dev, accepted", d, "aws_sqs_queue", "visibility_timeout_seconds")
	checkDriftChange(t, "drift as dev, accepted", accepted, "30", "90", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	if accepted.Source == nil || accepted.Source.File != "vars/dev/terraform.tfvars" || accepted.Source.Line == 0 {
		t.Errorf("drift as dev, accepted: the source is %+v, want dev's terraform.tfvars", accepted.Source)
	}
	checkCodeUnchanged(t, "drift as dev, accepted", code, out)
	after = outputFiles(t, out)
	for name, content := range varFiles {
		switch {
		case name != devVars && after[name] != content:
			t.Errorf("drift as dev, accepted: changed %s:\n%s", name, after[name])
		case name == devVars && !regexp.MustCompile(`(?m)^\s*`+variable[1]+`\s*=\s*90\s*$`).MatchString(after[name]):
			t.Errorf("drift as dev, accepted: dev's terraform.tfvars doesn't set %s to 90:\n%s", variable[1], after[name])
		}
	}
	// Drift doesn't write the state: the code now absorbs the change.
	d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode", "--environment=dev")
	if c := driftChange(t, "drift as dev, written", d, "aws_sqs_queue", "visibility_timeout_seconds"); c.Outcome != report.DriftAbsorbed {
		t.Errorf("drift as dev, written: the timeout is %s (%s), want %s", c.Outcome, c.Reason, report.DriftAbsorbed)
	}
	credentials("prod")
	if d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode", "--environment=prod"); len(d.Changes) != 0 {
		t.Errorf("drift as prod:\n%s", driftChanges(d))
	}

	// A fresh checkout of the output has no .infraharvest, which its
	// .gitignore leaves out, and, applied, a team deletes imports.tf and
	// each environment's imports.tfvars. prod gets a queue: --incremental,
	// with --managed-state reading each environment's workspace state,
	// tells the merged root from its files and adds the queue into it as
	// prod's, and nothing else. Planned in its workspace, each environment
	// changes nothing, prod importing its new queue alone.
	had := resourceAddresses(t, dir)
	for _, path := range []string{
		filepath.Join(out, ".infraharvest"),
		filepath.Join(dir, "imports.tf"),
		filepath.Join(dir, "vars", "dev", "imports.tfvars"),
		filepath.Join(dir, "vars", "prod", "imports.tfvars"),
	} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	fixture.apply(ctx, t, tfexec.Var("audit=true"), tfexec.Var("reports=true"), tfexec.Var("dev_orders_timeout=90"), tfexec.Var("billing=true"))
	importInto(out, "--incremental", "--managed-state="+filepath.Join(dir, "terraform.tfstate.d"))
	coverage = coverageOf(t, out)
	checkCoverage(t, "--incremental from a fresh checkout", coverage, nil)
	merge = checkMerged(t, out, coverage, "aws/us-east-1", "dev", "prod")
	if len(merge.Unplanned) != 0 {
		t.Errorf("--incremental from a fresh checkout: unplanned %v", merge.Unplanned)
	}
	var added []string
	now := resourceAddresses(t, dir)
	for address := range now {
		if !had[address] {
			added = append(added, address)
		}
	}
	for address := range had {
		if !now[address] {
			t.Errorf("--incremental from a fresh checkout: %s is gone from the merged root", address)
		}
	}
	if len(added) != 1 || !strings.Contains(added[0], "billing") {
		t.Errorf("--incremental from a fresh checkout added %v, want prod's billing queue alone", added)
	}
	if !slices.ContainsFunc(merge.Residue, func(r report.MergeResidue) bool {
		return r.Env == "prod" && strings.Contains(r.MergedAddress, "billing")
	}) {
		t.Errorf("--incremental from a fresh checkout: prod's billing queue isn't prod's: %+v", merge.Residue)
	}
	if prod := readFile(t, filepath.Join(dir, "vars", "prod", "imports.tfvars")); !strings.Contains(prod, "shop-prd-billing") {
		t.Errorf("prod doesn't import its billing queue:\n%s", prod)
	}
	if dev := readFile(t, filepath.Join(dir, "vars", "dev", "imports.tfvars")); strings.Contains(dev, "shop-prd-billing") {
		t.Errorf("dev imports prod's billing queue:\n%s", dev)
	}
	for _, env := range []string{"dev", "prod"} {
		credentials(env)
		imports := checkWorkspacePlan(ctx, t, dir, execPath, plugins, env)
		switch {
		case env == "dev" && len(imports) != 0:
			t.Errorf("dev's workspace plan imports %v, which its state has", imports)
		case env == "prod" && (len(imports) != 1 || !strings.Contains(imports[0], "billing")):
			t.Errorf("prod's workspace plan imports %v, want its billing queue alone", imports)
		}
	}
	// Planned with no state, as the gate does, each environment imports
	// everything it has and changes nothing.
	selectWorkspace(ctx, t, dir, execPath, plugins, "default")
	imported = checkMergedPlans(ctx, t, dir, execPath, plugins, credentials, "dev", "prod")
	if imported["dev"]["aws_sqs_queue"] != 7 || imported["prod"]["aws_sqs_queue"] != 7 || imported["prod"]["aws_sns_topic"] != 1 {
		t.Errorf("--incremental from a fresh checkout: imported %v", imported)
	}

	// A layout that puts the environments' roots in the merged root's
	// directory keeps them apart, each planning as it was generated.
	apart := t.TempDir()
	importInto(apart, "--layout={out}/{provider}/{region}/{env}/{account}/")
	coverage = coverageOf(t, apart)
	checkCoverage(t, "the layout", coverage, nil)
	if m := mergeOf(t, coverage, "aws/us-east-1"); m.Merged || !strings.Contains(m.Reason, "merged root's directory") {
		t.Errorf("the layout: merged %v, %q", m.Merged, m.Reason)
	}
	for env, account := range environmentAccounts {
		credentials(env)
		checkNoChanges(ctx, t, filepath.Join(apart, "aws", "us-east-1", env, account), execPath, plugins, nil)
	}

	checkEnvironmentsDrift(ctx, t, fixture, engineName, execPath, plugins, credentials)

	// Not in the cloud, then --regenerate, on the merged root. After the
	// drift step, which imports again into an output of its own: the
	// regenerate step gives dev the alerts feature, which that step needs
	// prod alone to have.
	checkMergedRegenerate(ctx, t, fixture, importInto, credentials, out, execPath, plugins)
}

// TestAWSEnvironmentsData imports testdata/aws-environments-data, in two
// accounts the configuration file maps to dev and prod, at once, with a
// catalogue overlay that leaves prod's alerts policy out: the role of
// prod's alerts feature reads the policy through a data source, which
// only prod's root has. The merged root counts the data source with the
// feature's deploy_ variable, and refers to data.aws_iam_policy.<name>[0];
// planned as each environment, it imports what that environment has and
// changes nothing.
func TestAWSEnvironmentsData(t *testing.T) {
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	seedApplied(ctx, t, "aws-environments-data", execPath, plugins)
	accounts := map[string]string{"dev": "717171717171", "prod": "818181818181"}
	config := writeConfig(t, `version: 1
environments:
  dev:
    aws: { accounts: ["717171717171"] }
  prod:
    aws: { accounts: ["818181818181"] }
    tokens: [prd]
`)
	overlay, err := filepath.Abs(filepath.Join("testdata", "catalogue", "environments-data"))
	if err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	root := cli.NewCmdRoot()
	root.SetArgs([]string{
		"import", "aws",
		"--config=" + config,
		"--engine=" + engineName,
		"--terraform-path=" + execPath,
		"--accounts=" + accounts["dev"] + "," + accounts["prod"],
		"--parallel=2",
		"--regions=us-east-1",
		"--services=iam",
		// Resources of the root, so that the attachment that reads the
		// data source is one of its own.
		"--modules=none",
		"--catalogue=" + overlay,
		"--out=" + out,
		"--all",
	})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("infraharvest import: %v", err)
	}
	coverage := coverageOf(t, out)
	checkCoverage(t, "the import", coverage, nil)
	checkMerged(t, out, coverage, "aws/global", "dev", "prod")
	dir := filepath.Join(out, "aws", "global")

	data := readFile(t, filepath.Join(dir, "data.tf"))
	gate := regexp.MustCompile(`data "aws_iam_policy" "(\w+)" \{\s*count\s*=\s*var\.(deploy_\w+) \? 1 : 0\s`).FindStringSubmatch(data)
	if gate == nil {
		t.Fatalf("data.tf doesn't count the alerts policy with a deploy_ variable:\n%s", data)
	}
	files := configuration(t, dir)
	if !strings.Contains(files, "data.aws_iam_policy."+gate[1]+"[0].") {
		t.Errorf("the merged root doesn't refer to data.aws_iam_policy.%s[0]:\n%s", gate[1], files)
	}
	attachment := regexp.MustCompile(`resource "aws_iam_role_policy_attachment" "\w+" \{\s*count\s*=\s*var\.` + gate[2] + ` \? 1 : 0\s`)
	if !attachment.MatchString(files) {
		t.Errorf("the alerts role's policy attachment isn't counted by %s, as its data source is:\n%s", gate[2], files)
	}
	if prod := readFile(t, filepath.Join(dir, "vars", "prod", "terraform.tfvars")); !regexp.MustCompile(`(?m)^\s*` + gate[2] + `\s*=\s*true\s*$`).MatchString(prod) {
		t.Errorf("prod's terraform.tfvars doesn't set %s:\n%s", gate[2], prod)
	}
	if dev := readFile(t, filepath.Join(dir, "vars", "dev", "terraform.tfvars")); regexp.MustCompile(`(?m)^\s*` + gate[2] + `\s*=\s*true\s*$`).MatchString(dev) {
		t.Errorf("dev's terraform.tfvars sets %s:\n%s", gate[2], dev)
	}

	// Each environment plans with its own account's credentials.
	defer t.Setenv("AWS_ACCESS_KEY_ID", "test")
	credentials := func(env string) { t.Setenv("AWS_ACCESS_KEY_ID", accounts[env]) }
	imported := checkMergedPlans(ctx, t, dir, execPath, plugins, credentials, "dev", "prod")
	if imported["prod"]["aws_iam_role_policy_attachment"] != 1 || imported["dev"]["aws_iam_role_policy_attachment"] != 0 || imported["prod"]["aws_iam_policy"] != 0 {
		t.Errorf("imported: %v", imported)
	}
}
