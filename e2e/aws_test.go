// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// awsServices are the infraharvest services that import what
// testdata/aws creates.
var awsServices = []string{
	"alb", "auto_scaling", "cloudwatch", "dynamodb", "ebs", "ecr", "ecs", "eip", "iam", "igw",
	"kinesis", "kms", "logs", "nacl", "nat", "route53", "route_table", "s3",
	"secretsmanager", "sfn", "sg", "sns", "sqs", "ssm", "subnet", "vpc",
	"vpc_endpoint",
}

// awsEstate is testdata/aws, created in the AWS emulator for one test,
// and the engine that test imports it with.
type awsEstate struct {
	ctx                  context.Context
	engineName, execPath string
	pluginCache          string
	state                []*tfjson.StateResource
}

// awsSeeded is what seedAWS created in this process. The emulator can't
// create testdata/aws twice, so the tests run against one Floci share it;
// CI runs each on a runner of its own, with a Floci of its own (see the
// e2e-aws job).
var awsSeeded struct {
	sync.Mutex
	state []*tfjson.StateResource
}

// seedAWS creates testdata/aws in the AWS emulator at AWS_ENDPOINT_URL,
// with Terraform (or OpenTofu with E2E_ENGINE=tofu), unless an earlier test
// did, skipping the test when there is no emulator.
func seedAWS(t *testing.T) awsEstate {
	t.Helper()
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	t.Cleanup(cancel)
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	pluginCache := filepath.Join(cache, "plugins")
	awsSeeded.Lock()
	defer awsSeeded.Unlock()
	if awsSeeded.state == nil {
		awsSeeded.state = seed(ctx, t, "aws", execPath, pluginCache)
	}
	return awsEstate{ctx: ctx, engineName: engineName, execPath: execPath, pluginCache: pluginCache, state: awsSeeded.state}
}

// TestAWSSelection checks discover and what imports through its
// selection file: discover lists everything into the file, importing what
// it selects gives the same files as --all, as importing the unchanged
// estate again must (G7), and --managed-state and --assume-role work.
func TestAWSSelection(t *testing.T) {
	e := seedAWS(t)
	out := importAWS(e.ctx, t, e.engineName, e.execPath, "--all")
	selectionFile := discoverAWS(e.ctx, t, e.state)
	checkManagedState(e.ctx, t, e.state)
	checkAssumeRole(e.ctx, t, e.state)
	again := importAWS(e.ctx, t, e.engineName, e.execPath, "--selection="+selectionFile)
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
}

// TestAWSIncremental checks --incremental adds what an import through a
// selection file left out to the roots it generated (see
// checkIncremental).
func TestAWSIncremental(t *testing.T) {
	e := seedAWS(t)
	checkIncremental(e.ctx, t, e.engineName, e.execPath, e.pluginCache, discoverAWS(e.ctx, t, e.state), e.state)
}

// TestAWSRoundTrip creates resources in an AWS emulator, imports them with
// Terraform (or OpenTofu with E2E_ENGINE=tofu) and checks the engine plans every
// generated resource as an import with no changes. Run it against Floci:
//
//	docker compose -f e2e/compose.yaml up -d --wait
//	AWS_ENDPOINT_URL=http://localhost:4566 go test -tags e2e,minimal,aws ./e2e/
func TestAWSRoundTrip(t *testing.T) {
	e := seedAWS(t)
	ctx, engineName, execPath, state := e.ctx, e.engineName, e.execPath, e.state

	out := importAWS(ctx, t, engineName, execPath, "--all")
	// Running again with --resume generates no root again: nothing changed.
	before := generatedTimes(t, out)
	importAWS(ctx, t, engineName, execPath, "--all", "--resume", "--out="+out)
	if after := generatedTimes(t, out); !reflect.DeepEqual(before, after) {
		t.Errorf("--resume generated roots again: %v, then %v", before, after)
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
			if !c.Passed && (c.Name != gate.CheckPlan || !emulatorGapsOnly(c.Details)) {
				t.Errorf("%s: %s failed: %v", d.Path, c.Name, c.Details)
			}
		}
	}
	imported := map[string]int{}
	var lock string
	// The network resources refer to the VPC instead of repeating its ID;
	// the plans below check that changes nothing.
	referencedVPC := false
	// The ALB refers to the default security group, which the selection
	// leaves out, through a data source.
	readsDefaultGroup := false
	for _, dir := range generatedDirs(t, out) {
		if rejected, err := os.ReadFile(filepath.Join(dir, engine.RejectedFileName)); err == nil {
			t.Errorf("%s: resources left out:\n%s", dir, rejected)
		}
		// One root per account and region, each with its own state key.
		rel, err := filepath.Rel(out, dir)
		if err != nil {
			t.Fatal(err)
		}
		backend := readFile(t, filepath.Join(dir, importer.BackendFileName))
		if wantKey := "e2e/" + filepath.ToSlash(rel) + "/terraform.tfstate"; !strings.HasPrefix(filepath.ToSlash(rel), "aws/000000000000/") || !strings.Contains(backend, wantKey) || !strings.Contains(backend, "use_lockfile = true") {
			t.Errorf("%s: want a root per account and region with state key %s:\n%s", rel, wantKey, backend)
		}
		versions := readFile(t, filepath.Join(dir, engine.VersionsFileName))
		if !strings.Contains(versions, "required_version") || !strings.Contains(versions, `version = "~> `) {
			t.Errorf("%s: %s doesn't pin Terraform and the provider:\n%s", dir, engine.VersionsFileName, versions)
		}
		if internalLabel.MatchString(readFile(t, filepath.Join(dir, engine.ImportsFileName))) {
			t.Errorf("%s: an import address has an r-- label", dir)
		}
		readFile(t, filepath.Join(dir, engine.ReadmeFileName))
		if strings.Contains(configuration(t, dir), "vpc_id = aws_vpc.") {
			referencedVPC = true
		}
		if strings.Contains(configuration(t, dir), "data.aws_security_group.") {
			readsDefaultGroup = strings.Contains(readFile(t, filepath.Join(dir, engine.DataFileName)), `data "aws_security_group"`)
		}
		// Arguments that only repeat a default are left out.
		if nulls := nullArguments(t, dir); len(nulls) > 0 {
			t.Errorf("%s: arguments set to null: %v", dir, nulls)
		}
		// One provider version for the whole import.
		if content := readFile(t, filepath.Join(dir, tfrun.LockFileName)); lock == "" {
			lock = content
		} else if content != lock {
			t.Errorf("%s: %s differs from the other directories'", dir, tfrun.LockFileName)
		}
		for typ, n := range checkNoChanges(ctx, t, dir, execPath, e.pluginCache, secretValues(t, dir, state)) {
			imported[typ] += n
		}
	}
	if !referencedVPC {
		t.Error("no root refers to the VPC")
	}
	if !readsDefaultGroup {
		t.Errorf("no root reads the default security group through %s", engine.DataFileName)
	}
	// Each bucket becomes a call of the curated S3 module, the role shaped
	// like the iam-role module a call of it, the VPC built like the vpc
	// module a call of it, and the two other roles, of the same shape,
	// share a generated local module; the plans above check that changes
	// nothing.
	calls := map[string]int{}
	for _, d := range coverage.Directories {
		for _, m := range d.Modules {
			if m.Declined != "" {
				continue
			}
			// A directory's module is called by its path from the root.
			if source := path.Join(d.Path, m.Source); strings.HasSuffix(source, "/testdata/modules/queue") {
				calls[queueModule(t)]++
				continue
			}
			calls[m.Source]++
		}
	}
	for source, want := range map[string]int{adapters.S3Bucket().Source: 3, adapters.IAMRole().Source: 1, adapters.VPC().Source: 1, queueModule(t): 1} {
		if calls[source] < want {
			t.Errorf("%d calls of %s, want %d or more; modules:", calls[source], source, want)
			for _, d := range coverage.Directories {
				declined := false
				for _, m := range d.Modules {
					t.Logf("%s: %+v", d.Path, m)
					declined = declined || (m.Source == source && m.Declined != "")
				}
				// What the adapter declined, as it read it.
				if declined {
					t.Logf("%s:\n%s", d.Path, configuration(t, filepath.Join(out, d.Path)))
				}
			}
		}
	}
	modules, err := filepath.Glob(filepath.Join(out, engine.ModulesDirName, "iam_role_*", "main.tf"))
	if err != nil || len(modules) == 0 {
		t.Errorf("no generated IAM role module under %s (%v)", engine.ModulesDirName, err)
	}
	seeded := map[string]int{}
	for _, r := range state {
		if r.Type == "aws_cloudformation_stack" {
			continue // it only owns a bucket, which discover leaves out
		}
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

func secretValues(t *testing.T, dir string, created []*tfjson.StateResource) []tfexec.PlanOption {
	t.Helper()
	var vars []tfexec.PlanOption
	blocks := configBlocks(t, dir)
	// Variables with a default, such as those for another root's
	// resources, need no value: only secrets have none.
	defaulted := map[string]bool{}
	for _, block := range blocks {
		if block.Type == "variable" && len(block.Labels) == 1 {
			if _, ok := block.Body.Attributes["default"]; ok {
				defaulted[block.Labels[0]] = true
			}
		}
	}
	for _, block := range blocks {
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
			if defaulted[variable] {
				continue
			}
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

// importAWS runs infraharvest import aws with engineName on the services
// testdata/aws covers, into a new directory it returns.
func importAWS(ctx context.Context, t *testing.T, engineName, execPath string, selectionArgs ...string) string {
	t.Helper()
	out := t.TempDir()
	root := cli.NewCmdRoot()
	root.SetArgs(append([]string{
		"import", "aws",
		"--engine=" + engineName,
		"--terraform-path=" + execPath,
		"--regions=us-east-1",
		"--services=" + strings.Join(awsServices, ","),
		"--out=" + out,
		// The default layout, one root per account and region, and a state
		// backend in the emulator (see stateBackendConfig).
		"--config=" + stateBackendConfig(t),
		// Keep going after a failed directory so one run reports every
		// problem; the checks on coverage.json still fail the test.
		"--allow-partial",
	}, selectionArgs...))
	if err := root.ExecuteContext(ctx); importer.ExitCode(err) != report.ExitOK && importer.ExitCode(err) != report.ExitPartial {
		t.Fatalf("infraharvest import: %v", err)
	}
	return out
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
			if !slices.Contains(emulatorGaps[typ], a) && !stateOnly(typ, map[string]string{a: ""}) {
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
	root := cli.NewCmdRoot()
	root.SetArgs([]string{
		"discover", "aws",
		"--regions=us-east-1",
		"--services=" + strings.Join(awsServices, ","),
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
	// The bucket a CloudFormation stack owns is left out, naming the stack.
	cfnBucket := false
	for _, r := range f.Resources {
		if r.Type == "aws_s3_bucket" && r.ID == "infraharvest-e2e-cfn-assets" {
			cfnBucket = true
			if r.Include || !strings.Contains(r.Reason, "CloudFormation stack infraharvest-e2e-app") {
				t.Errorf("the bucket of a CloudFormation stack: include=%v %q", r.Include, r.Reason)
			}
		}
	}
	if !cfnBucket {
		t.Error("discover didn't list the bucket of the CloudFormation stack")
	}
	// Tags come from the Resource Groups Tagging API, for rules by tag: a
	// queue's ID is its URL, which names the queue its ARN does. Floci's
	// Tagging API lists the services its Resource Explorer indexes, which
	// EC2 isn't one of.
	for _, r := range created {
		if r.Type != "aws_sqs_queue" {
			continue
		}
		id, _ := r.AttributeValues["id"].(string)
		for _, listed := range f.Resources {
			if listed.Type == "aws_sqs_queue" && listed.ID == id && listed.Tags["Project"] != "infraharvest-e2e" {
				t.Errorf("discover recorded the tags of queue %s as %v, want Project=infraharvest-e2e", id, listed.Tags)
			}
		}
	}
	if excluded == 0 {
		t.Error("discover excluded nothing, though the emulator's default VPC exists")
	}
	return path
}

// queueModule is the directory of a queue module of the organisation's own,
// which the queue becomes a call of.
func queueModule(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", "modules", "queue"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// stateBucket is the bucket testdata/aws creates for the generated roots'
// state.
const stateBucket = "infraharvest-e2e-state"

// stateBackendConfig writes a configuration file that gives the generated
// roots an S3 backend in the emulator, and lists the organisation's own
// queue module (see queueModule).
func stateBackendConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "infraharvest.yaml")
	content := "version: 1\nbackend:\n  s3:\n    bucket: " + stateBucket + "\n    region: us-east-1\n    key_prefix: e2e\nmodules:\n  - source: " + strconv.Quote(queueModule(t)) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// nullArguments returns the arguments the root in dir sets to null, in
// resources and their nested blocks.
func nullArguments(t *testing.T, dir string) []string {
	t.Helper()
	var nulls []string
	var walk func(prefix string, body *hclsyntax.Body)
	walk = func(prefix string, body *hclsyntax.Body) {
		for name, attr := range body.Attributes {
			if lit, ok := attr.Expr.(*hclsyntax.LiteralValueExpr); ok && lit.Val.IsNull() {
				nulls = append(nulls, prefix+"."+name)
			}
		}
		for _, b := range body.Blocks {
			walk(prefix+"."+b.Type, b.Body)
		}
	}
	for _, b := range configBlocks(t, dir) {
		if b.Type == "resource" {
			walk(strings.Join(b.Labels, "."), b.Body)
		}
	}
	sort.Strings(nulls)
	return nulls
}

// checkManagedState runs discover with --managed-state on a state file
// that holds the created SQS queues and SNS topics: discover must leave
// them out as already managed.
func checkManagedState(ctx context.Context, t *testing.T, created []*tfjson.StateResource) {
	t.Helper()
	type instance struct {
		Attributes map[string]any `json:"attributes"`
	}
	type resource struct {
		Mode      string     `json:"mode"`
		Type      string     `json:"type"`
		Name      string     `json:"name"`
		Instances []instance `json:"instances"`
	}
	var resources []resource
	managedIDs := map[string]bool{}
	for _, r := range created {
		id, ok := r.AttributeValues["id"].(string)
		if !ok || (r.Type != "aws_sqs_queue" && r.Type != "aws_sns_topic") {
			continue
		}
		resources = append(resources, resource{Mode: "managed", Type: r.Type, Name: r.Name, Instances: []instance{{Attributes: map[string]any{"id": id}}}})
		managedIDs[r.Type+" "+id] = true
	}
	content, err := json.Marshal(map[string]any{"version": 4, "resources": resources})
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "terraform.tfstate")
	if err := os.WriteFile(statePath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "selection.yaml")
	root := cli.NewCmdRoot()
	root.SetArgs([]string{"discover", "aws", "--regions=us-east-1", "--services=sqs,sns", "--selection=" + path, "--managed-state=" + statePath})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("infraharvest discover --managed-state: %v", err)
	}
	f, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, r := range f.Resources {
		if !managedIDs[r.Type+" "+r.ID] {
			continue
		}
		found++
		if r.Include || !strings.HasPrefix(r.Reason, report.TerraformPrefix) {
			t.Errorf("%s %s is in the state but not excluded as managed: include=%v %q", r.Type, r.ID, r.Include, r.Reason)
		}
	}
	if found != len(managedIDs) || found == 0 {
		t.Errorf("found %d of the %d managed resources in the selection", found, len(managedIDs))
	}
}

// checkAssumeRole lists the SQS queues through a role: listing works with
// the role's credentials.
func checkAssumeRole(ctx context.Context, t *testing.T, created []*tfjson.StateResource) {
	t.Helper()
	withRole(ctx, t, "infraharvest-readonly", func() { listThroughRole(ctx, t, created) })
}

func listThroughRole(ctx context.Context, t *testing.T, created []*tfjson.StateResource) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "selection.yaml")
	root := cli.NewCmdRoot()
	root.SetArgs([]string{"discover", "aws", "--regions=us-east-1", "--services=sqs", "--selection=" + path, "--assume-role=arn:aws:iam::000000000000:role/infraharvest-readonly"})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("infraharvest discover --assume-role: %v", err)
	}
	f, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range created {
		if r.Type != "aws_sqs_queue" {
			continue
		}
		if id, _ := r.AttributeValues["id"].(string); !f.Has(r.Type, id) {
			t.Errorf("listing through the role misses %s %s", r.Type, id)
		}
	}
}

// internalLabel matches an import address whose label has the r-- prefix
// of the escaped labels the listers once made: the engine labels resources
// from their names.
var internalLabel = regexp.MustCompile(`\bto\s*=\s*\S*\.r--`)

// referencesVPC matches an argument that refers to a VPC resource.
var referencesVPC = regexp.MustCompile(`vpc_id\s+= aws_vpc\.`)

// incrementalTypes are the types the first import of checkIncremental
// leaves out, for the incremental import to add.
var incrementalTypes = []string{"aws_sqs_queue", "aws_subnet"}

// checkIncremental imports everything selectionFile selects but
// incrementalTypes, then imports again with --incremental: the second
// import adds those resources to the roots, in a file of their own,
// referring to what the roots have, and changes nothing else. A third adds
// nothing. Then a queue it adds and deletes is reported gone (see
// checkNoLongerInCloud).
func checkIncremental(ctx context.Context, t *testing.T, engineName, execPath, pluginCache, selectionFile string, created []*tfjson.StateResource) {
	t.Helper()
	f, err := selection.Load(selectionFile)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range f.Resources {
		if slices.Contains(incrementalTypes, r.Type) {
			f.Resources[i].Include = false
		}
	}
	partial := filepath.Join(t.TempDir(), "selection.yaml")
	if err := f.Save(partial); err != nil {
		t.Fatal(err)
	}
	out := importAWS(ctx, t, engineName, execPath, "--selection="+partial)
	before := outputFiles(t, out)

	importAWS(ctx, t, engineName, execPath, "--selection="+selectionFile, "--incremental", "--out="+out)
	after := outputFiles(t, out)
	added := addedConfiguration(t, before, after)
	for name, content := range added {
		if strings.Contains(content, `resource "aws_subnet"`) && !referencesVPC.MatchString(content) {
			t.Errorf("what --incremental added to %s doesn't refer to the VPC the root has:\n%s", name, content)
		}
	}
	if len(added) == 0 {
		t.Fatal("--incremental added no resources")
	}
	var coverage report.Report
	if err := json.Unmarshal([]byte(after[report.Dir+"/coverage.json"]), &coverage); err != nil {
		t.Fatal(err)
	}
	inRoot := 0
	for _, e := range coverage.Excluded {
		if e.Owner == report.OwnerTerraformRoot {
			inRoot++
		}
	}
	if inRoot == 0 || coverage.Totals.LeftOut != 0 || coverage.Totals.Failed != 0 || len(coverage.Failures) != 0 {
		t.Errorf("coverage.json of --incremental: %d in the roots already, %+v, failures %v", inRoot, coverage.Totals, coverage.Failures)
	}
	// Everything the roots have is still there.
	if len(coverage.NotInCloud) != 0 {
		t.Errorf("--incremental reports what the emulator has as no longer in the cloud: %+v", coverage.NotInCloud)
	}
	for _, d := range coverage.Directories {
		for _, c := range d.Checks {
			if !c.Passed && (c.Name != gate.CheckPlan || !emulatorGapsOnly(c.Details)) {
				t.Errorf("--incremental: %s: %s failed: %v", d.Path, c.Name, c.Details)
			}
		}
	}
	imported := map[string]int{}
	for _, dir := range generatedDirs(t, out) {
		for typ, n := range checkNoChanges(ctx, t, dir, execPath, pluginCache, secretValues(t, dir, created)) {
			imported[typ] += n
		}
	}
	for _, typ := range incrementalTypes {
		want := 0
		for _, r := range created {
			if r.Type == typ {
				want++
			}
		}
		if imported[typ] < want {
			t.Errorf("%s: created %d, the roots import %d after --incremental", typ, want, imported[typ])
		}
	}

	importAWS(ctx, t, engineName, execPath, "--selection="+selectionFile, "--incremental", "--out="+out)
	for name := range addedConfiguration(t, after, outputFiles(t, out)) {
		t.Errorf("--incremental with nothing new wrote to %s", name)
	}
	checkNoLongerInCloud(ctx, t, engineName, execPath, selectionFile, out)
}
