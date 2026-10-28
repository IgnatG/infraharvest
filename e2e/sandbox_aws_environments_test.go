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

	"github.com/IgnatG/infraharvest/internal/importer"
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
// the estate when it is done. The sandbox-aws workflow runs it when asked
// to (estate=environments); see e2e/README.md.
func TestSandboxAWSEnvironments(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	s := newSandbox(ctx, t)

	estate := s.seed(ctx, t, "aws-environments", "suffix="+s.engineName)
	created := estate.apply(ctx, t)
	secrets := sandboxEnvironmentSecrets(t, created)

	config := writeConfig(t, sandboxEnvironmentsConfig)
	// No module calls: each bucket is a resource of the merged root.
	s.importer(t, []string{s.region}, sandboxEnvironmentsServices).must(ctx, t, "--config="+config, "--modules=none")
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
		for _, tc := range []struct{ env, vars, want string }{{"dev", devVars, "30"}, {"prod", prodVars, "120"}} {
			if !regexp.MustCompile(`(?m)^\s*` + timeout[1] + `\s*=\s*` + tc.want + `\s*$`).MatchString(tc.vars) {
				t.Errorf("%s's terraform.tfvars doesn't set %s to %s:\n%s", tc.env, timeout[1], tc.want, tc.vars)
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
