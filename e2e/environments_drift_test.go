// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/driftpolicy"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/report"
)

// environmentsBackendConfig is environmentsConfig with an S3 backend in
// dev's state bucket, which testdata/aws-environments creates; prod's
// backend settings are pointed at prod's own bucket once generated.
const environmentsBackendConfig = environmentsConfig + `backend:
  s3:
    bucket: shop-dev-tfstate
    region: us-east-1
    key_prefix: environments
`

// checkEnvironmentsDrift checks drift on the merged root of
// testdata/aws-environments, as TestAWSEnvironments left the fixture,
// imported again with an S3 backend: dev's three channel queues, and
// prod's, become one resource with for_each over a map variable each
// environment's var file sets. Each environment initialises the root with
// its vars/<env>/backend.hcl, prod's pointed at prod's own bucket, and
// applies it, so that its state is in its bucket, not in the root. Then,
// in the cloud, dev's email queue gets another retention, and prod's
// alerts queue, of the deploy_alerts feature only prod has, a delay. Drift
// as each environment, with its credentials, reads its state from its
// bucket, and reports its change, kept; --write-decisions writes an entry
// for each, with its environment. Accepted in the decisions file, dev's
// change is written into the email key of the map in dev's var file,
// the code and prod's var file staying as they were; prod's into the
// alerts queue's block, which only prod has. Each environment then plans
// no change, and drift finds nothing left to revert.
func checkEnvironmentsDrift(ctx context.Context, t *testing.T, fixture *appliedFixture, engineName, execPath, plugins string, credentials func(env string)) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	out := t.TempDir()
	root := cli.NewCmdRoot()
	root.SetArgs([]string{
		"import", "aws",
		"--config=" + writeConfig(t, environmentsBackendConfig),
		"--engine=" + engineName,
		"--terraform-path=" + execPath,
		"--accounts=" + environmentAccounts["dev"] + "," + environmentAccounts["prod"],
		"--parallel=2",
		"--regions=us-east-1",
		"--services=sqs,sns",
		"--out=" + out,
		"--all",
	})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("infraharvest import with a backend: %v", err)
	}
	coverage := coverageOf(t, out)
	checkCoverage(t, "the import with a backend", coverage, nil)
	checkMerged(t, out, coverage, "aws/us-east-1", "dev", "prod")
	dir := filepath.Join(out, "aws", "us-east-1")

	// The channel queues are one resource with for_each over a map
	// variable, whose email key is dev's email queue.
	variable, key := instanceKey(t, dir, "dev", "shop-dev-email")
	forEach := regexp.MustCompile(`resource "aws_sqs_queue" "(\w+)" \{\s*for_each\s*=\s*var\.` + variable + `\s`).FindStringSubmatch(configuration(t, dir))
	if forEach == nil {
		t.Fatalf("no queue has for_each over var.%s:\n%s", variable, configuration(t, dir))
	}
	if got := instanceField(t, dir, "dev", variable, key, "message_retention_seconds"); got != "3600" {
		t.Fatalf("dev's var file gives %s[%q] the retention %s, want 3600", variable, key, got)
	}
	// Each environment's state in its own account's bucket.
	prodBackend := filepath.Join(dir, "vars", "prod", "backend.hcl")
	settings := readFile(t, prodBackend)
	bucket := regexp.MustCompile(`(?m)^(\s*bucket\s*=\s*)"shop-dev-tfstate"`)
	if !bucket.MatchString(settings) {
		t.Fatalf("prod's backend.hcl doesn't name the configured bucket:\n%s", settings)
	}
	if err := os.WriteFile(prodBackend, []byte(bucket.ReplaceAllString(settings, `${1}"shop-prd-tfstate"`)), 0o644); err != nil {
		t.Fatal(err)
	}
	if providers := readFile(t, filepath.Join(dir, "providers.tf")); !strings.Contains(providers, `backend "s3"`) {
		t.Fatalf("the merged root has no S3 backend:\n%s", providers)
	}
	for _, env := range []string{"dev", "prod"} {
		credentials(env)
		tf := backendTerraform(ctx, t, dir, execPath, plugins, env)
		var vars []tfexec.ApplyOption
		for _, name := range []string{"common.tfvars", env + "/terraform.tfvars", env + "/imports.tfvars"} {
			vars = append(vars, tfexec.VarFile(filepath.Join(dir, "vars", filepath.FromSlash(name))))
		}
		if err := tf.Apply(ctx, append(vars, tfexec.Lock(false))...); err != nil {
			t.Fatalf("%s: terraform apply: %v", env, err)
		}
	}
	for _, local := range []string{"terraform.tfstate", "terraform.tfstate.d"} {
		if _, err := os.Stat(filepath.Join(dir, local)); err == nil {
			t.Errorf("the merged root keeps %s, not its state in the buckets", local)
		}
	}

	fixture.apply(ctx, t, tfexec.Var("audit=true"), tfexec.Var("reports=true"), tfexec.Var("dev_orders_timeout=90"), tfexec.Var("billing=true"),
		tfexec.Var("dev_email_retention=7200"), tfexec.Var("alerts_delay=30"))
	drift := driftRun{ctx: ctx, engineName: engineName, execPath: execPath, out: out}
	code := codeFiles(t, out)
	prodVars := readFile(t, filepath.Join(dir, "vars", "prod", "terraform.tfvars"))

	// Each environment's change, kept, and its entry in the decisions file.
	credentials("dev")
	d := drift.mustExit(t, report.ExitDrift, "--detailed-exitcode", "--environment=dev", "--write-decisions")
	if len(d.Roots) != 1 || d.Roots[0].Backend != "s3" || d.Roots[0].Environment != "dev" || d.Roots[0].Resources == 0 {
		t.Errorf("drift as dev: roots %+v, want aws/us-east-1 (dev) read from its S3 backend", d.Roots)
	}
	retention := driftChange(t, "drift as dev", d, "aws_sqs_queue", "message_retention_seconds")
	checkDriftChange(t, "drift as dev", retention, "3600", "7200", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	if want := "aws_sqs_queue." + forEach[1] + `["` + key + `"]`; retention.Address != want || retention.Environment != "dev" {
		t.Errorf("drift as dev: the retention is of %s in %q, want %s in dev", retention.Address, retention.Environment, want)
	}
	credentials("prod")
	d = drift.mustExit(t, report.ExitDrift, "--detailed-exitcode", "--environment=prod", "--write-decisions")
	if len(d.Roots) != 1 || d.Roots[0].Backend != "s3" || d.Roots[0].Environment != "prod" {
		t.Errorf("drift as prod: roots %+v, want aws/us-east-1 (prod) read from its S3 backend", d.Roots)
	}
	delay := driftChange(t, "drift as prod", d, "aws_sqs_queue", "delay_seconds")
	checkDriftChange(t, "drift as prod", delay, "0", "30", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	if !strings.Contains(delay.Address, "alerts") || !strings.HasSuffix(delay.Address, "[0]") || delay.Environment != "prod" {
		t.Errorf("drift as prod: the delay is of %s in %q, want the alerts queue's instance [0] in prod", delay.Address, delay.Environment)
	}
	checkCodeUnchanged(t, "drift, kept", code, out)
	decisions := filepath.Join(out, driftpolicy.DefaultDecisionsFile)
	if e := decisionEntry(t, decisions, "message_retention_seconds", driftpolicy.Undecided); e.Environment != "dev" || e.Root != "aws/us-east-1" {
		t.Errorf("%s: the retention's entry is of %s (%s), want aws/us-east-1 (dev)", decisions, e.Root, e.Environment)
	}
	if e := decisionEntry(t, decisions, "delay_seconds", driftpolicy.Undecided); e.Environment != "prod" || e.Root != "aws/us-east-1" {
		t.Errorf("%s: the delay's entry is of %s (%s), want aws/us-east-1 (prod)", decisions, e.Root, e.Environment)
	}
	acceptDrift(t, decisions, "message_retention_seconds")
	acceptDrift(t, decisions, "delay_seconds")

	// Accepted, dev's change goes into the email key of dev's map, alone.
	credentials("dev")
	d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode", "--environment=dev", "--decisions="+decisions)
	retention = driftChange(t, "drift as dev, decided", d, "aws_sqs_queue", "message_retention_seconds")
	checkDriftChange(t, "drift as dev, decided", retention, "3600", "7200", report.DriftAccept, report.DriftDecidedByEntry, report.DriftWritten)
	if retention.Source == nil || retention.Source.File != "vars/dev/terraform.tfvars" || retention.Source.Line == 0 {
		t.Errorf("drift as dev, decided: the source is %+v, want dev's terraform.tfvars", retention.Source)
	}
	checkCodeUnchanged(t, "drift as dev, decided", code, out)
	if got := instanceField(t, dir, "dev", variable, key, "message_retention_seconds"); got != "7200" {
		t.Errorf("drift as dev, decided: dev's var file gives %s[%q] the retention %s, want 7200:\n%s", variable, key, got, readFile(t, filepath.Join(dir, "vars", "dev", "terraform.tfvars")))
	}
	for _, other := range []string{"sms", "push"} {
		k := keyNamed(t, dir, "dev", variable, "shop-dev-"+other)
		if got, before := instanceField(t, dir, "dev", variable, k, "message_retention_seconds"), map[string]string{"sms": "7200", "push": "10800"}[other]; got != before {
			t.Errorf("drift as dev, decided: dev's %s queue's retention became %s, want %s", other, got, before)
		}
	}
	if got := readFile(t, filepath.Join(dir, "vars", "prod", "terraform.tfvars")); got != prodVars {
		t.Errorf("drift as dev, decided: prod's terraform.tfvars changed:\n%s", got)
	}

	// Accepted, prod's change goes into the alerts queue's block, which
	// only prod has; dev's var file stays as dev's run left it.
	devVars := readFile(t, filepath.Join(dir, "vars", "dev", "terraform.tfvars"))
	credentials("prod")
	d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode", "--environment=prod", "--decisions="+decisions)
	delay = driftChange(t, "drift as prod, decided", d, "aws_sqs_queue", "delay_seconds")
	checkDriftChange(t, "drift as prod, decided", delay, "0", "30", report.DriftAccept, report.DriftDecidedByEntry, report.DriftWritten)
	if delay.Source == nil || !strings.HasSuffix(delay.Source.File, ".tf") {
		t.Errorf("drift as prod, decided: the source is %+v, want the alerts queue's block", delay.Source)
	}
	if !codeMatches(t, out, `delay_seconds\s*=\s*30\b`) {
		t.Errorf("drift as prod, decided: the code doesn't set the alerts queue's delay_seconds = 30")
	}
	if got := readFile(t, filepath.Join(dir, "vars", "dev", "terraform.tfvars")); got != devVars {
		t.Errorf("drift as prod, decided: dev's terraform.tfvars changed:\n%s", got)
	}
	if got := readFile(t, filepath.Join(dir, "vars", "prod", "terraform.tfvars")); got != prodVars {
		t.Errorf("drift as prod, decided: prod's terraform.tfvars changed:\n%s", got)
	}

	// Each environment plans no change, and drift finds nothing to revert.
	for _, env := range []string{"dev", "prod"} {
		credentials(env)
		tf := backendTerraform(ctx, t, dir, execPath, plugins, env)
		var vars []tfexec.PlanOption
		for _, name := range []string{"common.tfvars", env + "/terraform.tfvars", env + "/imports.tfvars"} {
			vars = append(vars, tfexec.VarFile(filepath.Join(dir, "vars", filepath.FromSlash(name))))
		}
		changes, err := tf.Plan(ctx, append(vars, tfexec.Lock(false))...)
		if err != nil || changes {
			t.Errorf("%s: the accepted code plans changes (%v): %v", env, changes, err)
		}
		d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode", "--environment="+env)
		for _, c := range d.Changes {
			if c.Outcome != report.DriftAbsorbed {
				t.Errorf("drift as %s, accepted: %s %s is %s (%s), want %s", env, c.Address, c.Attribute, c.Outcome, c.Reason, report.DriftAbsorbed)
			}
		}
	}
}

// backendTerraform returns Terraform for the merged root in dir,
// initialised with environment env's backend settings.
func backendTerraform(ctx context.Context, t *testing.T, dir, execPath, plugins, env string) *tfrun.Runner {
	t.Helper()
	tf, err := tfrun.New(dir, execPath, plugins, nil)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := filepath.Abs(filepath.Join(dir, "vars", env, "backend.hcl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := tf.Init(ctx, tfexec.Reconfigure(true), tfexec.BackendConfig(settings)); err != nil {
		t.Fatalf("%s: terraform init -backend-config=%s: %v", env, settings, err)
	}
	return tf
}

// varFileMaps returns the maps of objects environment env's var file of
// the merged root in dir sets, by variable.
func varFileMaps(t *testing.T, dir, env string) map[string]cty.Value {
	t.Helper()
	path := filepath.Join(dir, "vars", env, "terraform.tfvars")
	f, diags := hclsyntax.ParseConfig([]byte(readFile(t, path)), path, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("%s: %v", path, diags)
	}
	maps := map[string]cty.Value{}
	for name, attr := range f.Body.(*hclsyntax.Body).Attributes {
		v, diags := attr.Expr.Value(nil)
		if diags.HasErrors() || v.IsNull() || !v.Type().IsObjectType() && !v.Type().IsMapType() {
			continue
		}
		maps[name] = v
	}
	return maps
}

// instanceKey returns the map variable environment env's var file sets
// whose key has the field name, name, and the key.
func instanceKey(t *testing.T, dir, env, name string) (string, string) {
	t.Helper()
	for variable, m := range varFileMaps(t, dir, env) {
		if key, ok := keyWithName(m, name); ok {
			return variable, key
		}
	}
	t.Fatalf("%s's var file has no map with a key named %s:\n%s", env, name, readFile(t, filepath.Join(dir, "vars", env, "terraform.tfvars")))
	return "", ""
}

// keyNamed returns the key of variable, a map environment env's var file
// sets, whose field name is name.
func keyNamed(t *testing.T, dir, env, variable, name string) string {
	t.Helper()
	m, ok := varFileMaps(t, dir, env)[variable]
	if !ok {
		t.Fatalf("%s's var file doesn't set %s", env, variable)
	}
	key, found := keyWithName(m, name)
	if !found {
		t.Fatalf("%s's var file has no key of %s named %s", env, variable, name)
	}
	return key
}

func keyWithName(m cty.Value, name string) (string, bool) {
	if !m.CanIterateElements() {
		return "", false
	}
	for it := m.ElementIterator(); it.Next(); {
		k, item := it.Element()
		if !item.Type().IsObjectType() || !item.Type().HasAttribute("name") {
			continue
		}
		if n := item.GetAttr("name"); n.Type().Equals(cty.String) && !n.IsNull() && n.AsString() == name {
			return k.AsString(), true
		}
	}
	return "", false
}

// instanceField returns the field of key of variable, a map environment
// env's var file sets, as text: a number as Terraform writes it.
func instanceField(t *testing.T, dir, env, variable, key, field string) string {
	t.Helper()
	m, ok := varFileMaps(t, dir, env)[variable]
	if !ok || !m.Type().IsObjectType() || !m.Type().HasAttribute(key) {
		t.Fatalf("%s's var file has no key %q of %s", env, key, variable)
	}
	item := m.GetAttr(key)
	if !item.Type().IsObjectType() || !item.Type().HasAttribute(field) {
		t.Fatalf("%s's var file has no %s at the key %q of %s", env, field, key, variable)
	}
	v := item.GetAttr(field)
	switch {
	case v.IsNull():
		return "null"
	case v.Type().Equals(cty.Number):
		return v.AsBigFloat().Text('f', -1)
	case v.Type().Equals(cty.String):
		return v.AsString()
	}
	return v.GoString()
}
