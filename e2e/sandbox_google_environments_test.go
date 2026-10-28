// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && sandbox && (!minimal || google)

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
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
)

// googleSlot is one sandbox project, as its bootstrap
// (testdata/sandbox/google-bootstrap) sets it up: the credentials files
// of its fixture and read-only service accounts.
type googleSlot struct {
	project                                 string
	fixtureCredentials, readonlyCredentials string
}

// TestSandboxGoogleEnvironments creates
// testdata/sandbox/google-environments, a Pub/Sub topic of dev in the
// first sandbox project and one of prd in the second, each as its
// project's fixture service account, and takes them through what a team
// with an environment in each project does:
//
//  1. The first merge imports both projects' global resources at once,
//     with the configuration file's environments mapping them to dev and
//     prod, as the first project's read-only service account, which the
//     second project's bootstrap lets read it too: one run signs in once,
//     and separate runs never merge. A selection file of rules alone
//     keeps the import to the estate's topics, as the projects hold the
//     bootstraps' service accounts too. The merged root, google/global,
//     plans as each environment with no changes, as that environment's
//     own read-only service account, and each environment applies it into
//     its workspace, as its fixture service account (see checkApplied).
//  2. From then on each environment reads as its own read-only service
//     account alone. prd gets an alerts topic: prod's --incremental import
//     of the second project adds it into the merged root as prod's,
//     merging dev again from its kept root, unplanned; each environment's
//     workspace plan changes nothing, prod importing the topic alone.
//  3. infraharvest verify --environment passes for each environment.
//  4. prd's owner label changes out of band: drift --environment=prod
//     reports it, kept, and drift --environment=dev finds nothing.
//
// It destroys the estate when it is done. The sandbox-google workflow
// runs it when asked to (estate=environments); see e2e/README.md.
func TestSandboxGoogleEnvironments(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Minute)
	defer cancel()
	s := newGoogleSandbox(ctx, t)
	slots := map[string]googleSlot{
		"dev": {project: s.project, fixtureCredentials: s.fixtureCredentials, readonlyCredentials: s.readonlyCredentials},
		"prod": {
			project:             os.Getenv("SANDBOX_GOOGLE_SECOND_PROJECT"),
			fixtureCredentials:  os.Getenv("SANDBOX_GOOGLE_SECOND_FIXTURE_CREDENTIALS"),
			readonlyCredentials: os.Getenv("SANDBOX_GOOGLE_SECOND_READONLY_CREDENTIALS"),
		},
	}
	dev, prod := slots["dev"], slots["prod"]
	if prod.project == "" || prod.fixtureCredentials == "" || prod.readonlyCredentials == "" {
		skipUnconfigured(t, "SANDBOX_GOOGLE_SECOND_PROJECT, SANDBOX_GOOGLE_SECOND_FIXTURE_CREDENTIALS and SANDBOX_GOOGLE_SECOND_READONLY_CREDENTIALS name the second sandbox project; see the sandbox-google workflow")
	}
	if dev.project == prod.project {
		t.Fatal("the two sandbox projects are the same: each environment needs one of its own")
	}
	for _, f := range []string{prod.fixtureCredentials, prod.readonlyCredentials} {
		if _, err := os.Stat(f); err != nil {
			t.Fatal(err)
		}
	}
	if slices.Contains([]string{filepath.Clean(dev.readonlyCredentials), filepath.Clean(prod.fixtureCredentials)}, filepath.Clean(prod.readonlyCredentials)) {
		t.Fatal("the second project's read-only credentials are the first's, or its fixture's: each environment must run as its own read-only service account")
	}

	estate := seedSandbox(ctx, t, "google-environments", s.fixtureDir, s.execPath, s.plugins, nil,
		"suffix="+s.engineName,
		"dev_project="+dev.project, "dev_credentials="+dev.fixtureCredentials,
		"prod_project="+prod.project, "prod_credentials="+prod.fixtureCredentials)
	created := estate.apply(ctx, t)
	name := estateName(ctx, t, estate)

	config := writeConfig(t, fmt.Sprintf(`version: 1
environments:
  dev:
    google: { projects: [%q] }
  prod:
    google: { projects: [%q] }
    tokens: [prd]
`, dev.project, prod.project))
	// Rules alone: the estate's topics, of this engine's run, and nothing
	// else of the projects.
	selection := fmt.Sprintf(`version: 1
defaults:
  include: false
rules:
  - include: { type: google_pubsub_topic, name: "*%s-*" }
resources: []
`, name)
	readonly := func(env string) {
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", slots[env].readonlyCredentials)
	}
	// waitFor waits for Cloud Asset Inventory to find what the estate has
	// in env's project, as env's read-only service account.
	waitFor := func(env string, created []*tfjson.StateResource) {
		t.Helper()
		readonly(env)
		var in []*tfjson.StateResource
		for _, r := range created {
			if project, _ := r.AttributeValues["project"].(string); project == slots[env].project {
				in = append(in, r)
			}
		}
		waitForInventory(ctx, t, slots[env].project, in, name)
	}
	// importInto imports the projects into s.out, with the flags more adds,
	// and checks that nothing was denied.
	importInto := func(what string, projects []string, more ...string) report.Report {
		t.Helper()
		path := filepath.Join(t.TempDir(), "selection.yaml")
		if err := os.WriteFile(path, []byte(selection), 0o644); err != nil {
			t.Fatal(err)
		}
		logged := &syncBuffer{}
		logs := log.Writer()
		log.SetOutput(io.MultiWriter(logs, logged))
		defer log.SetOutput(logs)
		root := cli.NewCmdRoot()
		root.SetArgs(append([]string{
			"import", "google",
			"--config=" + config,
			"--engine=" + s.engineName,
			"--terraform-path=" + s.execPath,
			"--projects=" + strings.Join(projects, ","),
			"--regions=global",
			"--services=cloud_asset",
			// No module calls: each topic is a resource of the merged
			// root, whose labels the environments' var files set.
			"--modules=none",
			"--selection=" + path,
			"--out=" + s.out,
		}, more...))
		if err := root.ExecuteContext(ctx); importer.ExitCode(err) != report.ExitOK {
			t.Fatalf("%s: infraharvest import google: %v", what, err)
		}
		coverage := readCoverage(t, s.out)
		checkNoAuthorizationErrors(t, logged.String(), coverage, googleAuthorizationErrors)
		checkCoverage(t, what, coverage, nil)
		return coverage
	}
	// Applying an import updates a labelled resource's labels in place, to
	// the same values, which needs the right to update it: each
	// environment applies as its fixture service account (see
	// checkApplied).
	fixture := func(env string) {
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", slots[env].fixtureCredentials)
	}
	const path = "google/global"
	dir := filepath.Join(s.out, filepath.FromSlash(path))
	envs := []string{"dev", "prod"}

	// 1. The first merge, as the first project's read-only service
	// account, which reads both.
	waitFor("dev", created)
	waitFor("prod", created)
	readonly("dev")
	coverage := importInto("the first merge", []string{dev.project, prod.project})
	checkMerged(t, s.out, coverage, path, envs...)
	imported := checkMergedPlans(ctx, t, dir, s.execPath, s.plugins, readonly, envs...)
	for _, env := range envs {
		if imported[env]["google_pubsub_topic"] != 1 {
			t.Errorf("%s imports %v, want its orders topic", env, imported[env])
		}
	}
	if files := configuration(t, dir); strings.Contains(files, `"team-a"`) || strings.Contains(files, `"team-b"`) {
		t.Errorf("the merged root's code sets an environment's owner label:\n%s", files)
	}
	applyMergedWorkspaces(ctx, t, dir, s.execPath, s.plugins, fixture, envs...)
	for _, env := range envs {
		readonly(env)
		if imports := checkWorkspacePlan(ctx, t, dir, s.execPath, s.plugins, env); len(imports) != 0 {
			t.Errorf("%s's workspace plan, applied, imports %v", env, imports)
		}
	}

	// 2. prod's incremental import, of its own project, as its own
	// read-only service account.
	created = estate.apply(ctx, t, tfexec.Var("prod_alerts=true"))
	waitFor("prod", created)
	coverage = importInto("prod's --incremental", []string{prod.project}, "--incremental")
	merge := checkMerged(t, s.out, coverage, path, envs...)
	if strings.Join(merge.Unplanned, ",") != "dev" {
		t.Errorf("prod's --incremental: unplanned %v, want dev, merged from its kept root", merge.Unplanned)
	}
	alerts := name + "-prd-alerts"
	if !slices.ContainsFunc(merge.Residue, func(r report.MergeResidue) bool {
		return r.Env == "prod" && strings.Contains(r.MergedAddress, "alerts")
	}) || !strings.Contains(readFile(t, filepath.Join(dir, "vars", "prod", "imports.tfvars")), alerts) {
		t.Errorf("prod's --incremental didn't add prd's alerts topic as prod's: residue %+v", merge.Residue)
	}
	if strings.Contains(readFile(t, filepath.Join(dir, "vars", "dev", "imports.tfvars")), alerts) {
		t.Error("dev imports prd's alerts topic")
	}
	for _, env := range envs {
		readonly(env)
		imports := checkWorkspacePlan(ctx, t, dir, s.execPath, s.plugins, env)
		switch {
		case env == "dev" && len(imports) != 0:
			t.Errorf("dev's workspace plan imports %v, which its state has", imports)
		case env == "prod" && (len(imports) != 1 || !strings.Contains(imports[0], "alerts")):
			t.Errorf("prod's workspace plan imports %v, want its alerts topic alone", imports)
		}
	}
	fixture("prod")
	applyWorkspace(ctx, t, dir, s.execPath, s.plugins, "prod")

	// 3. verify, as each environment.
	for _, env := range envs {
		readonly(env)
		checkVerified(ctx, t, s.engineName, s.execPath, s.out, path, env)
	}

	// 4. drift, as each environment, once prd's owner label changes.
	estate.apply(ctx, t, tfexec.Var("prod_alerts=true"), tfexec.Var("prod_owner=team-c"))
	drift := driftRun{ctx: ctx, engineName: s.engineName, execPath: s.execPath, out: s.out}
	readonly("prod")
	d := drift.mustExit(t, report.ExitDrift, "--detailed-exitcode", "--environment=prod")
	if len(d.Roots) != 1 || d.Roots[0].Path != path || d.Roots[0].Environment != "prod" || d.Roots[0].Resources == 0 {
		t.Errorf("drift as prod: roots %+v", d.Roots)
	}
	owner := driftChange(t, "drift as prod", d, "google_pubsub_topic", "labels.owner")
	checkDriftChange(t, "drift as prod", owner, "team-b", "team-c", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	if owner.Environment != "prod" {
		t.Errorf("drift as prod: the change is of %q", owner.Environment)
	}
	readonly("dev")
	if d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode", "--environment=dev"); len(d.Changes) != 0 {
		t.Errorf("drift as dev:\n%s", driftChanges(d))
	}
}
