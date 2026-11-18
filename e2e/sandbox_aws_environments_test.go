// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && sandbox && (!minimal || aws)

package e2e

import (
	"context"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// sandboxEnvironmentsConfig maps the two estates of
// testdata/sandbox/aws-environments, in the one sandbox account, to dev
// and prod by their Environment tag. It names no account, so the stack
// key leaves the account out and the two environments' roots of the
// region merge; with one account, it tests merging, not each
// environment's own credentials.
const sandboxEnvironmentsConfig = `version: 1
environments:
  dev:
    tags: { Environment: dev }
  prod:
    tags: { Environment: prd }
    tokens: [prd]
`

// sandboxEnvironmentsServices are the infraharvest services that import
// what testdata/sandbox/aws-environments creates.
var sandboxEnvironmentsServices = []string{"s3", "sqs", "ssm"}

// sandboxEnvironmentTags are the environments of the configuration file
// by the Environment tag the estate gives their resources.
var sandboxEnvironmentTags = map[string]string{"dev": "dev", "prd": "prod"}

// TestSandboxAWSEnvironments creates testdata/sandbox/aws-environments,
// two small estates of one workload, dev and prd, in the sandbox account
// as the fixture profile, and imports its region as the read-only
// profile, with a configuration file whose environments map the estates
// by tag (sandboxEnvironmentsConfig). The roots of the two environments
// must merge into one root they share, aws/<region>, with what differs in
// each environment's var files: the queue's visibility timeout a
// variable, prd's alerts queue counted by a deploy_ variable only prod
// sets, and each environment's secret-named parameter value a sensitive
// variable that no var file sets and no file of the output holds. Planned
// as each environment, with its var files and its secret, the merged root
// must import what that environment has and change nothing. It destroys
// the estate when it is done. Last, it hands a resource from the merged
// root to another over (see checkSandboxHandover). The sandbox-aws workflow
// runs it when asked to (estate=environments); see e2e/README.md.
func TestSandboxAWSEnvironments(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Minute)
	defer cancel()
	s := newSandbox(ctx, t)

	estate := s.seed(ctx, t, "aws-environments", "suffix="+s.engineName)
	created := estate.apply(ctx, t)
	secrets := sandboxEnvironmentSecrets(t, created)

	config := writeConfig(t, sandboxEnvironmentsConfig)
	// No module calls: each bucket is a resource of the merged root.
	run := s.importer(t, []string{s.region}, sandboxEnvironmentsServices)
	run.must(ctx, t, "--config="+config, "--modules=none")
	coverage := readCoverage(t, s.out)
	t.Logf("totals: %+v", coverage.Totals)
	checkCoverage(t, "the import", coverage, nil)
	path := "aws/" + s.region
	merge := checkMerged(t, s.out, coverage, path, "dev", "prod")
	t.Logf("%s: %d shared, residue %+v, variables %v", path, merge.Shared, merge.Residue, merge.Variables)
	dir := filepath.Join(s.out, "aws", s.region)

	files := configuration(t, dir)
	if !strings.Contains(strings.Join(strings.Fields(files), " "), "Environment = var.env") {
		t.Errorf("the merged root's tags don't take Environment from var.env:\n%s", files)
	}
	for _, env := range []string{"dev", "prd"} {
		if strings.Contains(files, s.engineName+"-"+env+"-orders") {
			t.Errorf("the merged root names %s's orders queue, not from a local:\n%s", env, files)
		}
	}
	devVars := readFile(t, filepath.Join(dir, "vars", "dev", "terraform.tfvars"))
	prodVars := readFile(t, filepath.Join(dir, "vars", "prod", "terraform.tfvars"))
	if timeout := regexp.MustCompile(`visibility_timeout_seconds\s*=\s*var\.(\w+)`).FindStringSubmatch(files); timeout == nil {
		t.Errorf("no variable sets the orders queue's visibility timeout:\n%s", files)
	} else {
		// An environment's terraform.tfvars sets only the values that differ
		// from the defaults in variables.tf, so the environment whose value
		// became the default leaves it out. A null default leaves the
		// attribute to the provider, whose default timeout is dev's 30.
		def := variableDefault(t, dir, timeout[1])
		for _, tc := range []struct{ env, vars, want string }{{"dev", devVars, "30"}, {"prod", prodVars, "120"}} {
			set := regexp.MustCompile(`(?m)^\s*` + timeout[1] + `\s*=\s*(\S+)\s*$`).FindStringSubmatch(tc.vars)
			switch {
			case set != nil && set[1] != tc.want:
				t.Errorf("%s's terraform.tfvars sets %s to %s, want %s", tc.env, timeout[1], set[1], tc.want)
			case set == nil && def != tc.want && (def != "null" || tc.want != "30"):
				t.Errorf("%s's terraform.tfvars doesn't set %s, whose default is %q, not %s:\n%s", tc.env, timeout[1], def, tc.want, tc.vars)
			}
		}
	}
	if gate := regexp.MustCompile(`count\s*=\s*var\.(deploy_\w+) \? 1 : 0`).FindStringSubmatch(files); gate == nil {
		t.Errorf("no deploy_ variable counts prd's alerts queue:\n%s", files)
	} else {
		set := regexp.MustCompile(`(?m)^\s*` + gate[1] + `\s*=\s*true\s*$`)
		if !set.MatchString(prodVars) || set.MatchString(devVars) {
			t.Errorf("%s isn't set for prod alone:\ndev:\n%s\nprod:\n%s", gate[1], devVars, prodVars)
		}
	}
	secretVariable := checkSandboxEnvironmentSecrets(t, s.out, dir, secrets)

	// Planned as each environment, with its var files and its secret, the
	// merged root imports what that environment has and changes nothing.
	want := map[string]map[string]int{
		"dev":  {"aws_sqs_queue": 1, "aws_s3_bucket": 1, "aws_ssm_parameter": 1},
		"prod": {"aws_sqs_queue": 2, "aws_s3_bucket": 1, "aws_ssm_parameter": 1},
	}
	for _, env := range []string{"dev", "prod"} {
		vars := mergedVars(dir, env)
		if secretVariable != "" {
			vars = append(vars, tfexec.Var(secretVariable+"="+secrets[env]))
		}
		imported := checkNoChanges(ctx, t, dir, s.execPath, s.plugins, vars)
		t.Logf("%s imports %v", env, imported)
		for typ, n := range want[env] {
			if imported[typ] != n {
				t.Errorf("%s imports %d of %s, want %d", env, imported[typ], typ, n)
			}
		}
	}

	// What the import put in other roots, such as the account's resources
	// without an environment's tag, plans as it was generated; the roots
	// merged, kept under the checkpoints, aren't the output's.
	for _, other := range generatedDirs(t, s.out) {
		rel, err := filepath.Rel(s.out, other)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Clean(other) != filepath.Clean(dir) && strings.Split(filepath.ToSlash(rel), "/")[0] != importer.CheckpointDir {
			checkNoChanges(ctx, t, other, s.execPath, s.plugins, secretValues(t, other, created))
		}
	}

	checkSandboxHandover(ctx, t, s, estate, run, config, path, secretVariable, secrets, created)
}

// sandboxHandoverEnvironment is the Environment tag checkSandboxHandover
// gives prd's alerts queue, which neither environment's tags match.
const sandboxHandoverEnvironment = "sandbox-handover"

// checkSandboxHandover does what a team does when the Environment tag of
// a resource changes: it moves to another root. It applies the merged
// root of path in the output as each environment, into the workspace
// named after it, so that each has prd's alerts queue as its state has it
// (only prod's does, as only prod counts it); gives the queue another
// Environment tag as the fixture profile (alerts_environment), so that it
// is neither dev's nor prod's; and imports again with --regenerate. The
// queue must be handed over: the merged root's moves.tf gets a removed
// block for it, with destroy = false, which its README lists with the
// root it goes to, and that root imports it, with nothing else failing or
// left incomplete. Applied in order, the merged root as each environment
// first, then the root the queue goes to, the prod workspace must have
// forgotten the queue, and every plan, as each environment and of the new
// root, must have no changes. config is the configuration file of the
// import (see sandboxEnvironmentsConfig), secretVariable the merged
// root's variable for the parameters' values, secrets their values by
// environment, and created the estate's resources.
func checkSandboxHandover(ctx context.Context, t *testing.T, s *sandbox, estate *sandboxEstate, run sandboxImport, config, path, secretVariable string, secrets map[string]string, created []*tfjson.StateResource) {
	t.Helper()
	environments := []string{"dev", "prod"}
	alerts := ""
	for _, r := range created {
		if r.Type == "aws_sqs_queue" && r.Name == "prd_alerts" {
			alerts, _ = r.AttributeValues["name"].(string)
		}
	}
	if alerts == "" {
		t.Fatal("the estate has no alerts queue")
	}
	dir, err := filepath.Abs(filepath.Join(s.out, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	// vars are the options an environment plans and applies the merged
	// root with: its var files and its secret.
	secret := func(env string) []tfexec.PlanOption {
		if secretVariable == "" {
			return nil
		}
		return []tfexec.PlanOption{tfexec.Var(secretVariable + "=" + secrets[env])}
	}
	apply := func(tf *tfrun.Runner, env string) {
		t.Helper()
		opts := []tfexec.ApplyOption{tfexec.Lock(false)}
		for _, f := range mergedVarFiles(dir, env) {
			opts = append(opts, tfexec.VarFile(f))
		}
		for _, v := range secret(env) {
			opts = append(opts, v.(tfexec.ApplyOption))
		}
		if err := tf.Apply(ctx, opts...); err != nil {
			t.Fatalf("%s: terraform apply: %v", env, err)
		}
	}
	for _, env := range environments {
		tf, err := tfrun.New(dir, s.execPath, s.plugins, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := tf.Init(ctx); err != nil {
			t.Fatalf("%s: terraform init: %v", env, err)
		}
		if err := tf.WorkspaceNew(ctx, env); err != nil {
			t.Fatalf("terraform workspace new %s: %v", env, err)
		}
		apply(tf, env)
		if has := workspaceHasQueue(ctx, t, tf, alerts); has != (env == "prod") {
			t.Errorf("the %s workspace has the alerts queue %s: %v, want %v", env, alerts, has, env == "prod")
		}
	}

	created = estate.apply(ctx, t, tfexec.Var("alerts_environment="+sandboxHandoverEnvironment), tfexec.Target("aws_sqs_queue.prd_alerts"))
	run.must(ctx, t, "--config="+config, "--modules=none", "--regenerate")
	coverage := readCoverage(t, s.out)
	checkCoverage(t, "--regenerate", coverage, nil)
	checkMerged(t, s.out, coverage, path, "dev", "prod")
	if len(coverage.IncompleteHandovers) != 0 {
		t.Errorf("--regenerate: incomplete handovers %+v", coverage.IncompleteHandovers)
	}
	i := slices.IndexFunc(coverage.Departed, func(m report.Move) bool { return strings.HasSuffix(m.ID, "/"+alerts) })
	if i < 0 {
		t.Fatalf("--regenerate: the alerts queue %s isn't handed over: departed %+v", alerts, coverage.Departed)
	}
	departed := coverage.Departed[i]
	if departed.FromRoot != path || departed.ToRoot == "" || departed.ToRoot == path || departed.To == "" {
		t.Fatalf("--regenerate: the alerts queue goes from %s (%s) to %s (%s), want from %s to another root", departed.FromRoot, departed.From, departed.ToRoot, departed.To, path)
	}
	imports := false
	for _, d := range coverage.Directories {
		for _, r := range d.Imported {
			if d.Path == departed.ToRoot && strings.HasSuffix(r.ID, "/"+alerts) {
				imports = true
			}
			if d.Path != departed.ToRoot && strings.HasSuffix(r.ID, "/"+alerts) {
				t.Errorf("--regenerate: %s imports the alerts queue too", d.Path)
			}
		}
	}
	if !imports {
		t.Errorf("--regenerate: %s, which the alerts queue goes to, doesn't import it", departed.ToRoot)
	}
	moves := readFile(t, filepath.Join(dir, rootfile.MovesFileName))
	for _, want := range []string{"removed {", "destroy = false"} {
		if !strings.Contains(moves, want) {
			t.Errorf("the merged root's %s has no %q:\n%s", rootfile.MovesFileName, want, moves)
		}
	}
	if readme := readFile(t, filepath.Join(dir, rootfile.ReadmeFileName)); !strings.Contains(readme, "`"+departed.ToRoot+"`") {
		t.Errorf("the merged root's README doesn't say the alerts queue goes to %s:\n%s", departed.ToRoot, readme)
	}
	// The applied states hold the secrets, so the output isn't searched
	// for them again: the merged root must still read them from the
	// variable.
	if secretVariable != "" && !strings.Contains(configuration(t, dir), `variable "`+secretVariable+`"`) {
		t.Errorf("--regenerate: the merged root no longer declares the secret variable %s", secretVariable)
	}

	// The merged root first, as each environment, which forgets the queue
	// where it has it, then the root the queue goes to, which imports it.
	for _, env := range environments {
		tf := workspace(ctx, t, dir, s.execPath, s.plugins, env)
		apply(tf, env)
		if workspaceHasQueue(ctx, t, tf, alerts) {
			t.Errorf("the %s workspace still has the alerts queue %s once the merged root is applied again", env, alerts)
		}
	}
	other := filepath.Join(s.out, filepath.FromSlash(departed.ToRoot))
	applyPlanned(ctx, t, other, s.execPath, s.plugins, secretValues(t, other, created))
	for _, env := range environments {
		selectWorkspace(ctx, t, dir, s.execPath, s.plugins, env)
		checkPlannedNothing(t, "the merged root, as "+env+", with the alerts queue handed over", planRoot(ctx, t, dir, s.execPath, s.plugins, append(mergedVars(dir, env), secret(env)...)...))
	}
	checkPlannedNothing(t, departed.ToRoot+", with the alerts queue", planRoot(ctx, t, other, s.execPath, s.plugins, secretValues(t, other, created)...))
}

// workspaceHasQueue reports whether the state of the workspace tf has
// selected has the SQS queue called name.
func workspaceHasQueue(ctx context.Context, t *testing.T, tf *tfrun.Runner, name string) bool {
	t.Helper()
	state, err := tf.Show(ctx)
	if err != nil {
		t.Fatalf("terraform show: %v", err)
	}
	if state.Values == nil || state.Values.RootModule == nil {
		return false
	}
	for _, r := range state.Values.RootModule.Resources {
		if r.Type == "aws_sqs_queue" && r.AttributeValues["name"] == name {
			return true
		}
	}
	return false
}

// sandboxEnvironmentSecrets returns the value of each environment's
// secret-named parameter the estate created, by environment.
func sandboxEnvironmentSecrets(t *testing.T, created []*tfjson.StateResource) map[string]string {
	t.Helper()
	secrets := map[string]string{}
	for _, r := range created {
		if r.Type != "aws_ssm_parameter" {
			continue
		}
		tags, _ := r.AttributeValues["tags"].(map[string]any)
		tag, _ := tags["Environment"].(string)
		value, _ := r.AttributeValues["value"].(string)
		if env, ok := sandboxEnvironmentTags[tag]; ok && value != "" {
			secrets[env] = value
		}
	}
	if len(secrets) != len(sandboxEnvironmentTags) {
		t.Fatalf("the estate's parameters have the secrets of %v, want one of each environment", slices.Sorted(maps.Keys(secrets)))
	}
	return secrets
}

// checkSandboxEnvironmentSecrets checks that the merged root in dir takes
// the parameters' values from one sensitive variable with no default,
// which none of its var files sets, and that no file of the output in out
// holds either environment's secret. It returns the variable, "" if there
// isn't one.
func checkSandboxEnvironmentSecrets(t *testing.T, out, dir string, secrets map[string]string) string {
	t.Helper()
	var sensitive []string
	for _, b := range configBlocks(t, dir) {
		if b.Type != "variable" || len(b.Labels) != 1 {
			continue
		}
		attr, ok := b.Body.Attributes["sensitive"]
		if !ok {
			continue
		}
		if v, diags := attr.Expr.Value(nil); diags.HasErrors() || v.Type() != cty.Bool || !v.True() {
			continue
		}
		if _, ok := b.Body.Attributes["default"]; !ok {
			sensitive = append(sensitive, b.Labels[0])
		}
	}
	if len(sensitive) != 1 {
		t.Errorf("the merged root has sensitive variables %v without a default, want one, for the parameters' values", sensitive)
		return ""
	}
	set := regexp.MustCompile(`(?m)^\s*` + sensitive[0] + `\s*=`)
	for name, content := range outputFiles(t, out) {
		for env, secret := range secrets {
			if strings.Contains(content, secret) {
				t.Errorf("%s holds %s's secret", name, env)
			}
		}
		if strings.HasSuffix(name, ".tfvars") && set.MatchString(content) {
			t.Errorf("%s sets the sensitive variable %s", name, sensitive[0])
		}
	}
	return sensitive[0]
}

// variableDefault returns the default of variable name in the root in dir,
// as HCL writes a number or a string, "null" if it is null, or "" if it has
// none.
func variableDefault(t *testing.T, dir, name string) string {
	t.Helper()
	for _, block := range configBlocks(t, dir) {
		if block.Type != "variable" || len(block.Labels) != 1 || block.Labels[0] != name {
			continue
		}
		attr, ok := block.Body.Attributes["default"]
		if !ok {
			return ""
		}
		v, diags := attr.Expr.Value(nil)
		if diags.HasErrors() || !v.IsKnown() {
			return ""
		}
		if v.IsNull() {
			return "null"
		}
		switch v.Type() {
		case cty.Number:
			return v.AsBigFloat().Text('f', -1)
		case cty.String:
			return v.AsString()
		}
	}
	return ""
}
