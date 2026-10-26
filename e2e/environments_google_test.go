// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || google)

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
	"github.com/IgnatG/infraharvest/internal/providers/google"
	"github.com/IgnatG/infraharvest/internal/report"
)

// googleEnvironmentsConfig tells the environments of one project apart by
// their env label, as configuration files do for environments that share
// a project.
const googleEnvironmentsConfig = `version: 1
environments:
  dev:
    tags: { env: dev }
  prod:
    tags: { env: prd }
    tokens: [prd]
`

// googleEventsTopic is the name of the Pub/Sub topic dev gets when
// testdata/google-environments is applied with events=true.
const googleEventsTopic = "infraharvest-e2e-dev-events"

// TestGoogleEnvironments takes testdata/google-environments, a bucket of
// dev and one of prod in one project, which the configuration file's
// environments tell apart by label, through the whole chain on one merged
// root:
//
//  1. The import merges the roots of the two environments into one root
//     they share, google/global, which plans as each environment with no
//     changes; the owner label, which differs, is in each environment's
//     var file.
//  2. Each environment applies it into its workspace, as the README says
//     for a root without a backend.
//  3. From a fresh checkout of the output, without .infraharvest and with
//     imports.tf and each environment's imports.tfvars deleted, dev gets a
//     Pub/Sub topic: --incremental, with --managed-state reading the
//     workspaces' states, adds it into the merged root as dev's, and
//     nothing else. In its workspace, dev imports the topic alone and prod
//     nothing; with no state, each imports all it has.
//  4. The topic, applied into dev's workspace, is deleted out of band, as
//     prod gets a reports bucket: from a fresh checkout again, without
//     .infraharvest, --incremental adds the bucket into the
//     merged root as prod's, and reports the topic no longer in the cloud,
//     as dev's, at its address in the merged root, which keeps it, with
//     dev's import of it, and every other address. The topic is then
//     created again, as it was, and each environment applies the merged
//     root into its workspace.
//  5. Dev's bucket gets another owner label out of band: drift as dev
//     reports it, kept, and, accepted, writes it into dev's var file
//     alone; drift as prod finds nothing. As docs/drift.md says, the next
//     Google plan of a label accepted at a resource may still show it
//     added to the resource's labels, which changes only the state: each
//     environment's plan changes nothing else, applied it changes nothing,
//     and drift then has nothing left to revert.
//  6. --regenerate with nothing changed keeps every address, with no moved
//     or removed block, and each workspace plans with no changes. With the
//     topic's env label taken off, --regenerate hands it over to the
//     project's shared root with a removed block; applied in order, the
//     merged root as each environment, then the shared root, every plan
//     has no changes.
//
// Cloud Asset Inventory is stood in by assetSearch, a new one each time
// the fixture changes. It changes the estate the other tests import from
// the same floci-gcp, so CI runs it on a runner of its own.
func TestGoogleEnvironments(t *testing.T) {
	endpoint := os.Getenv(google.EndpointEnv)
	if endpoint == "" {
		skipUnconfigured(t, google.EndpointEnv+" is not set; start floci-gcp in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("%s=%s: the test creates resources, so it only runs against a local emulator", google.EndpointEnv, endpoint)
	}
	isolateGoogleConfig(t, endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	fixture := seedApplied(ctx, t, "google-environments", execPath, plugins)
	// The fixture's variables, as the test last applied them; each apply
	// gives the listers a stand-in of their own that finds what the
	// fixture has then.
	vars := map[string]string{}
	search := func() { t.Setenv(google.EndpointEnv, assetSearch(t, endpoint, fixture.resources(ctx, t)).URL) }
	apply := func(set ...string) {
		t.Helper()
		for _, kv := range set {
			k, v, _ := strings.Cut(kv, "=")
			vars[k] = v
		}
		var opts []tfexec.ApplyOption
		for _, k := range slices.Sorted(maps.Keys(vars)) {
			opts = append(opts, tfexec.Var(k+"="+vars[k]))
		}
		fixture.apply(ctx, t, opts...)
		search()
	}
	search()

	config := writeConfig(t, googleEnvironmentsConfig)
	out := t.TempDir()
	importInto := func(more ...string) report.Report {
		t.Helper()
		root := cli.NewCmdRoot()
		root.SetArgs(append([]string{
			"import", "google",
			"--config=" + config,
			"--engine=" + engineName,
			"--terraform-path=" + execPath,
			"--projects=" + googleProject,
			"--services=cloud_asset",
			"--regions=global",
			// No module calls: each bucket is a resource of the merged
			// root, whose labels the environments' var files set.
			"--modules=none",
			"--out=" + out,
			"--all",
		}, more...))
		if err := root.ExecuteContext(ctx); importer.ExitCode(err) != report.ExitOK {
			t.Fatalf("infraharvest import google %v: %v", more, err)
		}
		return coverageOf(t, out)
	}
	envs := []string{"dev", "prod"}
	dir := filepath.Join(out, "google", "global")
	varFile := func(env string) string { return filepath.Join(dir, "vars", env, "terraform.tfvars") }

	// 1. The environments merge into one root.
	coverage := importInto()
	checkCoverage(t, "the import", coverage, nil)
	checkMerged(t, out, coverage, "google/global", envs...)
	imported := checkMergedPlans(ctx, t, dir, execPath, plugins, nil, envs...)
	for _, env := range envs {
		if imported[env]["google_storage_bucket"] != 1 {
			t.Errorf("%s imports %v, want its bucket", env, imported[env])
		}
	}
	for env, owner := range map[string]string{"dev": "alice", "prod": "bob"} {
		if got := readFile(t, varFile(env)); !strings.Contains(got, `"`+owner+`"`) {
			t.Errorf("%s's terraform.tfvars doesn't set its bucket's owner label %q:\n%s", env, owner, got)
		}
	}
	if strings.Contains(configuration(t, dir), `"alice"`) || strings.Contains(configuration(t, dir), `"bob"`) {
		t.Errorf("the merged root's code sets an environment's owner label:\n%s", configuration(t, dir))
	}

	// 2. Each environment applies the merged root into its workspace.
	for _, env := range envs {
		tf, err := engine.NewTerraform(dir, execPath, plugins, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := tf.Init(ctx); err != nil {
			t.Fatalf("%s: terraform init: %v", env, err)
		}
		if err := tf.WorkspaceNew(ctx, env); err != nil {
			t.Fatalf("terraform workspace new %s: %v", env, err)
		}
		applyWorkspace(ctx, t, dir, execPath, plugins, env)
		if imports := checkWorkspacePlan(ctx, t, dir, execPath, plugins, env); len(imports) != 0 {
			t.Errorf("%s's workspace plan, applied, imports %v", env, imports)
		}
	}

	// 3. From a fresh checkout, --incremental adds dev's new topic, and
	// nothing else.
	had := resourceAddresses(t, dir)
	for _, path := range []string{
		filepath.Join(out, ".infraharvest"),
		filepath.Join(dir, engine.ImportsFileName),
		filepath.Join(dir, "vars", "dev", "imports.tfvars"),
		filepath.Join(dir, "vars", "prod", "imports.tfvars"),
	} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	apply("events=true")
	coverage = importInto("--incremental", "--managed-state="+filepath.Join(dir, "terraform.tfstate.d"))
	checkCoverage(t, "--incremental from a fresh checkout", coverage, nil)
	merge := checkMerged(t, out, coverage, "google/global", envs...)
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
	if len(added) != 1 || !strings.HasPrefix(added[0], "google_pubsub_topic.") {
		t.Errorf("--incremental from a fresh checkout added %v, want dev's topic alone", added)
	}
	if !slices.ContainsFunc(merge.Residue, func(r report.MergeResidue) bool {
		return r.Env == "dev" && strings.HasPrefix(r.MergedAddress, "google_pubsub_topic.")
	}) {
		t.Errorf("--incremental from a fresh checkout: dev's topic isn't dev's: %+v", merge.Residue)
	}
	if dev := readFile(t, filepath.Join(dir, "vars", "dev", "imports.tfvars")); !strings.Contains(dev, googleEventsTopic) {
		t.Errorf("dev doesn't import its topic:\n%s", dev)
	}
	if prod := readFile(t, filepath.Join(dir, "vars", "prod", "imports.tfvars")); strings.Contains(prod, googleEventsTopic) {
		t.Errorf("prod imports dev's topic:\n%s", prod)
	}
	for _, env := range envs {
		imports := checkWorkspacePlan(ctx, t, dir, execPath, plugins, env)
		switch {
		case env == "dev" && (len(imports) != 1 || !strings.HasPrefix(imports[0], "google_pubsub_topic.")):
			t.Errorf("dev's workspace plan imports %v, want its topic alone", imports)
		case env == "prod" && len(imports) != 0:
			t.Errorf("prod's workspace plan imports %v, which its state has", imports)
		}
	}
	// Planned with no state, as the gate does, each environment imports
	// everything it has and changes nothing.
	selectWorkspace(ctx, t, dir, execPath, plugins, "default")
	imported = checkMergedPlans(ctx, t, dir, execPath, plugins, nil, envs...)
	if imported["dev"]["google_storage_bucket"] != 1 || imported["dev"]["google_pubsub_topic"] != 1 || imported["prod"]["google_storage_bucket"] != 1 || imported["prod"]["google_pubsub_topic"] != 0 {
		t.Errorf("--incremental from a fresh checkout: imported %v", imported)
	}
	for _, env := range envs {
		applyWorkspace(ctx, t, dir, execPath, plugins, env)
	}

	// 4. Deleted out of band, the topic is reported no longer in the
	// cloud, as dev's, at its address in the merged root, as prod gets a
	// reports bucket, which the same run adds as prod's. Cloud Asset
	// Inventory lists every topic of the project, so the topic counts as
	// gone; buckets would too, but not in a module call (see
	// docs/usage.md).
	// Again from a fresh checkout, without .infraharvest: the topic comes
	// back into dev's root generated afresh from the merged root.
	if err := os.RemoveAll(filepath.Join(out, ".infraharvest")); err != nil {
		t.Fatal(err)
	}
	had = resourceAddresses(t, dir)
	apply("events=false", "reports=true")
	coverage = importInto("--incremental")
	checkCoverage(t, "--incremental after deleting dev's topic", coverage, nil)
	merge = checkMerged(t, out, coverage, "google/global", envs...)
	if merge.Unchanged {
		t.Errorf("--incremental with prod's new bucket: the merged root is unchanged")
	}
	added, now = nil, resourceAddresses(t, dir)
	for address := range now {
		if !had[address] {
			added = append(added, address)
		}
	}
	for address := range had {
		if !now[address] {
			t.Errorf("--incremental after deleting dev's topic: %s is gone from the merged root", address)
		}
	}
	if len(added) != 1 || !strings.HasPrefix(added[0], "google_storage_bucket.") {
		t.Errorf("--incremental after deleting dev's topic added %v, want prod's reports bucket alone", added)
	} else if !slices.ContainsFunc(merge.Residue, func(r report.MergeResidue) bool {
		return r.Env == "prod" && strings.HasPrefix(r.MergedAddress, added[0])
	}) {
		t.Errorf("--incremental after deleting dev's topic: prod's reports bucket isn't prod's: %+v", merge.Residue)
	}
	if prod := readFile(t, filepath.Join(dir, "vars", "prod", "imports.tfvars")); !strings.Contains(prod, "infraharvest-e2e-prd-reports") {
		t.Errorf("prod doesn't import its reports bucket:\n%s", prod)
	}
	// Dev's import of the topic stays, as plain --incremental leaves it.
	if dev := readFile(t, filepath.Join(dir, "vars", "dev", "imports.tfvars")); !strings.Contains(dev, googleEventsTopic) || strings.Contains(dev, "infraharvest-e2e-prd-reports") {
		t.Errorf("dev's imports after deleting its topic:\n%s", dev)
	}
	if len(coverage.NotInCloud) != 1 {
		t.Fatalf("--incremental after deleting dev's topic: no longer in the cloud %+v, want the topic", coverage.NotInCloud)
	}
	gone := coverage.NotInCloud[0]
	label, _, _ := strings.Cut(strings.TrimPrefix(gone.Address, "google_pubsub_topic."), "[")
	if gone.Path != "google/global" || gone.Environment != "dev" || !strings.HasSuffix(gone.ID, "/"+googleEventsTopic) || !strings.HasPrefix(gone.Address, "google_pubsub_topic.") || !strings.Contains(configuration(t, dir), `resource "google_pubsub_topic" "`+label+`"`) {
		t.Errorf("no longer in the cloud: got %+v, want dev's topic at its address in the merged root", gone)
	}
	if md := readFile(t, filepath.Join(out, report.Dir, "report.md")); !strings.Contains(md, "`"+gone.Address+"`") || !strings.Contains(md, "of dev") {
		t.Errorf("report.md doesn't list %s as dev's, no longer in the cloud:\n%s", gone.Address, md)
	}
	// The topic comes back as it was, which dev's workspace still has, and
	// prod imports its reports bucket into its workspace.
	apply("events=true")
	for _, env := range envs {
		applyWorkspace(ctx, t, dir, execPath, plugins, env)
	}

	// 5. Drift as each environment: dev's change, accepted, goes into
	// dev's var file alone.
	apply("dev_owner=carol")
	drift := driftRun{ctx: ctx, engineName: engineName, execPath: execPath, out: out}
	code := codeFiles(t, out)
	prodVars := readFile(t, varFile("prod"))
	d := drift.mustExit(t, report.ExitDrift, "--detailed-exitcode", "--environment=dev")
	if len(d.Roots) != 1 || d.Roots[0].Path != "google/global" || d.Roots[0].Environment != "dev" || d.Roots[0].Resources == 0 {
		t.Errorf("drift as dev: roots %+v", d.Roots)
	}
	owner := driftChange(t, "drift as dev", d, "google_storage_bucket", "labels.owner")
	checkDriftChange(t, "drift as dev", owner, "alice", "carol", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)
	if owner.Environment != "dev" {
		t.Errorf("drift as dev: the change is of %q", owner.Environment)
	}
	checkCodeUnchanged(t, "drift as dev", code, out)
	d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode", "--environment=dev", "--accept=all")
	owner = driftChange(t, "drift as dev, accepted", d, "google_storage_bucket", "labels.owner")
	checkDriftChange(t, "drift as dev, accepted", owner, "alice", "carol", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	if owner.Source == nil || owner.Source.File != "vars/dev/terraform.tfvars" || owner.Source.Line == 0 {
		t.Errorf("drift as dev, accepted: the source is %+v, want dev's terraform.tfvars", owner.Source)
	}
	checkCodeUnchanged(t, "drift as dev, accepted", code, out)
	if dev := readFile(t, varFile("dev")); !strings.Contains(dev, `"carol"`) || strings.Contains(dev, `"alice"`) {
		t.Errorf("drift as dev, accepted: dev's terraform.tfvars doesn't give the owner label \"carol\":\n%s", dev)
	}
	if got := readFile(t, varFile("prod")); got != prodVars {
		t.Errorf("drift as dev, accepted: prod's terraform.tfvars changed:\n%s", got)
	}
	// Prod's state may lag the cloud on what only the cloud sets, such as
	// a bucket's updated time, which drift reports as absorbed.
	d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode", "--environment=prod")
	for _, c := range d.Changes {
		if c.Outcome != report.DriftAbsorbed {
			t.Errorf("drift as prod:\n%s", driftChanges(d))
			break
		}
	}
	// Each environment plans no change but, with Google, to the labels
	// kept only in state (see docs/drift.md); applied, that changes only
	// the state, the plan is clean, and drift finds nothing to revert.
	for _, env := range envs {
		checkPlanClean(t, env+": the accepted code", planWorkspace(ctx, t, dir, execPath, plugins, env))
		applyWorkspace(ctx, t, dir, execPath, plugins, env)
		checkPlanClean(t, env+": the accepted code, applied", planWorkspace(ctx, t, dir, execPath, plugins, env))
		d = drift.mustExit(t, report.ExitOK, "--detailed-exitcode", "--environment="+env)
		for _, c := range d.Changes {
			if c.Outcome != report.DriftAbsorbed {
				t.Errorf("drift as %s, applied: %s %s is %s (%s), want %s", env, c.Address, c.Attribute, c.Outcome, c.Reason, report.DriftAbsorbed)
			}
		}
	}

	// 6. --regenerate with nothing changed keeps every address.
	had = resourceAddresses(t, dir)
	coverage = importInto("--regenerate")
	checkCoverage(t, "--regenerate", coverage, nil)
	checkMerged(t, out, coverage, "google/global", envs...)
	if len(coverage.Moved) != 0 || len(coverage.Departed) != 0 {
		t.Errorf("--regenerate with nothing changed: moved %+v, departed %+v", coverage.Moved, coverage.Departed)
	}
	if _, err := os.Stat(filepath.Join(dir, engine.MovesFileName)); err == nil {
		t.Errorf("--regenerate with nothing changed wrote %s:\n%s", engine.MovesFileName, readFile(t, filepath.Join(dir, engine.MovesFileName)))
	}
	if now := resourceAddresses(t, dir); !maps.Equal(now, had) {
		t.Errorf("--regenerate with nothing changed: the merged root's resources are %v, were %v", slices.Sorted(maps.Keys(now)), slices.Sorted(maps.Keys(had)))
	}
	for _, env := range envs {
		if imports := checkWorkspacePlan(ctx, t, dir, execPath, plugins, env); len(imports) != 0 {
			t.Errorf("--regenerate with nothing changed: %s's workspace plan imports %v, which its state has", env, imports)
		}
	}

	// Without its env label, the topic is in no environment: --regenerate
	// hands it over from the merged root to the project's shared root.
	apply("events_env=")
	coverage = importInto("--regenerate")
	checkCoverage(t, "--regenerate, the topic unlabelled", coverage, nil)
	checkMerged(t, out, coverage, "google/global", envs...)
	i := slices.IndexFunc(coverage.Departed, func(m report.Move) bool { return strings.HasSuffix(m.ID, "/"+googleEventsTopic) })
	if i < 0 {
		t.Fatalf("--regenerate: dev's topic isn't handed over: departed %+v", coverage.Departed)
	}
	departed := coverage.Departed[i]
	if departed.FromRoot != "google/global" || !strings.HasPrefix(departed.ToRoot, "envs/shared/") || departed.To == "" {
		t.Fatalf("--regenerate: the topic goes from %s (%s) to %s (%s), want from google/global to the shared root", departed.FromRoot, departed.From, departed.ToRoot, departed.To)
	}
	if len(coverage.IncompleteHandovers) != 0 {
		t.Errorf("--regenerate: incomplete handovers %+v", coverage.IncompleteHandovers)
	}
	moves := readFile(t, filepath.Join(dir, engine.MovesFileName))
	for _, want := range []string{"removed {", "destroy = false"} {
		if !strings.Contains(moves, want) {
			t.Errorf("the merged root's %s has no %q:\n%s", engine.MovesFileName, want, moves)
		}
	}
	for address := range resourceAddresses(t, dir) {
		if strings.HasPrefix(address, "google_pubsub_topic.") {
			t.Errorf("--regenerate: the merged root still has %s", address)
		}
	}
	// The merged root first, as it hands the topic over, then the shared
	// root, which imports it.
	for _, env := range envs {
		applyWorkspace(ctx, t, dir, execPath, plugins, env)
	}
	sharedDir := filepath.Join(out, filepath.FromSlash(departed.ToRoot))
	applyRoot(ctx, t, sharedDir, execPath, plugins, nil)
	for _, env := range envs {
		checkPlanClean(t, "the merged root as "+env+", handed over", planWorkspace(ctx, t, dir, execPath, plugins, env))
	}
	checkPlanClean(t, departed.ToRoot, planRoot(ctx, t, sharedDir, execPath, plugins))
}
