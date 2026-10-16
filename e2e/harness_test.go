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

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine"
)

// seed applies testdata/<fixture> and returns the resources it created.
func seed(ctx context.Context, t *testing.T, fixture, execPath, pluginCache string) []*tfjson.StateResource {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join("testdata", fixture, "main.tf")
	content, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	tf, err := engine.NewTerraform(dir, execPath, pluginCache, nil)
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

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// generatedDirs returns every root under out: the directories with an
// imports.tf.
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
		if !d.IsDir() && d.Name() == engine.ImportsFileName {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatalf("no %s under %s", engine.ImportsFileName, out)
	}
	return dirs
}

// configPaths returns the files of the root in dir that hold its
// configuration, in name order: its .tf files but imports.tf.
func configPaths(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	return slices.DeleteFunc(paths, func(p string) bool { return filepath.Base(p) == engine.ImportsFileName })
}

// configuration returns the configuration of the root in dir, its files
// joined.
func configuration(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	for _, path := range configPaths(t, dir) {
		b.WriteString(readFile(t, path))
	}
	return b.String()
}

// configBlocks returns the top-level blocks of the root in dir.
func configBlocks(t *testing.T, dir string) []*hclsyntax.Block {
	t.Helper()
	var blocks []*hclsyntax.Block
	for _, path := range configPaths(t, dir) {
		f, diags := hclsyntax.ParseConfig([]byte(readFile(t, path)), path, hcl.InitialPos)
		if diags.HasErrors() {
			t.Fatal(diags)
		}
		blocks = append(blocks, f.Body.(*hclsyntax.Body).Blocks...)
	}
	return blocks
}

// checkNoChanges plans dir with vars and reports every resource that is not
// an import without changes. It returns how many resources of each type it
// imports.
func checkNoChanges(ctx context.Context, t *testing.T, dir, execPath, pluginCache string, vars []tfexec.PlanOption) map[string]int {
	t.Helper()
	tf, err := engine.NewTerraform(dir, execPath, pluginCache, nil)
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
		// Modules read data sources, such as the caller's identity.
		if rc.Mode != tfjson.ManagedResourceMode {
			continue
		}
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

// stateOnlyArguments are, by provider, the arguments it keeps only in
// state, which the plan sets after an import (see the catalogue's
// state_only), by type and under catalogue.AllTypes for every type: the
// Google provider's labels and terraform_labels, for example.
var stateOnlyArguments = map[string]map[string][]string{
	"aws":    catalogue.MustBuiltin("aws").StateOnly(),
	"google": catalogue.MustBuiltin("google").StateOnly(),
}

// emulatorGaps are attributes Floci leaves out of its API responses where
// AWS returns them, so the plan sets the provider's default. Each one is a
// difference from AWS, not from infraharvest's output.
var emulatorGaps = map[string][]string{
	// DescribeAutoScalingGroups has no AvailabilityZoneDistribution or
	// CapacityReservationSpecification.
	"aws_autoscaling_group": {"availability_zone_distribution", "capacity_reservation_specification"},
	// DescribeServices has no deploymentConfiguration.
	"aws_ecs_service": {"deployment_maximum_percent", "deployment_minimum_healthy_percent"},
	// DescribeTargetGroupAttributes has no target_group_health.* keys.
	"aws_lb_target_group": {"target_group_health"},
	// Unverified: the plan marks these computed attributes unknown after an
	// import. Check on a real account (sandbox, P0-04) whether AWS does too.
	"aws_nat_gateway": {"regional_nat_gateway_address", "secondary_allocation_ids", "secondary_private_ip_addresses"},
}

// onEmulator is whether the test runs against an emulator, whose gaps
// (emulatorGaps) a plan may show; the sandbox test runs against AWS.
var onEmulator = true

// stateOnly reports whether every changed attribute is a state-only argument
// of resourceType, or, on an emulator, one the emulator doesn't return.
func stateOnly(resourceType string, changed map[string]string) bool {
	provider, _, _ := strings.Cut(resourceType, "_")
	arguments := append(slices.Clone(stateOnlyArguments[provider][catalogue.AllTypes]), stateOnlyArguments[provider][resourceType]...)
	for name := range changed {
		if !slices.Contains(arguments, name) && !(onEmulator && slices.Contains(emulatorGaps[resourceType], name)) {
			return false
		}
	}
	return true
}

// requiredEnv, when set, makes a test that has no emulator or sandbox to
// run against fail instead of skipping. The CI jobs that run the e2e and
// sandbox tests set it, so a broken setup can't pass as a run that tested
// nothing; a local run without it still skips.
const requiredEnv = "INFRAHARVEST_E2E_REQUIRED"

// skipUnconfigured skips the test for reason, which says what to set, or
// fails it when requiredEnv is set.
func skipUnconfigured(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv(requiredEnv) != "" {
		t.Fatalf("%s (%s is set, so the test must run)", reason, requiredEnv)
	}
	t.Skip(reason)
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
