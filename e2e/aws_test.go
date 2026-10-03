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

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/cmd"
	"github.com/IgnatG/infraharvest/engine"
)

// awsServices are the infraharvest services that import what
// testdata/aws creates.
var awsServices = []string{
	"dynamodb", "ecr", "iam", "igw", "kinesis", "kms", "logs", "route53",
	"route_table", "secretsmanager", "sg", "sns", "sqs", "subnet", "vpc",
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

	seeded := seed(ctx, t, execPath, filepath.Join(cache, "plugins"))

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

	imported := map[string]int{}
	for _, dir := range generatedDirs(t, out) {
		for typ, n := range checkNoChanges(ctx, t, dir, execPath, filepath.Join(cache, "plugins")) {
			imported[typ] += n
		}
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

// seed applies testdata/aws and returns how many resources of each type it
// created.
func seed(ctx context.Context, t *testing.T, execPath, pluginCache string) map[string]int {
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
	created := map[string]int{}
	for _, r := range state.Values.RootModule.Resources {
		if r.Mode == "managed" {
			created[r.Type]++
		}
	}
	return created
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

// checkNoChanges plans dir and reports every resource that is not an import
// without changes. It returns how many resources of each type it imports.
func checkNoChanges(ctx context.Context, t *testing.T, dir, execPath, pluginCache string) map[string]int {
	t.Helper()
	tf, err := engine.NewTerraform(dir, execPath, pluginCache)
	if err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(t.TempDir(), "e2e.tfplan")
	if _, err := tf.Plan(ctx, tfexec.Out(planFile)); err != nil {
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

// stateOnlyArguments are arguments the AWS provider keeps only in state and
// uses when it deletes a resource. They are null after an import, so the
// first apply records their defaults without calling AWS.
var stateOnlyArguments = map[string][]string{
	"aws_secretsmanager_secret": {"force_overwrite_replica_secret", "recovery_window_in_days"},
}

// stateOnly reports whether every changed attribute is a state-only argument
// of resourceType.
func stateOnly(resourceType string, changed map[string]string) bool {
	for name := range changed {
		if !slices.Contains(stateOnlyArguments[resourceType], name) {
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
