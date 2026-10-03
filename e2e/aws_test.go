// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/cmd"
	"github.com/IgnatG/infraharvest/engine"
)

// awsServices are the infraharvest services that import what
// testdata/aws creates.
var awsServices = []string{
	"alb", "cloudwatch", "dynamodb", "ebs", "ecr", "ecs", "eip", "iam", "igw",
	"kinesis", "kms", "logs", "nacl", "nat", "route53", "route_table", "s3",
	"secretsmanager", "sfn", "sg", "sns", "sqs", "ssm", "subnet", "vpc",
	"vpc_endpoint",
}

// TestAWSRoundTrip creates resources in an AWS emulator, imports them with
// --engine=terraform and checks Terraform plans every generated resource
// as an import with no changes. Run it against Floci:
//
//	docker compose -f e2e/compose.yaml up -d --wait
//	AWS_ENDPOINT_URL=http://localhost:4566 go test -tags e2e,minimal,aws ./e2e/
func TestAWSRoundTrip(t *testing.T) {
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		t.Skip("AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cache := t.TempDir()
	execPath, err := engine.FindTerraform(ctx, "", filepath.Join(cache, "terraform"))
	if err != nil {
		t.Fatal(err)
	}

	state := seed(ctx, t, execPath, filepath.Join(cache, "plugins"))

	out := t.TempDir()
	root := cmd.NewCmdRoot()
	root.SetArgs([]string{
		"import", "aws",
		"--engine=terraform",
		"--terraform-path=" + execPath,
		"--regions=us-east-1",
		"--resources=" + strings.Join(awsServices, ","),
		"--path-pattern={output}/{provider}/",
		"--path-output=" + out,
		// Keep going after a failed directory so one run reports every
		// problem; the checks below still fail the test.
		"--allow-partial",
	})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("infraharvest import: %v", err)
	}

	readFile(t, filepath.Join(out, ".gitignore"))
	imported := map[string]int{}
	var lock string
	for _, dir := range generatedDirs(t, out) {
		if rejected, err := os.ReadFile(filepath.Join(dir, engine.RejectedFileName)); err == nil {
			t.Errorf("%s: resources left out:\n%s", dir, rejected)
		}
		versions := readFile(t, filepath.Join(dir, engine.VersionsFileName))
		if !strings.Contains(versions, "required_version") || !strings.Contains(versions, `version = "~> `) {
			t.Errorf("%s: %s doesn't pin Terraform and the provider:\n%s", dir, engine.VersionsFileName, versions)
		}
		if strings.Contains(readFile(t, filepath.Join(dir, engine.ImportsFileName)), "tfer--") {
			t.Errorf("%s: legacy tfer-- resource names", dir)
		}
		readFile(t, filepath.Join(dir, engine.ReadmeFileName))
		// One provider version for the whole import.
		if content := readFile(t, filepath.Join(dir, engine.LockFileName)); lock == "" {
			lock = content
		} else if content != lock {
			t.Errorf("%s: %s differs from the other directories'", dir, engine.LockFileName)
		}
		for typ, n := range checkNoChanges(ctx, t, dir, execPath, filepath.Join(cache, "plugins"), secretValues(t, dir, state)) {
			imported[typ] += n
		}
	}
	seeded := map[string]int{}
	for _, r := range state {
		seeded[r.Type]++
	}
	for _, typ := range sortedKeys(seeded) {
		if imported[typ] < seeded[typ] {
			t.Errorf("%s: created %d, imported %d", typ, seeded[typ], imported[typ])
		}
	}
}

// isolateAWSConfig points the SDK and the Terraform provider at test
// credentials and empty shared files, so the test never uses a real account.
func isolateAWSConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"config":      "[default]\nregion = us-east-1\n",
		"credentials": "[default]\naws_access_key_id = test\naws_secret_access_key = test\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range map[string]string{
		"AWS_CONFIG_FILE":             filepath.Join(dir, "config"),
		"AWS_SHARED_CREDENTIALS_FILE": filepath.Join(dir, "credentials"),
		"AWS_ACCESS_KEY_ID":           "test",
		"AWS_SECRET_ACCESS_KEY":       "test",
		"AWS_SESSION_TOKEN":           "",
		"AWS_PROFILE":                 "",
		"AWS_DEFAULT_PROFILE":         "",
		"AWS_REGION":                  "us-east-1",
		"AWS_DEFAULT_REGION":          "us-east-1",
	} {
		t.Setenv(k, v)
	}
}

// seed applies testdata/aws and returns the resources it created.
func seed(ctx context.Context, t *testing.T, execPath, pluginCache string) []*tfjson.StateResource {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join("testdata", "aws", "main.tf")
	content, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	tf, err := engine.NewTerraform(dir, execPath, pluginCache)
	if err != nil {
		t.Fatal(err)
	}
	// Seeding failures are usually emulator gaps; Terraform's output names them.
	tf.SetStdout(os.Stdout)
	tf.SetStderr(os.Stderr)
	if err := tf.Init(ctx); err != nil {
		t.Fatalf("seed: terraform init: %v", err)
	}
	// No destroy: the emulator keeps everything in memory, and Floci can't
	// delete some resources (ECR repositories hang). Restart it to reset.
	if err := tf.Apply(ctx); err != nil {
		t.Fatalf("seed: terraform apply: %v", err)
	}
	state, err := tf.Show(ctx)
	if err != nil {
		t.Fatalf("seed: terraform show: %v", err)
	}
	var created []*tfjson.StateResource
	for _, r := range state.Values.RootModule.Resources {
		if r.Mode == tfjson.ManagedResourceMode {
			created = append(created, r)
		}
	}
	return created
}

// secretValues sets the secret variables of the configuration generated in
// dir to the values the test created. It finds each value by the resource
// type and name of the resource that uses the variable.
func secretValues(t *testing.T, dir string, created []*tfjson.StateResource) []tfexec.PlanOption {
	t.Helper()
	path := filepath.Join(dir, engine.GeneratedFileName)
	file, diags := hclsyntax.ParseConfig([]byte(readFile(t, path)), path, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	var vars []tfexec.PlanOption
	for _, block := range file.Body.(*hclsyntax.Body).Blocks {
		if block.Type != "resource" {
			continue
		}
		name := ""
		if attr, ok := block.Body.Attributes["name"]; ok {
			if v, diags := attr.Expr.Value(nil); !diags.HasErrors() && v.Type() == cty.String {
				name = v.AsString()
			}
		}
		for attrName, attr := range block.Body.Attributes {
			traversal, ok := attr.Expr.(*hclsyntax.ScopeTraversalExpr)
			if !ok || traversal.Traversal.RootName() != "var" || len(traversal.Traversal) != 2 {
				continue
			}
			variable := traversal.Traversal[1].(hcl.TraverseAttr).Name
			value, ok := createdValue(created, block.Labels[0], name, attrName)
			if !ok {
				t.Errorf("%s: no created %s named %q with a %s for variable %s", dir, block.Labels[0], name, attrName, variable)
				continue
			}
			vars = append(vars, tfexec.Var(variable+"="+value))
		}
	}
	return vars
}

// createdValue returns the string attribute of the created resource of
// resourceType called name.
func createdValue(created []*tfjson.StateResource, resourceType, name, attribute string) (string, bool) {
	for _, r := range created {
		if r.Type == resourceType && r.AttributeValues["name"] == name {
			value, ok := r.AttributeValues[attribute].(string)
			return value, ok
		}
	}
	return "", false
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// generatedDirs returns every directory under out with a generated.tf.
func generatedDirs(t *testing.T, out string) []string {
	t.Helper()
	var dirs []string
	err := filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".terraform" {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == engine.GeneratedFileName {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatalf("no %s under %s", engine.GeneratedFileName, out)
	}
	return dirs
}

// checkNoChanges plans dir with vars and reports every resource that is not
// an import without changes. It returns how many resources of each type it
// imports.
func checkNoChanges(ctx context.Context, t *testing.T, dir, execPath, pluginCache string, vars []tfexec.PlanOption) map[string]int {
	t.Helper()
	tf, err := engine.NewTerraform(dir, execPath, pluginCache)
	if err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(t.TempDir(), "e2e.tfplan")
	if _, err := tf.Plan(ctx, append(vars, tfexec.Out(planFile))...); err != nil {
		t.Errorf("%s: terraform plan of the generated configuration: %v", dir, err)
		return nil
	}
	plan, err := tf.ShowPlanFile(ctx, planFile)
	if err != nil {
		t.Fatalf("%s: terraform show: %v", dir, err)
	}
	imported := map[string]int{}
	for _, rc := range plan.ResourceChanges {
		if rc.Change.Importing == nil {
			t.Errorf("%s: %s is planned to %v instead of imported", dir, rc.Address, rc.Change.Actions)
			continue
		}
		if !rc.Change.Actions.NoOp() {
			changed := changedAttributes(rc.Change.Before, rc.Change.After)
			if !rc.Change.Actions.Update() || !stateOnly(rc.Type, changed) {
				t.Errorf("%s: %s (import ID %q) is imported with changes: %v %v", dir, rc.Address, rc.Change.Importing.ID, rc.Change.Actions, changed)
				continue
			}
		}
		imported[rc.Type]++
	}
	return imported
}

// stateOnlyArguments are arguments AWS doesn't report, so they are null
// after an import and the plan sets the provider's defaults: settings the
// provider keeps only in state (used when it deletes a resource, for
// example), and settings that don't apply to the resource's kind. The AWS
// provider's own import tests ignore the same arguments.
var stateOnlyArguments = map[string][]string{
	"aws_ecs_service":           {"wait_for_steady_state"},
	"aws_lb_target_group":       {"lambda_multi_value_headers_enabled", "proxy_protocol_v2"},
	"aws_secretsmanager_secret": {"force_overwrite_replica_secret", "recovery_window_in_days"},
}

// emulatorGaps are attributes Floci leaves out of its API responses where
// AWS returns them, so the plan sets the provider's default. Each one is a
// difference from AWS, not from infraharvest's output.
var emulatorGaps = map[string][]string{
	// DescribeServices has no deploymentConfiguration.
	"aws_ecs_service": {"deployment_maximum_percent", "deployment_minimum_healthy_percent"},
	// DescribeTargetGroupAttributes has no target_group_health.* keys.
	"aws_lb_target_group": {"target_group_health"},
}

// stateOnly reports whether every changed attribute is a state-only argument
// of resourceType, or one the emulator doesn't return.
func stateOnly(resourceType string, changed map[string]string) bool {
	for name := range changed {
		if !slices.Contains(stateOnlyArguments[resourceType], name) && !slices.Contains(emulatorGaps[resourceType], name) {
			return false
		}
	}
	return true
}

func isLoopback(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// changedAttributes maps each top-level attribute a planned update changes
// to "before -> after".
func changedAttributes(before, after interface{}) map[string]string {
	b, _ := before.(map[string]interface{})
	a, _ := after.(map[string]interface{})
	changed := map[string]string{}
	for k, v := range a {
		if !reflect.DeepEqual(b[k], v) {
			changed[k] = fmt.Sprintf("%v -> %v", b[k], v)
		}
	}
	return changed
}
