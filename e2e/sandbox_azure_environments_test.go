// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && sandbox && (!minimal || azure)

package e2e

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// azureSlot is one sandbox subscription, as its bootstrap
// (testdata/sandbox/azure-bootstrap) sets it up: the estate's resource
// group and the client IDs of its two identities.
type azureSlot struct {
	subscriptionID, resourceGroup     string
	fixtureClientID, readonlyClientID string
}

// TestSandboxAzureEnvironments creates testdata/sandbox/azure-environments,
// a network security group of dev in the first sandbox subscription and
// one of prd in the second, each as its subscription's fixture identity,
// and takes them through what a team with an environment in each
// subscription does:
//
//  1. The first merge imports both subscriptions at once, with the
//     configuration file's environments mapping them to dev and prod, as
//     the first subscription's read-only identity, which the second
//     subscription's bootstrap lets read it too: one run signs in once,
//     and separate runs never merge. The merged root, azure/<resource
//     group>, plans as each environment with no changes, as that
//     environment's own read-only identity, and each applies it into its
//     workspace.
//  2. From then on each environment works as its own read-only identity
//     alone. prd gets an api group: prod's --incremental import of the
//     second subscription adds it into the merged root as prod's, merging
//     dev again from its kept root, unplanned; each environment's
//     workspace plan changes nothing, prod importing the group alone.
//  3. infraharvest verify --environment passes for each environment.
//  4. prd's owner tag changes out of band: drift --environment=prod
//     reports it, kept, and drift --environment=dev finds nothing.
//
// It destroys the estate when it is done. The sandbox-azure workflow runs
// it when asked to (estate=environments); see e2e/README.md.
func TestSandboxAzureEnvironments(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	s := newAzureSandbox(ctx, t)
	slots := map[string]azureSlot{
		"dev": {
			subscriptionID: s.subscriptionID, resourceGroup: s.resourceGroup,
			fixtureClientID: s.fixtureClientID, readonlyClientID: s.readonlyClientID,
		},
		"prod": {
			subscriptionID:   os.Getenv("SANDBOX_AZURE_SECOND_SUBSCRIPTION_ID"),
			resourceGroup:    envOr("SANDBOX_AZURE_SECOND_RESOURCE_GROUP", sandboxAzureResourceGroup),
			fixtureClientID:  os.Getenv("SANDBOX_AZURE_SECOND_FIXTURE_CLIENT_ID"),
			readonlyClientID: os.Getenv("SANDBOX_AZURE_SECOND_READONLY_CLIENT_ID"),
		},
	}
	dev, prod := slots["dev"], slots["prod"]
	if prod.subscriptionID == "" || prod.fixtureClientID == "" || prod.readonlyClientID == "" {
		skipUnconfigured(t, "SANDBOX_AZURE_SECOND_SUBSCRIPTION_ID, SANDBOX_AZURE_SECOND_FIXTURE_CLIENT_ID and SANDBOX_AZURE_SECOND_READONLY_CLIENT_ID name the second sandbox subscription; see the sandbox-azure workflow")
	}
	if strings.EqualFold(dev.subscriptionID, prod.subscriptionID) {
		t.Fatal("the two sandbox subscriptions are the same: each environment needs one of its own")
	}
	if strings.EqualFold(dev.readonlyClientID, prod.readonlyClientID) || strings.EqualFold(prod.fixtureClientID, prod.readonlyClientID) {
		t.Fatal("the second subscription's read-only identity is the first's, or its fixture's: each environment must run as its own read-only identity")
	}
	// The merged root's directory is the layout without the subscription:
	// the environments' roots merge only under one resource group name.
	if dev.resourceGroup != prod.resourceGroup {
		t.Fatalf("the estate's resource groups are %s and %s: the environments' roots merge only under one name", dev.resourceGroup, prod.resourceGroup)
	}

	estate := seedSandbox(ctx, t, "azure-environments", s.fixtureDir, s.execPath, s.plugins, nil,
		"suffix="+s.engineName, "resource_group="+dev.resourceGroup,
		"dev_subscription_id="+dev.subscriptionID, "dev_client_id="+dev.fixtureClientID,
		"prod_subscription_id="+prod.subscriptionID, "prod_client_id="+prod.fixtureClientID)
	estate.apply(ctx, t)

	config := writeConfig(t, fmt.Sprintf(`version: 1
environments:
  dev:
    azure: { subscriptions: [%q] }
  prod:
    azure: { subscriptions: [%q] }
    tokens: [prd]
`, dev.subscriptionID, prod.subscriptionID))
	// signIn has what runs next sign in as slot's read-only identity, in
	// its subscription.
	signIn := func(slot azureSlot) {
		t.Setenv("ARM_CLIENT_ID", slot.readonlyClientID)
		t.Setenv("ARM_SUBSCRIPTION_ID", slot.subscriptionID)
	}
	credentials := func(env string) { signIn(slots[env]) }
	// importInto imports the subscriptions into s.out, with the flags more
	// adds, and checks that nothing was denied.
	importInto := func(what string, subscriptions []string, more ...string) report.Report {
		t.Helper()
		logged := &syncBuffer{}
		logs := log.Writer()
		log.SetOutput(io.MultiWriter(logs, logged))
		defer log.SetOutput(logs)
		root := cli.NewCmdRoot()
		root.SetArgs(append([]string{
			"import", "azure",
			"--config=" + config,
			"--engine=" + s.engineName,
			"--terraform-path=" + s.execPath,
			"--subscriptions=" + strings.Join(subscriptions, ","),
			"--resource-group=" + dev.resourceGroup,
			"--services=network_security_group",
			"--out=" + s.out,
			"--all",
		}, more...))
		if err := root.ExecuteContext(ctx); importer.ExitCode(err) != report.ExitOK {
			t.Fatalf("%s: infraharvest import azure: %v", what, err)
		}
		coverage := readCoverage(t, s.out)
		checkNoAuthorizationErrors(t, logged.String(), coverage, azureAuthorizationErrors)
		checkCoverage(t, what, coverage, nil)
		return coverage
	}
	path := "azure/" + dev.resourceGroup
	dir := filepath.Join(s.out, filepath.FromSlash(path))
	envs := []string{"dev", "prod"}

	// 1. The first merge, as the first subscription's read-only identity,
	// which reads both.
	signIn(dev)
	coverage := importInto("the first merge", []string{dev.subscriptionID, prod.subscriptionID})
	checkMerged(t, s.out, coverage, path, envs...)
	imported := checkMergedPlans(ctx, t, dir, s.execPath, s.plugins, credentials, envs...)
	for _, env := range envs {
		if imported[env]["azurerm_network_security_group"] != 1 {
			t.Errorf("%s imports %v, want its web group", env, imported[env])
		}
	}
	if files := configuration(t, dir); strings.Contains(files, `"team-a"`) || strings.Contains(files, `"team-b"`) {
		t.Errorf("the merged root's code sets an environment's owner tag:\n%s", files)
	}
	// The apply only imports, which reading allows.
	applyMergedWorkspaces(ctx, t, dir, s.execPath, s.plugins, credentials, envs...)

	// 2. prod's incremental import, of its own subscription, as its own
	// read-only identity.
	estate.apply(ctx, t, tfexec.Var("prod_api=true"))
	credentials("prod")
	coverage = importInto("prod's --incremental", []string{prod.subscriptionID}, "--incremental")
	merge := checkMerged(t, s.out, coverage, path, envs...)
	if strings.Join(merge.Unplanned, ",") != "dev" {
		t.Errorf("prod's --incremental: unplanned %v, want dev, merged from its kept root", merge.Unplanned)
	}
	api := s.engineName + "-prd-api"
	if !slices.ContainsFunc(merge.Residue, func(r report.MergeResidue) bool {
		return r.Env == "prod" && strings.Contains(r.MergedAddress, "api")
	}) || !strings.Contains(readFile(t, filepath.Join(dir, "vars", "prod", "imports.tfvars")), api) {
		t.Errorf("prod's --incremental didn't add prd's api group as prod's: residue %+v", merge.Residue)
	}
	if strings.Contains(readFile(t, filepath.Join(dir, "vars", "dev", "imports.tfvars")), api) {
		t.Error("dev imports prd's api group")
	}
	for _, env := range envs {
		credentials(env)
		imports := checkWorkspacePlan(ctx, t, dir, s.execPath, s.plugins, env)
		switch {
		case env == "dev" && len(imports) != 0:
			t.Errorf("dev's workspace plan imports %v, which its state has", imports)
		case env == "prod" && (len(imports) != 1 || !strings.Contains(imports[0], "api")):
			t.Errorf("prod's workspace plan imports %v, want its api group alone", imports)
		}
	}
	credentials("prod")
	applyWorkspace(ctx, t, dir, s.execPath, s.plugins, "prod")

	// 3. verify, as each environment.
	for _, env := range envs {
		credentials(env)
		checkVerified(ctx, t, s.engineName, s.execPath, s.out, path, env)
	}

	// 4. drift, as each environment, once prd's owner tag changes.
	estate.apply(ctx, t, tfexec.Var("prod_api=true"), tfexec.Var("prod_owner=team-c"))
	drift := driftRun{ctx: ctx, engineName: s.engineName, execPath: s.execPath, out: s.out}
	credentials("prod")
	d := drift.mustExit(t, report.ExitDrift, "--detailed-exitcode", "--environment=prod")
	if len(d.Roots) != 1 || d.Roots[0].Path != path || d.Roots[0].Environment != "prod" || d.Roots[0].Resources == 0 {
		t.Errorf("drift as prod: roots %+v", d.Roots)
	}
	owner := driftChange(t, "drift as prod", d, "azurerm_network_security_group", "tags.owner")
	checkDriftChange(t, "drift as prod", owner, "team-b", "team-c", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	if owner.Environment != "prod" {
		t.Errorf("drift as prod: the change is of %q", owner.Environment)
	}
	credentials("dev")
	if d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode", "--environment=dev"); len(d.Changes) != 0 {
		t.Errorf("drift as dev:\n%s", driftChanges(d))
	}
}
