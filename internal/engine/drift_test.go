// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

// fakeDriftTerraform is a DriftTerraform for Drift's tests: it records
// its calls, prefixed with its name, into calls, which the root's and the
// staged copy's share, and returns plans in order from ShowPlanFile.
type fakeDriftTerraform struct {
	name      string
	calls     *[]string
	state     string        // what StatePull returns
	shown     *tfjson.State // what ShowStateFile returns
	plans     []*tfjson.Plan
	initOpts  [][]tfexec.InitOption
	planOpts  [][]tfexec.PlanOption
	applyOpts [][]tfexec.ApplyOption
	onInit    func()
	schemas   *tfjson.ProviderSchemas

	// unformatted are the files FormatCheck finds unformatted.
	unformatted []string
}

func (f *fakeDriftTerraform) record(call string) { *f.calls = append(*f.calls, f.name+":"+call) }

func (f *fakeDriftTerraform) Init(_ context.Context, opts ...tfexec.InitOption) error {
	f.record("init")
	f.initOpts = append(f.initOpts, opts)
	if f.onInit != nil {
		f.onInit()
	}
	return nil
}

func (f *fakeDriftTerraform) PlanJSON(_ context.Context, w io.Writer, opts ...tfexec.PlanOption) (bool, error) {
	f.record("plan")
	f.planOpts = append(f.planOpts, opts)
	_, err := io.WriteString(w, `{"type":"change_summary","changes":{"add":0,"change":0,"remove":0,"import":0}}`+"\n")
	return false, err
}

func (f *fakeDriftTerraform) Validate(context.Context) (*tfjson.ValidateOutput, error) {
	f.record("validate")
	return &tfjson.ValidateOutput{Valid: true}, nil
}

func (f *fakeDriftTerraform) ProvidersSchema(context.Context) (*tfjson.ProviderSchemas, error) {
	f.record("schema")
	if f.schemas != nil {
		return f.schemas, nil
	}
	return &tfjson.ProviderSchemas{}, nil
}

func (f *fakeDriftTerraform) ShowPlanFile(context.Context, string, ...tfexec.ShowOption) (*tfjson.Plan, error) {
	f.record("show")
	if len(f.plans) == 0 {
		return &tfjson.Plan{}, nil
	}
	p := f.plans[0]
	f.plans = f.plans[1:]
	return p, nil
}

func (f *fakeDriftTerraform) FormatCheck(context.Context, ...tfexec.FormatOption) (bool, []string, error) {
	f.record("fmt")
	return len(f.unformatted) == 0, f.unformatted, nil
}

func (f *fakeDriftTerraform) SetStdout(io.Writer) {}

func (f *fakeDriftTerraform) StatePull(context.Context, ...tfexec.StatePullOption) (string, error) {
	f.record("state-pull")
	return f.state, nil
}

func (f *fakeDriftTerraform) Apply(_ context.Context, opts ...tfexec.ApplyOption) error {
	f.record("apply")
	f.applyOpts = append(f.applyOpts, opts)
	return nil
}

func (f *fakeDriftTerraform) ShowStateFile(context.Context, string, ...tfexec.ShowOption) (*tfjson.State, error) {
	f.record("show-state")
	if f.shown == nil {
		return &tfjson.State{}, nil
	}
	return f.shown, nil
}

// hasOption reports whether opts has one equal to want.
func hasOption[T any](opts []T, want T) bool {
	return slices.ContainsFunc(opts, func(o T) bool { return reflect.DeepEqual(o, want) })
}

const driftState = `{"version":4,"resources":[{"mode":"managed","type":"aws_s3_bucket","name":"logs","instances":[{"attributes":{"bucket":"logs","secret":"in-state-only"}}]}]}`

const driftMain = `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags = {
    Env = "prod"
  }
}

resource "aws_security_group" "web" {
  name        = "web"
  description = "a"
  ingress {
    from_port = 443
  }
}

module "data" {
  source = "../../../modules/bucket"
}
`

// driftRoot writes a root at aws/123/eu-west-2 of an output directory
// with modules/bucket, with files besides main.tf, and returns the root
// and where Drift stages it.
func driftRoot(t *testing.T, files map[string]string) (dir, staging string) {
	t.Helper()
	out := t.TempDir()
	dir = filepath.Join(out, "aws", "123", "eu-west-2")
	for _, d := range []string{dir, filepath.Join(out, "modules", "bucket")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files["main.tf"] = driftMain
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, filepath.Join(out, ".infraharvest", "d")
}

func driftResource(address, typ string, actions tfjson.Actions, before, after any) *tfjson.ResourceChange {
	rc := &tfjson.ResourceChange{Address: address, Type: typ, Mode: tfjson.ManagedResourceMode, Change: &tfjson.Change{Actions: actions, Before: before, After: after}}
	if strings.HasPrefix(address, "module.") {
		parts := strings.SplitN(address, ".", 3)
		rc.ModuleAddress = parts[0] + "." + parts[1]
	}
	return rc
}

func sensitiveChange(rc *tfjson.ResourceChange, mask any) *tfjson.ResourceChange {
	rc.Change.BeforeSensitive, rc.Change.AfterSensitive = mask, mask
	return rc
}

func TestDriftClassifiesEachChange(t *testing.T) {
	t.Parallel()
	dir, staging := driftRoot(t, map[string]string{
		"backend.tf":   "terraform {\n  backend \"s3\" {\n    bucket = \"state\"\n    key    = \"k\"\n  }\n}\n",
		"variables.tf": "variable \"drift_test_db_password\" {\n  type      = string\n  sensitive = true\n}\n",
		"imports.tf":   "import {\n  to = aws_s3_bucket.logs\n  id = \"logs\"\n}\n\nimport {\n  to = aws_s3_bucket.fresh\n  id = \"fresh\"\n}\n",
	})
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
		driftResource("aws_s3_bucket.logs", "aws_s3_bucket", tfjson.Actions{tfjson.ActionUpdate},
			map[string]any{"bucket": "logs", "arn": "arn:1", "force_destroy": false, "tags": map[string]any{"Env": "prod"}, "tags_all": map[string]any{"Env": "prod"}, "password_wo": nil},
			map[string]any{"bucket": "logs", "arn": "arn:2", "force_destroy": true, "tags": map[string]any{"Env": "dev"}, "tags_all": map[string]any{"Env": "dev"}, "password_wo": "x"}),
		driftResource("aws_security_group.web", "aws_security_group", tfjson.Actions{tfjson.ActionUpdate},
			map[string]any{"description": "a", "ingress": []any{map[string]any{"cidr_blocks": []any{"10.0.0.0/8"}}}},
			map[string]any{"description": "rotated new-secret-value", "ingress": []any{map[string]any{"cidr_blocks": []any{"0.0.0.0/0"}}}}),
		driftResource("module.data.aws_s3_bucket.this", "aws_s3_bucket", tfjson.Actions{tfjson.ActionUpdate},
			map[string]any{"tags": map[string]any{"Team": "a"}},
			map[string]any{"tags": map[string]any{"Team": "b"}}),
		sensitiveChange(driftResource("aws_db_instance.db", "aws_db_instance", tfjson.Actions{tfjson.ActionUpdate},
			map[string]any{"password": "old-secret-value"},
			map[string]any{"password": "new-secret-value"}), map[string]any{"password": true}),
		driftResource("aws_sqs_queue.gone", "aws_sqs_queue", tfjson.Actions{tfjson.ActionDelete}, map[string]any{"name": "gone"}, nil),
	}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		driftResource("aws_s3_bucket.logs", "aws_s3_bucket", tfjson.Actions{tfjson.ActionUpdate},
			map[string]any{"tags": map[string]any{"Env": "dev"}, "tags_all": map[string]any{"Env": "dev"}},
			map[string]any{"tags": map[string]any{"Env": "prod"}, "tags_all": map[string]any{"Env": "prod"}}),
		driftResource("aws_security_group.web", "aws_security_group", tfjson.Actions{tfjson.ActionDelete, tfjson.ActionCreate},
			map[string]any{"ingress": []any{map[string]any{"cidr_blocks": []any{"0.0.0.0/0"}}}},
			map[string]any{"ingress": []any{map[string]any{"cidr_blocks": []any{"10.0.0.0/8"}}}}),
		driftResource("module.data.aws_s3_bucket.this", "aws_s3_bucket", tfjson.Actions{tfjson.ActionNoop}, map[string]any{}, map[string]any{}),
		sensitiveChange(driftResource("aws_db_instance.db", "aws_db_instance", tfjson.Actions{tfjson.ActionUpdate},
			map[string]any{"password": "new-secret-value"},
			map[string]any{"password": placeholderString}), map[string]any{"password": true}),
		driftResource("aws_sqs_queue.gone", "aws_sqs_queue", tfjson.Actions{tfjson.ActionCreate}, nil, map[string]any{"name": "gone"}),
	}}
	importing := driftResource("aws_s3_bucket.fresh", "aws_s3_bucket", tfjson.Actions{tfjson.ActionNoop}, map[string]any{}, map[string]any{})
	importing.Change.Importing = &tfjson.Importing{ID: "fresh"}
	code.ResourceChanges = append(code.ResourceChanges, importing)

	var calls []string
	root := &fakeDriftTerraform{name: "root", calls: &calls, state: driftState}
	copied := &fakeDriftTerraform{name: "staged", calls: &calls, plans: []*tfjson.Plan{refresh, code}, shown: &tfjson.State{Values: &tfjson.StateValues{RootModule: &tfjson.StateModule{
		Resources: []*tfjson.StateResource{{Address: "aws_s3_bucket.logs", Mode: tfjson.ManagedResourceMode}},
	}}}}
	var stagedFiles map[string]string
	var stagedMode, stateMode os.FileMode
	copied.onInit = func() {
		stagedFiles = dirFiles(t, staging)
		if info, err := os.Stat(staging); err == nil {
			stagedMode = info.Mode().Perm()
		}
		if info, err := os.Stat(filepath.Join(staging, stagedStateFile)); err == nil {
			stateMode = info.Mode().Perm()
		}
	}
	var stagedDir string
	result, err := Drift(context.Background(), root, dir, staging, func(d string) (DriftTerraform, error) {
		stagedDir = d
		return copied, nil
	}, DriftOptions{StateOnly: map[string][]string{"aws_s3_bucket": {"force_destroy"}}})
	if err != nil {
		t.Fatal(err)
	}

	wantCalls := []string{"root:init", "root:state-pull", "staged:init", "staged:show-state", "staged:plan", "staged:show", "staged:apply", "staged:plan", "staged:show"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Errorf("calls %v, want %v", calls, wantCalls)
	}
	if stagedDir != staging {
		t.Errorf("staged Terraform for %s, want %s", stagedDir, staging)
	}
	if len(root.initOpts) != 1 || !hasOption(root.initOpts[0], tfexec.InitOption(tfexec.Backend(true))) || !hasOption(root.initOpts[0], tfexec.InitOption(tfexec.Reconfigure(true))) {
		t.Errorf("the root's init options %#v, want -backend=true -reconfigure", root.initOpts)
	}
	if len(copied.planOpts) != 2 {
		t.Fatalf("%d plans, want 2", len(copied.planOpts))
	}
	refreshOpts, codeOpts := copied.planOpts[0], copied.planOpts[1]
	for _, want := range []tfexec.PlanOption{tfexec.RefreshOnly(true), tfexec.Lock(false), tfexec.Var("drift_test_db_password=" + placeholderString), tfexec.Out(filepath.Join(staging, "refresh.tfplan"))} {
		if !hasOption(refreshOpts, want) {
			t.Errorf("the refresh-only plan has no option %#v", want)
		}
	}
	for _, want := range []tfexec.PlanOption{tfexec.Refresh(false), tfexec.Lock(false), tfexec.Out(filepath.Join(staging, "code.tfplan"))} {
		if !hasOption(codeOpts, want) {
			t.Errorf("the plan of the code has no option %#v", want)
		}
	}
	if hasOption(codeOpts, tfexec.PlanOption(tfexec.RefreshOnly(true))) {
		t.Error("the plan of the code refreshes")
	}
	if len(copied.applyOpts) != 1 || !hasOption(copied.applyOpts[0], tfexec.ApplyOption(tfexec.DirOrPlan(filepath.Join(staging, "refresh.tfplan")))) {
		t.Errorf("apply options %#v, want the refresh-only plan", copied.applyOpts)
	}

	// The staged copy.
	if _, ok := stagedFiles["backend.tf"]; ok {
		t.Error("the staged copy has backend.tf")
	}
	if got := stagedFiles[stagedStateFile]; got != driftState {
		t.Errorf("staged state %q, want the pulled state", got)
	}
	if !strings.Contains(stagedFiles["main.tf"], `source = "../../modules/bucket"`) {
		t.Errorf("the staged module call doesn't reach modules/bucket:\n%s", stagedFiles["main.tf"])
	}
	if runtime.GOOS != "windows" && (stagedMode != 0o700 || stateMode != 0o600) {
		t.Errorf("staged directory mode %o and state mode %o, want 700 and 600", stagedMode, stateMode)
	}
	if _, err := os.Stat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the staged copy is still there: %v", err)
	}
	if readFile(t, dir, "main.tf") != driftMain {
		t.Error("the root's own main.tf changed")
	}

	if result.Backend != "s3" || result.Resources != 1 {
		t.Errorf("backend %q with %d resources, want s3 with 1", result.Backend, result.Resources)
	}
	if !reflect.DeepEqual(result.NotApplied, []string{"aws_s3_bucket.fresh"}) {
		t.Errorf("not applied %v, want aws_s3_bucket.fresh", result.NotApplied)
	}
	if !reflect.DeepEqual(result.Placeholders, []string{"drift_test_db_password"}) {
		t.Errorf("placeholders %v", result.Placeholders)
	}

	type got struct{ address, attribute, kind, outcome string }
	var gots []got
	for _, c := range result.Changes {
		gots = append(gots, got{c.Address, c.Attribute, c.Kind, c.Outcome})
	}
	want := []got{
		{"aws_db_instance.db", "password", DriftChanged, DriftNotAssessable},
		{"aws_s3_bucket.logs", "arn", DriftChanged, DriftAbsorbed},
		{"aws_s3_bucket.logs", "tags.Env", DriftChanged, DriftKept},
		{"aws_security_group.web", "description", DriftChanged, DriftAbsorbed},
		{"aws_security_group.web", "ingress[0].cidr_blocks[0]", DriftChanged, DriftKept},
		{"aws_sqs_queue.gone", "", DriftDeleted, DriftKept},
		{"module.data.aws_s3_bucket.this", "tags.Team", DriftChanged, DriftAbsorbed},
	}
	if !reflect.DeepEqual(gots, want) {
		t.Errorf("changes:\n%v\nwant:\n%v", gots, want)
	}
	byKey := map[string]DriftChange{}
	for _, c := range result.Changes {
		byKey[c.Address+" "+c.Attribute] = c
	}
	if c := byKey["aws_db_instance.db password"]; c.Before != redacted || c.After != redacted {
		t.Errorf("a sensitive change shows %v -> %v", c.Before, c.After)
	}
	if c := byKey["aws_security_group.web description"]; c.After != "rotated "+redacted {
		t.Errorf("a secret value in another attribute shows as %v", c.After)
	}
	if c := byKey["aws_security_group.web ingress[0].cidr_blocks[0]"]; !strings.Contains(c.Reason, "replaces") {
		t.Errorf("a change the next apply replaces the resource for has the reason %q", c.Reason)
	}
	if c := byKey["aws_s3_bucket.logs tags.Env"]; c.Before != "prod" || c.After != "dev" || c.Source == nil || *c.Source != (DriftSource{File: "main.tf", Line: 4, Expression: `"prod"`}) {
		t.Errorf("tags.Env: %v -> %v at %+v", c.Before, c.After, c.Source)
	}
	if c := byKey["aws_security_group.web description"]; c.Source == nil || *c.Source != (DriftSource{File: "main.tf", Line: 10, Expression: `"a"`}) {
		t.Errorf("description at %+v", c.Source)
	}
	if c := byKey["aws_security_group.web ingress[0].cidr_blocks[0]"]; c.Source == nil || c.Source.Line != 11 {
		t.Errorf("ingress at %+v, want line 11", c.Source)
	}
	if c := byKey["aws_s3_bucket.logs arn"]; c.Source == nil || c.Source.Line != 1 {
		t.Errorf("an argument the code leaves out at %+v, want the block's line 1", c.Source)
	}
	if c := byKey["module.data.aws_s3_bucket.this tags.Team"]; c.Source != nil || c.ModuleAddress != "module.data" {
		t.Errorf("a module's resource has source %+v and module %q", c.Source, c.ModuleAddress)
	}
	for _, c := range result.Changes {
		if c.Decision != DriftKeep {
			t.Errorf("%s %s: decision %q without --accept", c.Address, c.Attribute, c.Decision)
		}
	}
	if slices.Contains(calls, "staged:schema") {
		t.Error("read the schemas with nothing accepted")
	}
	content, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"old-secret-value", "new-secret-value"} {
		if strings.Contains(string(content), secret) {
			t.Errorf("the result holds %q: %s", secret, content)
		}
	}
}

func TestDriftWithoutDriftAppliesNothing(t *testing.T) {
	t.Parallel()
	dir, staging := driftRoot(t, map[string]string{})
	var calls []string
	root := &fakeDriftTerraform{name: "root", calls: &calls, state: driftState}
	copied := &fakeDriftTerraform{name: "staged", calls: &calls, plans: []*tfjson.Plan{{}}}
	result, err := Drift(context.Background(), root, dir, staging, func(string) (DriftTerraform, error) { return copied, nil }, DriftOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"root:init", "root:state-pull", "staged:init", "staged:show-state", "staged:plan", "staged:show"}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls %v, want %v", calls, want)
	}
	if result.Backend != "local" || len(result.Changes) != 0 || len(result.Placeholders) != 0 {
		t.Errorf("result %+v", result)
	}
}

func TestDriftOfARootNeverAppliedPlansNothing(t *testing.T) {
	t.Parallel()
	dir, staging := driftRoot(t, map[string]string{"imports.tf": "import {\n  to = aws_s3_bucket.logs\n  id = \"logs\"\n}\n"})
	var calls []string
	root := &fakeDriftTerraform{name: "root", calls: &calls}
	result, err := Drift(context.Background(), root, dir, staging, func(string) (DriftTerraform, error) {
		t.Error("staged a root without state")
		return nil, errors.New("staged")
	}, DriftOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"root:init", "root:state-pull"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls %v, want %v", calls, want)
	}
	if !reflect.DeepEqual(result.NotApplied, []string{"aws_s3_bucket.logs"}) {
		t.Errorf("not applied %v", result.NotApplied)
	}
	if _, err := os.Stat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staged anyway: %v", err)
	}
}

func TestDriftRefusesRemoteBackends(t *testing.T) {
	t.Parallel()
	for name, backend := range map[string]string{
		"remote": "terraform {\n  backend \"remote\" {\n    organization = \"o\"\n  }\n}\n",
		"cloud":  "terraform {\n  cloud {\n    organization = \"o\"\n  }\n}\n",
		"http":   "terraform {\n  backend \"http\" {}\n}\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir, staging := driftRoot(t, map[string]string{"backend.tf": backend})
			var calls []string
			root := &fakeDriftTerraform{name: "root", calls: &calls, state: driftState}
			_, err := Drift(context.Background(), root, dir, staging, func(string) (DriftTerraform, error) { return root, nil }, DriftOptions{})
			var unsupported *UnsupportedBackendError
			if !errors.As(err, &unsupported) || unsupported.Backend != name {
				t.Fatalf("got %v, want the %s backend refused", err, name)
			}
			if !strings.Contains(err.Error(), "s3, gcs and azurerm") {
				t.Errorf("the error doesn't say what is supported: %v", err)
			}
			if len(calls) != 0 {
				t.Errorf("Terraform ran: %v", calls)
			}
		})
	}
}

func TestStageDriftRemovesBackendBlocksAnywhere(t *testing.T) {
	t.Parallel()
	dir, staging := driftRoot(t, map[string]string{
		"versions.tf":           "terraform {\n  required_version = \">= 1.5\"\n  backend \"gcs\" {\n    bucket = \"state\"\n  }\n}\n",
		"terraform.tfstate":     "old",
		"secrets.auto.tfvars":   "x = 1\n",
		".terraform.lock.hcl":   "# lock\n",
		"terraform.tfstate.bak": "old",
	})
	if err := os.MkdirAll(filepath.Join(dir, ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := stageDrift(dir, staging, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	files := dirFiles(t, staging)
	if strings.Contains(files["versions.tf"], "backend") || !strings.Contains(files["versions.tf"], "required_version") {
		t.Errorf("staged versions.tf:\n%s", files["versions.tf"])
	}
	if files[stagedStateFile] != "{}" {
		t.Errorf("staged state %q", files[stagedStateFile])
	}
	if _, ok := files["terraform.tfstate.bak"]; ok {
		t.Error("staged an old state file")
	}
	for _, name := range []string{"secrets.auto.tfvars", ".terraform.lock.hcl"} {
		if _, ok := files[name]; !ok {
			t.Errorf("didn't stage %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(staging, ".terraform")); !errors.Is(err, os.ErrNotExist) {
		t.Error("staged .terraform")
	}
}

func TestVariablePlaceholders(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "variables.tf", `variable "with_default" {
  type    = string
  default = "x"
}

variable "from_env" {
  type = string
}

variable "from_tfvars" {
  type = number
}

variable "ports" {
  type = list(number)
}

variable "settings" {
  type = map(string)
}

variable "enabled" {
  type = bool
}
`)
	if err := os.WriteFile(filepath.Join(dir, "values.auto.tfvars"), []byte("from_tfvars = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TF_VAR_from_env", "set")
	files, err := configFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	vars, names := variablePlaceholders(dir, files)
	if want := []string{"enabled", "ports", "settings"}; !reflect.DeepEqual(names, want) {
		t.Errorf("placeholders for %v, want %v", names, want)
	}
	want := []tfexec.PlanOption{tfexec.Var("enabled=false"), tfexec.Var("ports=[]"), tfexec.Var("settings={}")}
	if !reflect.DeepEqual(vars, want) {
		t.Errorf("vars %#v, want %#v", vars, want)
	}
}

func TestDriftLeavesFoldTagsAndLabels(t *testing.T) {
	t.Parallel()
	leaves := []valueLeaf{
		{path: "effective_labels.team", before: "a", after: "b"},
		{path: "labels.team", before: "", after: ""},
		{path: "tags_all.Env", before: "prod", after: "dev"},
		{path: "terraform_labels.team", before: "a", after: "b"},
		{path: "token_wo", after: "x"},
	}
	stateOnly := map[string][]string{"*": {"labels", "terraform_labels"}}
	got := driftLeaves("google_storage_bucket", leaves, stateOnly)
	var paths []string
	for _, l := range got {
		paths = append(paths, l.path)
	}
	if want := []string{"labels.team", "tags.Env"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("paths %v, want %v", paths, want)
	}
}

func TestRedactDriftValue(t *testing.T) {
	t.Parallel()
	token := "ghp_" + strings.Repeat("a1B2", 9)
	got := redactDriftValue(map[string]any{"token": token, "name": "web", "list": []any{"hunter22-secret"}}, false, textForms([]string{"hunter22-secret"}))
	want := map[string]any{"token": redacted, "name": "web", "list": []any{redacted}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := redactDriftValue(map[string]any{"a": "b"}, true, nil); got != redacted {
		t.Errorf("a sensitive value shows as %v", got)
	}
}
