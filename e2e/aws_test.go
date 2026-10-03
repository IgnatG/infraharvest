// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
	"github.com/IgnatG/infraharvest/providers/aws"
	"github.com/IgnatG/infraharvest/report"
	"github.com/IgnatG/infraharvest/selection"
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
// --engine=terraform (or E2E_ENGINE=tofu) and checks the engine plans every
// generated resource as an import with no changes. Run it against Floci:
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
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}

	state := seed(ctx, t, execPath, filepath.Join(cache, "plugins"))

	out := importAWS(ctx, t, engineName, execPath, "--all")

	// discover lists everything into a selection file; importing what it
	// selects must give the same files as --all, and importing the
	// unchanged estate again the same files (G7).
	selectionFile := discoverAWS(ctx, t, state)
	again := importAWS(ctx, t, engineName, execPath, "--selection="+selectionFile)
	if first, second := outputFiles(t, out), outputFiles(t, again); !reflect.DeepEqual(first, second) {
		for name, content := range first {
			if second[name] != content {
				t.Errorf("%s differs between two imports of the same estate", name)
			}
		}
		if len(first) != len(second) {
			t.Errorf("two imports wrote %d and %d files", len(first), len(second))
		}
	}

	readFile(t, filepath.Join(out, ".gitignore"))
	var coverage report.Report
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(out, report.Dir, "coverage.json"))), &coverage); err != nil {
		t.Fatal(err)
	}
	if coverage.Totals.LeftOut != 0 || coverage.Totals.Failed != 0 || len(coverage.Failures) != 0 {
		t.Errorf("coverage.json: %+v, failures %v", coverage.Totals, coverage.Failures)
	}
	// The verification gate may only fail on what the emulator leaves out.
	for _, d := range coverage.Directories {
		for _, c := range d.Checks {
			if !c.Passed && (c.Name != engine.CheckPlan || !emulatorGapsOnly(c.Details)) {
				t.Errorf("%s: %s failed: %v", d.Path, c.Name, c.Details)
			}
		}
	}
	imported := map[string]int{}
	var lock string
	// The network resources refer to the VPC instead of repeating its ID;
	// the plans below check that changes nothing.
	referencedVPC := false
	for _, dir := range generatedDirs(t, out) {
		if rejected, err := os.ReadFile(filepath.Join(dir, engine.RejectedFileName)); err == nil {
			t.Errorf("%s: resources left out:\n%s", dir, rejected)
		}
		// One root per account and region, each with its own state key.
		rel, err := filepath.Rel(out, dir)
		if err != nil {
			t.Fatal(err)
		}
		backend := readFile(t, filepath.Join(dir, cmd.BackendFileName))
		if wantKey := "e2e/" + filepath.ToSlash(rel) + "/terraform.tfstate"; !strings.HasPrefix(filepath.ToSlash(rel), "aws/000000000000/") || !strings.Contains(backend, wantKey) || !strings.Contains(backend, "use_lockfile = true") {
			t.Errorf("%s: want a root per account and region with state key %s:\n%s", rel, wantKey, backend)
		}
		versions := readFile(t, filepath.Join(dir, engine.VersionsFileName))
		if !strings.Contains(versions, "required_version") || !strings.Contains(versions, `version = "~> `) {
			t.Errorf("%s: %s doesn't pin Terraform and the provider:\n%s", dir, engine.VersionsFileName, versions)
		}
		if strings.Contains(readFile(t, filepath.Join(dir, engine.ImportsFileName)), "tfer--") {
			t.Errorf("%s: legacy tfer-- resource names", dir)
		}
		readFile(t, filepath.Join(dir, engine.ReadmeFileName))
		if strings.Contains(readFile(t, filepath.Join(dir, engine.GeneratedFileName)), "vpc_id = aws_vpc.") {
			referencedVPC = true
		}
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
	if !referencedVPC {
		t.Errorf("no %s refers to the VPC", engine.GeneratedFileName)
	}
	// The state and logs buckets have the same shape: one generated module,
	// called twice; the plans above check that changes nothing.
	modules, err := filepath.Glob(filepath.Join(out, engine.ModulesDirName, "s3_bucket_*", "main.tf"))
	if err != nil || len(modules) == 0 {
		t.Errorf("no generated S3 bucket module under %s (%v)", engine.ModulesDirName, err)
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
	// As the README says: init, with the root's backend, then plan.
	if err := tf.Init(ctx, tfexec.Reconfigure(true)); err != nil {
		t.Fatalf("%s: terraform init with the generated backend: %v", dir, err)
	}
	planFile := filepath.Join(t.TempDir(), "e2e.tfplan")
	// The emulator's S3 may not support the conditional writes S3 state
	// locking uses.
	if _, err := tf.Plan(ctx, append(vars, tfexec.Out(planFile), tfexec.Lock(false))...); err != nil {
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

// stateOnlyArguments are the arguments the AWS provider keeps only in
// state, which the plan sets after an import (see
// aws.AWSProvider.StateOnlyArguments).
var stateOnlyArguments = aws.AWSProvider{}.StateOnlyArguments()

// emulatorGaps are attributes Floci leaves out of its API responses where
// AWS returns them, so the plan sets the provider's default. Each one is a
// difference from AWS, not from infraharvest's output.
var emulatorGaps = map[string][]string{
	// DescribeServices has no deploymentConfiguration.
	"aws_ecs_service": {"deployment_maximum_percent", "deployment_minimum_healthy_percent"},
	// DescribeTargetGroupAttributes has no target_group_health.* keys.
	"aws_lb_target_group": {"target_group_health"},
	// Unverified: the plan marks these computed attributes unknown after an
	// import. Check on a real account (sandbox, P0-04) whether AWS does too.
	"aws_nat_gateway": {"regional_nat_gateway_address", "secondary_allocation_ids", "secondary_private_ip_addresses"},
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

// e2eEngine returns the engine E2E_ENGINE names: terraform (the default) or
// tofu.
func e2eEngine(t *testing.T) (string, engine.Binary) {
	t.Helper()
	switch name := os.Getenv("E2E_ENGINE"); name {
	case "", "terraform":
		return "terraform", engine.TerraformBinary
	case "tofu":
		return name, engine.TofuBinary
	default:
		t.Fatalf("E2E_ENGINE=%s: use terraform or tofu", name)
		return "", engine.Binary{}
	}
}

// importAWS runs infraharvest import aws with engineName on the services
// testdata/aws covers, into a new directory it returns.
func importAWS(ctx context.Context, t *testing.T, engineName, execPath string, selectionArgs ...string) string {
	t.Helper()
	out := t.TempDir()
	root := cmd.NewCmdRoot()
	root.SetArgs(append([]string{
		"import", "aws",
		"--engine=" + engineName,
		"--terraform-path=" + execPath,
		"--regions=us-east-1",
		"--resources=" + strings.Join(awsServices, ","),
		"--path-output=" + out,
		// The default layout, one root per account and region, and a state
		// backend in the emulator (see stateBackendConfig).
		"--config=" + stateBackendConfig(t),
		// Keep going after a failed directory so one run reports every
		// problem; the checks on coverage.json still fail the test.
		"--allow-partial",
	}, selectionArgs...))
	if err := root.ExecuteContext(ctx); cmd.ExitCode(err) != report.ExitOK && cmd.ExitCode(err) != report.ExitPartial {
		t.Fatalf("infraharvest import: %v", err)
	}
	return out
}

// outputFiles returns the files an import wrote under out, by path relative
// to it, without Terraform's working directories.
func outputFiles(t *testing.T, out string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".terraform" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(out, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = readFile(t, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// gateChange matches a plan check's detail about an update:
// "aws_ecs_service.web: update (deployment_maximum_percent, ...)".
var gateChange = regexp.MustCompile(`^(\S+): update \((.*)\)$`)

// emulatorGapsOnly reports whether every change a failed plan check
// details only sets arguments the emulator leaves out (see emulatorGaps),
// along with arguments the provider keeps only in state.
func emulatorGapsOnly(details []string) bool {
	for _, d := range details {
		if strings.Contains(d, "only state-only or secret arguments change") {
			continue
		}
		m := gateChange.FindStringSubmatch(d)
		if m == nil {
			return false
		}
		typ, _, _ := strings.Cut(m[1], ".")
		for _, a := range strings.Split(m[2], ", ") {
			if !slices.Contains(emulatorGaps[typ], a) && !slices.Contains(stateOnlyArguments[typ], a) {
				return false
			}
		}
	}
	return true
}

// discoverAWS lists the services testdata/aws covers into a selection file
// and checks it includes every resource the test created.
func discoverAWS(ctx context.Context, t *testing.T, created []*tfjson.StateResource) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "selection.yaml")
	root := cmd.NewCmdRoot()
	root.SetArgs([]string{
		"discover", "aws",
		"--regions=us-east-1",
		"--resources=" + strings.Join(awsServices, ","),
		"--selection=" + path,
	})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("infraharvest discover: %v", err)
	}
	f, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	excluded := 0
	for _, r := range f.Resources {
		if !r.Include {
			excluded++
			t.Logf("excluded by default: %s %s (%s)", r.Type, r.ID, r.Reason)
		}
	}
	for _, r := range created {
		id, ok := r.AttributeValues["id"].(string)
		if !ok {
			continue
		}
		for _, listed := range f.Resources {
			if listed.Type == r.Type && listed.ID == id && !listed.Include {
				t.Errorf("discover excludes %s %s, which the test created: %s", r.Type, id, listed.Reason)
			}
		}
	}
	if excluded == 0 {
		t.Error("discover excluded nothing, though the emulator's default VPC exists")
	}
	return path
}

// stateBucket is the bucket testdata/aws creates for the generated roots'
// state.
const stateBucket = "infraharvest-e2e-state"

// stateBackendConfig writes a configuration file that gives the generated
// roots an S3 backend in the emulator.
func stateBackendConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "infraharvest.yaml")
	content := "version: 1\nbackend:\n  s3:\n    bucket: " + stateBucket + "\n    region: us-east-1\n    key_prefix: e2e\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
