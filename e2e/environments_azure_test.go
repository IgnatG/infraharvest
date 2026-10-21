// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || azure)

package e2e

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// azureEnvironmentSubscriptions are the subscriptions
// testdata/azure-environments creates its resources in, by environment.
var azureEnvironmentSubscriptions = map[string]string{
	"dev":  "00000000-0000-0000-0000-000000000001",
	"prod": "00000000-0000-0000-0000-000000000003",
}

// azureEnvironmentsConfig maps the subscriptions of
// testdata/azure-environments to dev and prod.
const azureEnvironmentsConfig = `version: 1
environments:
  dev:
    azure: { subscriptions: ["00000000-0000-0000-0000-000000000001"] }
  prod:
    azure: { subscriptions: ["00000000-0000-0000-0000-000000000003"] }
    tokens: [prd]
`

// azureEnvironmentsRoot is the merged root of testdata/azure-environments,
// relative to the output directory.
const azureEnvironmentsRoot = "azure/infraharvest-e2e-envs"

// TestAzureEnvironments imports testdata/azure-environments, a resource
// group of dev's subscription and one of prod's, each with a network
// security group whose owner tag differs, which the configuration file
// maps to environments by subscription: the roots of the two environments
// merge into one root they share, with the subscription a variable of
// each environment, which plans as each environment with no changes.
// floci-az keeps no resource group tags, and has no Resource Graph to read
// tags with, so environments are told apart by subscription only.
//
// The merged root then goes through the chain TestAWSEnvironments takes.
// Each environment applies it into its workspace. From a fresh checkout
// of the output, without .infraharvest and with the import blocks
// deleted, --incremental with --managed-state on the workspaces adds the
// api group prod gets since into the merged root, as prod's, and nothing
// else; each environment plans in its workspace with no changes, prod
// importing its new group alone. Once dev's owner tag changes in the
// cloud, drift as dev, with dev's subscription, reports it and, accepted,
// writes it into dev's var file alone; drift as prod finds nothing, and
// each environment plans no change. Once dev gets an api group too,
// --regenerate moves prod's, which no count gates now, with a moved
// block, and, applied, each environment plans no change; generated again
// with nothing new, the merged root keeps every address.
//
// Not covered here: a backend, as floci-az has no storage, so the
// environments apply into workspaces; what is no longer in the cloud, as
// no Azure type counts as listed in full (see TestAzureIncremental); and
// a handover to another root with removed blocks, which
// TestAzureRegenerateMoves covers for roots of their own: a resource's
// name is in its ID on Azure, and the environments here are told apart
// by subscription alone, so nothing can change roots but by a change of
// the configuration file.
func TestAzureEnvironments(t *testing.T) {
	host := os.Getenv(azureMetadataHostEnv)
	if host == "" {
		skipUnconfigured(t, azureMetadataHostEnv+" is not set; start floci-az in e2e/compose.yaml")
	}
	if !isLoopback("https://" + host) {
		t.Fatalf("%s=%s: the test creates resources, so it only runs against a local emulator", azureMetadataHostEnv, host)
	}
	isolateAzureConfig(t, host)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	fixture := seedApplied(ctx, t, "azure-environments", execPath, plugins)
	config := writeConfig(t, azureEnvironmentsConfig)

	importInto := func(out string, more ...string) {
		t.Helper()
		root := cli.NewCmdRoot()
		root.SetArgs(append([]string{
			"import", "azure",
			"--config=" + config,
			"--engine=" + engineName,
			"--terraform-path=" + execPath,
			"--subscriptions=" + azureEnvironmentSubscriptions["dev"] + "," + azureEnvironmentSubscriptions["prod"],
			"--resource-group=infraharvest-e2e-envs",
			"--services=network_security_group,resource_group",
			"--out=" + out,
			"--all",
			// The tags, which only Resource Graph reads, fail (see
			// azureEmulatorFailure).
			"--allow-partial",
		}, more...))
		if err := root.ExecuteContext(ctx); importer.ExitCode(err) != report.ExitOK && importer.ExitCode(err) != report.ExitPartial {
			t.Fatalf("infraharvest import azure %v: %v", more, err)
		}
	}
	// Each environment plans, applies and checks drift with its own
	// subscription.
	credentials := func(env string) { t.Setenv("ARM_SUBSCRIPTION_ID", azureEnvironmentSubscriptions[env]) }

	out := t.TempDir()
	importInto(out)
	coverage := coverageOf(t, out)
	checkCoverage(t, "the import", coverage, azureEmulatorFailure)
	checkMerged(t, out, coverage, azureEnvironmentsRoot, "dev", "prod")
	dir := filepath.Join(out, filepath.FromSlash(azureEnvironmentsRoot))
	imported := checkMergedPlans(ctx, t, dir, execPath, plugins, credentials, "dev", "prod")
	for _, env := range []string{"dev", "prod"} {
		if imported[env]["azurerm_network_security_group"] != 1 || imported[env]["azurerm_resource_group"] != 1 {
			t.Errorf("%s imports %v, want its resource group and network security group", env, imported[env])
		}
	}

	// Each environment applies the merged root into its workspace, as the
	// README says for a root without a backend: floci-az has no storage
	// for one.
	applyMergedWorkspaces(ctx, t, dir, execPath, plugins, credentials, "dev", "prod")
	for _, env := range []string{"dev", "prod"} {
		credentials(env)
		if imports := checkWorkspacePlan(ctx, t, dir, execPath, plugins, env); len(imports) != 0 {
			t.Errorf("%s's workspace plan, applied, imports %v", env, imports)
		}
	}

	// A fresh checkout of the output has no .infraharvest, which its
	// .gitignore leaves out, and, applied, a team deletes imports.tf and
	// each environment's imports.tfvars. prod gets an api group:
	// --incremental, with --managed-state reading each environment's
	// workspace state, tells the merged root from its files and adds the
	// group into it as prod's, and nothing else.
	had := resourceAddresses(t, dir)
	for _, path := range []string{
		filepath.Join(out, ".infraharvest"),
		filepath.Join(dir, "imports.tf"),
		filepath.Join(dir, "vars", "dev", "imports.tfvars"),
		filepath.Join(dir, "vars", "prod", "imports.tfvars"),
	} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	vars := []tfexec.ApplyOption{tfexec.Var("prod_api=true")}
	fixture.apply(ctx, t, vars...)
	importInto(out, "--incremental", "--managed-state="+filepath.Join(dir, "terraform.tfstate.d"))
	coverage = coverageOf(t, out)
	checkCoverage(t, "--incremental from a fresh checkout", coverage, azureEmulatorFailure)
	merge := checkMerged(t, out, coverage, azureEnvironmentsRoot, "dev", "prod")
	if len(merge.Unplanned) != 0 {
		t.Errorf("--incremental from a fresh checkout: unplanned %v", merge.Unplanned)
	}
	var added []string
	now := resourceAddresses(t, dir)
	for address := range now {
		if !had[address] {
			added = append(added, address)
		}
	}
	for address := range had {
		if !now[address] {
			t.Errorf("--incremental from a fresh checkout: %s is gone from the merged root", address)
		}
	}
	if len(added) != 1 || !strings.Contains(added[0], "api") {
		t.Errorf("--incremental from a fresh checkout added %v, want prod's api group alone", added)
	}
	if !slices.ContainsFunc(merge.Residue, func(r report.MergeResidue) bool {
		return r.Env == "prod" && strings.Contains(r.MergedAddress, "api")
	}) {
		t.Errorf("--incremental from a fresh checkout: prod's api group isn't prod's: %+v", merge.Residue)
	}
	if prod := readFile(t, filepath.Join(dir, "vars", "prod", "imports.tfvars")); !strings.Contains(prod, "shop-prd-api") {
		t.Errorf("prod doesn't import its api group:\n%s", prod)
	}
	if dev := readFile(t, filepath.Join(dir, "vars", "dev", "imports.tfvars")); strings.Contains(dev, "shop-prd-api") {
		t.Errorf("dev imports prod's api group:\n%s", dev)
	}
	// Planned in its workspace, each environment changes nothing, prod
	// importing its new group alone; then each applies it.
	for _, env := range []string{"dev", "prod"} {
		credentials(env)
		imports := checkWorkspacePlan(ctx, t, dir, execPath, plugins, env)
		switch {
		case env == "dev" && len(imports) != 0:
			t.Errorf("dev's workspace plan imports %v, which its state has", imports)
		case env == "prod" && (len(imports) != 1 || !strings.Contains(imports[0], "api")):
			t.Errorf("prod's workspace plan imports %v, want its api group alone", imports)
		}
	}
	// Planned with no state, as the gate does, each environment imports
	// everything it has and changes nothing.
	selectWorkspace(ctx, t, dir, execPath, plugins, "default")
	imported = checkMergedPlans(ctx, t, dir, execPath, plugins, credentials, "dev", "prod")
	if imported["dev"]["azurerm_network_security_group"] != 1 || imported["prod"]["azurerm_network_security_group"] != 2 {
		t.Errorf("--incremental from a fresh checkout: imported %v", imported)
	}
	for _, env := range []string{"dev", "prod"} {
		credentials(env)
		applyWorkspace(ctx, t, dir, execPath, plugins, env)
	}

	// dev's owner tag changes in the cloud: drift as dev finds it, and
	// accepts it into dev's var file alone, as the environments share the
	// code; drift as prod finds none.
	vars = append(vars, tfexec.Var("dev_owner=team-c"))
	fixture.apply(ctx, t, vars...)
	drift := driftRun{ctx: ctx, engineName: engineName, execPath: execPath, out: out}
	code := codeFiles(t, out)
	varFiles := map[string]string{}
	for name, content := range outputFiles(t, out) {
		if strings.HasSuffix(name, ".tfvars") {
			varFiles[name] = content
		}
	}
	credentials("dev")
	d := drift.mustExit(t, report.ExitDrift, "--detailed-exitcode", "--environment=dev")
	if len(d.Roots) != 1 || d.Roots[0].Path != azureEnvironmentsRoot || d.Roots[0].Environment != "dev" || d.Roots[0].Resources == 0 {
		t.Errorf("drift as dev: roots %+v", d.Roots)
	}
	owner := driftChange(t, "drift as dev", d, "azurerm_network_security_group", "tags.owner")
	checkDriftChange(t, "drift as dev", owner, "team-a", "team-c", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	if owner.Environment != "dev" {
		t.Errorf("drift as dev: the change is of %q", owner.Environment)
	}
	credentials("prod")
	if d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode", "--environment=prod"); len(d.Changes) != 0 {
		t.Errorf("drift as prod:\n%s", driftChanges(d))
	}
	credentials("dev")
	d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode", "--environment=dev", "--accept=all")
	accepted := driftChange(t, "drift as dev, accepted", d, "azurerm_network_security_group", "tags.owner")
	checkDriftChange(t, "drift as dev, accepted", accepted, "team-a", "team-c", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	if accepted.Source == nil || accepted.Source.File != "vars/dev/terraform.tfvars" || accepted.Source.Line == 0 {
		t.Errorf("drift as dev, accepted: the source is %+v, want dev's terraform.tfvars", accepted.Source)
	}
	checkCodeUnchanged(t, "drift as dev, accepted", code, out)
	devVars := azureEnvironmentsRoot + "/vars/dev/terraform.tfvars"
	after := outputFiles(t, out)
	for name, content := range varFiles {
		switch {
		case name != devVars && after[name] != content:
			t.Errorf("drift as dev, accepted: changed %s:\n%s", name, after[name])
		case name == devVars && !strings.Contains(after[name], `"team-c"`):
			t.Errorf("drift as dev, accepted: dev's terraform.tfvars doesn't set the owner team-c:\n%s", after[name])
		}
	}
	// Drift doesn't write the state: the code now absorbs the change.
	d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode", "--environment=dev")
	if c := driftChange(t, "drift as dev, written", d, "azurerm_network_security_group", "tags.owner"); c.Outcome != report.DriftAbsorbed {
		t.Errorf("drift as dev, written: the owner is %s (%s), want %s", c.Outcome, c.Reason, report.DriftAbsorbed)
	}
	for _, env := range []string{"dev", "prod"} {
		credentials(env)
		if imports := checkWorkspacePlan(ctx, t, dir, execPath, plugins, env); len(imports) != 0 {
			t.Errorf("%s's workspace plan, accepted, imports %v", env, imports)
		}
	}

	// dev gets an api group too, so the merged root no longer counts
	// prod's: --regenerate moves it to its new address with a moved
	// block. Applied as each environment, dev importing its group, every
	// plan has no changes.
	vars = append(vars, tfexec.Var("dev_api=true"))
	fixture.apply(ctx, t, vars...)
	importInto(out, "--regenerate")
	coverage = coverageOf(t, out)
	checkCoverage(t, "--regenerate", coverage, azureEmulatorFailure)
	checkMerged(t, out, coverage, azureEnvironmentsRoot, "dev", "prod")
	if !slices.ContainsFunc(coverage.Moved, func(m report.Move) bool {
		return m.FromRoot == azureEnvironmentsRoot && strings.HasSuffix(strings.ToLower(m.ID), "/shop-prd-api") && m.From != m.To
	}) {
		t.Errorf("--regenerate: prod's api group doesn't move, now that every environment has it: moved %+v", coverage.Moved)
	}
	if len(coverage.Departed) != 0 || len(coverage.IncompleteHandovers) != 0 {
		t.Errorf("--regenerate: departed %+v, incomplete handovers %+v, want none", coverage.Departed, coverage.IncompleteHandovers)
	}
	if moves := readFile(t, filepath.Join(dir, engine.MovesFileName)); !strings.Contains(moves, "moved {") {
		t.Errorf("the merged root's moves.tf has no moved block:\n%s", moves)
	}
	for _, env := range []string{"dev", "prod"} {
		credentials(env)
		applyWorkspace(ctx, t, dir, execPath, plugins, env)
	}
	for _, env := range []string{"dev", "prod"} {
		credentials(env)
		if imports := checkWorkspacePlan(ctx, t, dir, execPath, plugins, env); len(imports) != 0 {
			t.Errorf("%s's workspace plan, regenerated and applied, imports %v", env, imports)
		}
	}

	// Generated again with nothing new, the merged root keeps every
	// address: nothing moves, and each environment still plans no change.
	addresses := resourceAddresses(t, dir)
	importInto(out, "--regenerate")
	coverage = coverageOf(t, out)
	checkCoverage(t, "--regenerate with nothing new", coverage, azureEmulatorFailure)
	checkMerged(t, out, coverage, azureEnvironmentsRoot, "dev", "prod")
	if len(coverage.Moved) != 0 || len(coverage.Departed) != 0 {
		t.Errorf("--regenerate with nothing new: moved %+v, departed %+v, want none", coverage.Moved, coverage.Departed)
	}
	if now := resourceAddresses(t, dir); !maps.Equal(now, addresses) {
		t.Errorf("--regenerate with nothing new: the merged root's addresses are %v, want %v", slices.Sorted(maps.Keys(now)), slices.Sorted(maps.Keys(addresses)))
	}
	for _, env := range []string{"dev", "prod"} {
		credentials(env)
		if imports := checkWorkspacePlan(ctx, t, dir, execPath, plugins, env); len(imports) != 0 {
			t.Errorf("%s's workspace plan, regenerated with nothing new, imports %v", env, imports)
		}
	}
}
