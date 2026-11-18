// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/run"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

func TestNeedsFreshGeneration(t *testing.T) {
	t.Parallel()
	failedPlan := gate.Gate{{Name: gate.CheckFormat, Passed: true}, {Name: gate.CheckPlan}}
	failedOther := gate.Gate{{Name: gate.CheckPlan, Passed: true}, {Name: gate.CheckScanners}}
	boom := errors.New("boom")
	for name, tc := range map[string]struct {
		result *Result
		err    error
		want   bool
	}{
		"no snapshot, a failed plan check": {&Result{Gate: failedPlan}, nil, false},
		"a snapshot, a failed plan check":  {&Result{UsedSnapshot: true, Gate: failedPlan}, nil, true},
		"a snapshot, another check failed": {&Result{UsedSnapshot: true, Gate: failedOther}, nil, false},
		"a snapshot, the gate passed":      {&Result{UsedSnapshot: true, Gate: gate.Gate{{Name: gate.CheckPlan, Passed: true}}}, nil, false},
		"a snapshot, an error":             {nil, &SnapshotError{Err: boom}, true},
		"an error without a snapshot":      {nil, boom, false},
		"a snapshot, stopped":              {nil, &SnapshotError{Err: context.Canceled}, false},
		"a snapshot, timed out":            {nil, &SnapshotError{Err: context.DeadlineExceeded}, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := NeedsFreshGeneration(tc.result, tc.err); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
	if err := (&SnapshotError{Err: boom}); !errors.Is(err, boom) || err.Error() != "boom" {
		t.Errorf("SnapshotError: %v", err)
	}
}

func TestSnapshotRootFacts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		"imports.tf": `
import {
  to = aws_s3_bucket.a
  id = "bucket-a"
}
import {
  to = module.m.aws_s3_bucket.main
  id = var.id
}
import {
  to = aws_iam_role.r
  identity = { name = "r" }
}
`,
		"generated.tf": `
resource "aws_s3_bucket" "a" {}
resource "aws_iam_role" "r" {}
data "aws_caller_identity" "me" {}
`,
		"x_override.tf": `resource "aws_s3_bucket" "a" {}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	blocks, err := importBlocks(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []tfrun.ImportBlock{{To: "aws_s3_bucket.a", ID: "bucket-a"}, {To: "module.m.aws_s3_bucket.main"}, {To: "aws_iam_role.r", ID: "name=r"}}
	if !slices.Equal(blocks, want) {
		t.Errorf("import blocks: got %+v, want %+v", blocks, want)
	}
	resources, err := resourceAddresses(dir)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(resources)
	if want := []string{"aws_iam_role.r", "aws_s3_bucket.a"}; !slices.Equal(resources, want) {
		t.Errorf("resources: got %v, want %v", resources, want)
	}
	if len(resources) != 2 {
		t.Errorf("an override or a data source was counted: %v", resources)
	}
}

// Without the option, or with a runner that can't run a snapshot's
// commands, the plans are the plain ones.
func TestCachedRunnerNeedsTheOption(t *testing.T) {
	t.Parallel()
	var plain tfrun.Terraform
	if c := cachedRunner(plain, plain, t.TempDir(), Options{}); c != nil {
		t.Error("a cache without the option")
	}
	opts := Options{ImportOnce: &ImportOnce{NewRunner: func(string) (tfrun.Terraform, error) { return nil, nil }}}
	if c := cachedRunner(plain, &fakeTerraform{}, t.TempDir(), opts); c != nil {
		t.Error("a cache for a runner that can't apply")
	}
	if (*tfrun.Cached)(nil).CachedPlans() != 0 {
		t.Error("a nil cache has plans")
	}
}

func TestDropBlocks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	main := "resource \"aws_s3_bucket\" \"a\" {\n  bucket = \"a\"\n}\n\nimport {\n  to = aws_s3_bucket.a\n  id = \"a\"\n}\n\nmoved {\n  from = aws_s3_bucket.old\n  to   = aws_s3_bucket.a\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dropBlocks(dir, "import", "moved"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "resource \"aws_s3_bucket\" \"a\" {\n  bucket = \"a\"\n}"; strings.TrimSpace(string(got)) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// withheldRoot is a root with what Terraform generates for SSM parameters: the
// secret value is withheld, and the provider requires one of three
// arguments, so it doesn't validate until the secrets step reads the value
// from a variable. The database's password is withheld too, which nothing
// requires.
const withheldRoot = `resource "aws_ssm_parameter" "token" {
  name  = "token"
  type  = "SecureString"
  value = null # sensitive
}

resource "aws_ssm_parameter" "plain" {
  name  = "plain"
  type  = "String"
  value = "x"
}

resource "aws_db_instance" "db" {
  identifier = "db"
  password   = null # sensitive
}
`

var withheldSchemas = &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
	"aws_ssm_parameter": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
		"value":    {AttributeType: cty.String, Optional: true, Sensitive: true},
		"value_wo": {AttributeType: cty.String, Optional: true, Sensitive: true, WriteOnly: true},
	}}},
	"aws_db_instance": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
		"password": {AttributeType: cty.String, Optional: true, Sensitive: true},
	}}},
}}}}

// noValueError is the error of the SSM parameter that has no value, at
// line of generated.tf.
func noValueError(line int) tfjson.Diagnostic {
	return tfjson.Diagnostic{
		Severity: tfjson.DiagnosticSeverityError,
		Summary:  "Invalid combination of arguments",
		Detail:   `"value": one of insecure_value,value,value_wo must be specified`,
		Range:    &tfjson.Range{Filename: rootfile.GeneratedFileName, Start: tfjson.Pos{Line: line}, End: tfjson.Pos{Line: line}},
	}
}

func writeRoot(t *testing.T, dir, generated string) {
	t.Helper()
	files := map[string]string{
		rootfile.GeneratedFileName: generated,
		rootfile.ImportsFileName: `import {
  to = aws_ssm_parameter.token
  id = "token"
}

import {
  to = aws_ssm_parameter.plain
  id = "plain"
}

import {
  to = aws_db_instance.db
  id = "db"
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The private copy a snapshot is imported in takes a placeholder for the
// argument its validation says is missing, and for no other.
func TestFillWithheld(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeRoot(t, dir, withheldRoot)
	tf := &fakeTerraform{dir: dir, schemas: withheldSchemas, validations: []*tfjson.ValidateOutput{{
		Valid:       false,
		Diagnostics: []tfjson.Diagnostic{noValueError(1)},
	}}}
	if err := fillWithheld(context.Background(), tf, dir); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, dir, rootfile.GeneratedFileName)
	if want := `value = "` + secret.PlaceholderValue + `"`; !strings.Contains(got, want) {
		t.Errorf("want %q for the parameter that has no value:\n%s", want, got)
	}
	if n := strings.Count(got, secret.PlaceholderValue); n != 1 {
		t.Errorf("%d placeholders, want 1 (the database's password is not missing):\n%s", n, got)
	}
	if !strings.Contains(got, "password   = null # sensitive") {
		t.Errorf("the password was changed:\n%s", got)
	}
}

func TestFillWithheldLeavesWhatIsNotMissing(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		generated   string
		validations []*tfjson.ValidateOutput
		calls       string
	}{
		"nothing withheld": {
			generated: "resource \"aws_ssm_parameter\" \"plain\" {\n  name  = \"plain\"\n  value = \"x\"\n}\n",
			calls:     "",
		},
		"a valid copy": {
			generated: withheldRoot,
			calls:     "validate",
		},
		"another error": {
			generated: withheldRoot,
			validations: []*tfjson.ValidateOutput{{Diagnostics: []tfjson.Diagnostic{
				{Severity: tfjson.DiagnosticSeverityError, Summary: "Unsupported argument", Detail: `An argument named "nope" is not expected here.`,
					Range: &tfjson.Range{Filename: rootfile.GeneratedFileName, Start: tfjson.Pos{Line: 3}, End: tfjson.Pos{Line: 3}}},
			}}},
			calls: "validate,schema",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeRoot(t, dir, tc.generated)
			tf := &fakeTerraform{dir: dir, schemas: withheldSchemas, validations: tc.validations}
			if err := fillWithheld(context.Background(), tf, dir); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, dir, rootfile.GeneratedFileName); got != tc.generated {
				t.Errorf("the file changed:\n%s", got)
			}
			if got := strings.Join(tf.calls, ","); got != tc.calls {
				t.Errorf("calls: got %q, want %q", got, tc.calls)
			}
		})
	}
}

// A write-only attribute is never given a placeholder: it is never stored,
// so unset is what an import produces.
func TestFillWithheldSkipsWriteOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	generated := "resource \"aws_ssm_parameter\" \"token\" {\n  name     = \"token\"\n  value_wo = null # sensitive\n}\n"
	writeRoot(t, dir, generated)
	tf := &fakeTerraform{dir: dir, schemas: withheldSchemas, validations: []*tfjson.ValidateOutput{{Diagnostics: []tfjson.Diagnostic{noValueError(1)}}}}
	if err := fillWithheld(context.Background(), tf, dir); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dir, rootfile.GeneratedFileName); got != generated {
		t.Errorf("the write-only attribute was filled:\n%s", got)
	}
}

// stateFake is the runner of the copy a snapshot is built in: the fake
// terraform, that applies.
type stateFake struct {
	*fakeTerraform
	applies int
}

func (f *stateFake) Apply(context.Context, ...tfexec.ApplyOption) error {
	f.applies++
	return nil
}

func (f *stateFake) StateMv(context.Context, string, string, ...tfexec.StateMvCmdOption) error {
	return nil
}

func (f *stateFake) StateRm(context.Context, string, ...tfexec.StateRmCmdOption) error {
	return nil
}

// A root that holds an SSM parameter with its value withheld gets a
// snapshot: the copy that is imported has a placeholder where the value
// is, the root's own files never do, and the placeholder is gone with the
// copy.
func TestSnapshotOfRootWithWithheldSecret(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // snapshots go to the system's temporary directory
	dir := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRoot(t, dir, withheldRoot)

	var built *stateFake
	var planned string
	opts := Options{ImportOnce: &ImportOnce{NewRunner: func(copied string) (tfrun.Terraform, error) {
		fake := &fakeTerraform{dir: copied, schemas: withheldSchemas, plans: []fakePlan{{summary: tfrun.ChangeSummary{Import: 3}}}, validations: []*tfjson.ValidateOutput{{Diagnostics: []tfjson.Diagnostic{noValueError(1)}}}}
		fake.onPlan = func() { planned = readFile(t, copied, rootfile.GeneratedFileName) }
		built = &stateFake{fakeTerraform: fake}
		return built, nil
	}}}
	g := &generation{dir: dir, opts: opts, r: run.Generating(opts), counting: &tfrun.Counting{}}

	snapshot, err := g.snapshotOf(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Remove()
	if snapshot.Len() != 3 || built.applies != 1 {
		t.Errorf("snapshot of %d resources, %d applies", snapshot.Len(), built.applies)
	}
	if want := `value = "` + secret.PlaceholderValue + `"`; !strings.Contains(planned, want) {
		t.Errorf("the copy was planned without a placeholder:\n%s", planned)
	}
	if got := readFile(t, dir, rootfile.GeneratedFileName); got != withheldRoot {
		t.Errorf("the root's own file changed:\n%s", got)
	}
	assertNotWritten(t, filepath.Dir(dir), secret.PlaceholderValue)
}
