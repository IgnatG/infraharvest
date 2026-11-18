// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// fakePlanTerraform writes a Terraform that records what a plan runs
// with: its arguments, its environment, and, while it runs, the var files
// it is given (their paths, modes and contents).
func fakePlanTerraform(t *testing.T) (execPath, records string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake Terraform is a shell script")
	}
	base := t.TempDir()
	records = filepath.Join(base, "records")
	if err := os.MkdirAll(records, 0o755); err != nil {
		t.Fatal(err)
	}
	execPath = filepath.Join(base, "terraform")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then echo '{\"terraform_version\":\"1.9.0\"}'; exit 0; fi\n" +
		"r=" + records + "\n" +
		"printf '%s\\n' \"$@\" > $r/args\n" +
		"env > $r/env\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$a\" in\n" +
		"    -var-file=*)\n" +
		"      f=\"${a#-var-file=}\"\n" +
		"      echo \"$f\" >> $r/paths\n" +
		"      stat -c %a \"$f\" >> $r/modes\n" +
		"      cat \"$f\" >> $r/vars\n" +
		"      ;;\n" +
		"  esac\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(execPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return execPath, records
}

func writeRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readRecord(t *testing.T, records, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(records, name))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

const envVarsRoot = `
variable "password" {
  type      = string
  sensitive = true
}
variable "untyped" {}
variable "count_of" { type = number }
variable "zones" { type = list(string) }
variable "from_file" { type = string }
variable "from_json" { type = string }
variable "unset" { type = string }
`

func newPlanRunner(t *testing.T, root, execPath string) *Runner {
	t.Helper()
	tf, err := New(root, execPath, filepath.Join(t.TempDir(), "plugins"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tf
}

// The environment of a runner never has TF_VAR_ variables (tfexec refuses
// them), so a user's TF_VAR_ value reaches no plan unless the plan is given
// it another way.
func TestRunnerEnvironmentDropsTFVars(t *testing.T) {
	execPath, records := fakePlanTerraform(t)
	t.Setenv("TF_VAR_password", "hunter2")
	root := writeRoot(t, map[string]string{"main.tf": envVarsRoot})
	if _, err := newPlanRunner(t, root, execPath).PlanJSON(t.Context(), io.Discard); err != nil {
		t.Fatal(err)
	}
	if env := readRecord(t, records, "env"); strings.Contains(env, "TF_VAR_") {
		t.Errorf("the plan's environment has a TF_VAR_ variable:\n%s", env)
	}
	if vars := readRecord(t, records, "vars"); vars != "" {
		t.Errorf("a plain runner gave the plan a var file:\n%s", vars)
	}
}

// With WithEnvVars the plan reads the TF_VAR_ values of the variables the
// root declares, from a private var file that goes when the plan ends, and
// the value reaches neither the command line nor the environment.
func TestWithEnvVarsGivesPlansTheValues(t *testing.T) {
	execPath, records := fakePlanTerraform(t)
	t.Setenv("TF_VAR_password", "p\"a${s}s\nw")
	t.Setenv("TF_VAR_untyped", "plain")
	t.Setenv("TF_VAR_count_of", "3")
	t.Setenv("TF_VAR_zones", `["a", "b"]`)
	t.Setenv("TF_VAR_from_file", "from the environment")
	t.Setenv("TF_VAR_from_json", "from the environment")
	t.Setenv("TF_VAR_undeclared", "ignored")
	root := writeRoot(t, map[string]string{
		"main.tf":                envVarsRoot,
		"terraform.tfvars":       "from_file = \"from the file\"\n",
		"extra.auto.tfvars.json": `{"from_json": "from the json"}`,
	})
	if _, err := WithEnvVars(newPlanRunner(t, root, execPath), root).PlanJSON(t.Context(), io.Discard); err != nil {
		t.Fatal(err)
	}

	vars := readRecord(t, records, "vars")
	for _, want := range []string{
		`password = "p\"a$${s}s\nw"`,
		`untyped  = "plain"`,
		`count_of = 3`,
		`zones    = ["a", "b"]`,
	} {
		if !strings.Contains(vars, want) {
			t.Errorf("the var file lacks %q:\n%s", want, vars)
		}
	}
	for _, unwanted := range []string{"from_file", "from_json", "undeclared", "unset", "ignored"} {
		if strings.Contains(vars, unwanted) {
			t.Errorf("the var file has %q, which the environment must not set:\n%s", unwanted, vars)
		}
	}
	if modes := strings.TrimSpace(readRecord(t, records, "modes")); modes != "600" {
		t.Errorf("var file mode %q, want 600", modes)
	}
	if args := readRecord(t, records, "args"); strings.Contains(args, "plain") || strings.Contains(args, "-var=") {
		t.Errorf("a value reached the command line:\n%s", args)
	}
	if env := readRecord(t, records, "env"); strings.Contains(env, "TF_VAR_") {
		t.Errorf("the plan's environment has a TF_VAR_ variable:\n%s", env)
	}
	path := strings.TrimSpace(readRecord(t, records, "paths"))
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("the private directory of the var file is still there after the plan (%v)", err)
	}
}

// The var file goes before the plan's other var files, so that theirs win,
// as they do over the environment in Terraform.
func TestWithEnvVarsComesFirst(t *testing.T) {
	execPath, records := fakePlanTerraform(t)
	t.Setenv("TF_VAR_untyped", "plain")
	root := writeRoot(t, map[string]string{"main.tf": envVarsRoot})
	other := filepath.Join(root, "other.tfvars")
	if _, err := WithEnvVars(newPlanRunner(t, root, execPath), root).PlanJSON(t.Context(), io.Discard, tfexec.VarFile(other)); err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, a := range strings.Split(readRecord(t, records, "args"), "\n") {
		if strings.HasPrefix(a, "-var-file=") {
			files = append(files, strings.TrimPrefix(a, "-var-file="))
		}
	}
	if len(files) != 2 || files[1] != other || filepath.Base(files[0]) != "env.tfvars" {
		t.Errorf("var files %v, want the environment's first, then %s", files, other)
	}
}

// A value that isn't valid for a variable that isn't a string fails the
// plan without quoting the value, and Terraform doesn't run.
func TestWithEnvVarsRejectsInvalidValueWithoutQuotingIt(t *testing.T) {
	execPath, records := fakePlanTerraform(t)
	t.Setenv("TF_VAR_zones", "[not closed hunter2")
	root := writeRoot(t, map[string]string{"main.tf": envVarsRoot})
	_, err := WithEnvVars(newPlanRunner(t, root, execPath), root).PlanJSON(t.Context(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "TF_VAR_zones") || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error %v, want one naming TF_VAR_zones without its value", err)
	}
	if readRecord(t, records, "args") != "" {
		t.Error("Terraform ran")
	}
}

// With no TF_VAR_ values for declared variables, plans get no var file.
func TestWithEnvVarsWithoutValues(t *testing.T) {
	execPath, records := fakePlanTerraform(t)
	root := writeRoot(t, map[string]string{"main.tf": envVarsRoot})
	if _, err := WithEnvVars(newPlanRunner(t, root, execPath), root).PlanJSON(t.Context(), io.Discard); err != nil {
		t.Fatal(err)
	}
	if args := readRecord(t, records, "args"); strings.Contains(args, "-var-file") {
		t.Errorf("a var file with nothing in it:\n%s", args)
	}
}
