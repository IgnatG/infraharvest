// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && sandbox && (!minimal || google)

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"google.golang.org/api/cloudasset/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/providers/google"
	"github.com/IgnatG/infraharvest/internal/report"
)

// sandboxGoogleRegion is where testdata/sandbox/google creates its
// regional resources, and the region the test imports besides global,
// unless SANDBOX_GOOGLE_REGION names another.
const sandboxGoogleRegion = "europe-west2"

// sandboxNamePrefix is what the name of every resource of every sandbox
// estate starts with: ih-sbx-<engine>-<random suffix>.
const sandboxNamePrefix = "ih-sbx-"

// How long the test waits for Cloud Asset Inventory to find what the
// estate created, and how often it searches meanwhile: the search lags
// behind changes by a few minutes.
const (
	inventoryWait = 10 * time.Minute
	inventoryPoll = time.Minute
)

// sandboxGoogleReadonlyRoles are the roles permissions/google grants: the
// import must find the read-only service account's grant of each in the
// project's IAM policy.
var sandboxGoogleReadonlyRoles = []string{"roles/cloudasset.viewer", "roles/iam.securityReviewer", "roles/viewer"}

// googleAuthorizationErrors are what Google Cloud, the client libraries
// and the google provider say when the caller's roles don't allow a call:
// the read-only template grants too little.
var googleAuthorizationErrors = []string{
	"PERMISSION_DENIED", "Error 403", "403 Forbidden", "status 403",
	"does not have", "denied on resource",
}

// sandboxGoogleLagging are the asset types whose search results lag
// behind changes by hours rather than minutes: Cloud Asset Inventory syncs
// functions every 7 hours
// (https://cloud.google.com/asset-inventory/docs/supported-asset-types).
// The test doesn't wait for them, and checkGoogleFunction looks for the
// estate's function apart.
var sandboxGoogleLagging = []string{"cloudfunctions.googleapis.com/Function"}

// sandboxGoogleUnlisted are the types the estate creates that no lister
// lists: a bucket's objects, such as the function's source, aren't
// resources Cloud Asset Inventory finds, and the query service lists the
// network firewall policy (see sandboxGoogleQueried).
var sandboxGoogleUnlisted = []string{"google_storage_bucket_object", sandboxGoogleQueried}

// sandboxGoogleQueried is the type of the estate no lister lists that the
// query service lists, through Terraform's list resources (see
// checkGoogleQuery).
const sandboxGoogleQueried = "google_compute_network_firewall_policy"

// TestSandboxGoogle creates testdata/sandbox/google in a real project as
// the fixture service account (the credentials file
// SANDBOX_GOOGLE_FIXTURE_CREDENTIALS names), waits for Cloud Asset
// Inventory to find it, imports the project's global resources and its
// region as the read-only service account
// (SANDBOX_GOOGLE_READONLY_CREDENTIALS), which has what permissions/google
// grants, as a user would, and checks that nothing was denied, that the
// engine plans every generated resource as an import with no changes, and
// that the import found what the estate created, by ID, with its labels,
// the project and the read-only account's grants, and that the default
// selection leaves out what Google created for the estate's function.
// Applied, the global root must take a label added out of band through
// infraharvest drift (see checkGoogleDrift). It destroys the estate when
// it is done. The sandbox-google workflow runs it, after approval; see
// e2e/README.md.
func TestSandboxGoogle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Minute)
	defer cancel()
	s := newGoogleSandbox(ctx, t)

	estate := seedSandbox(ctx, t, "google", s.fixtureDir, s.execPath, s.plugins,
		map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": s.fixtureCredentials},
		"suffix="+s.engineName, "project="+s.project, "region="+s.region)
	created := estate.apply(ctx, t)
	name := estateName(ctx, t, estate)

	// From here on the test's own Google calls, the search, the import and
	// the plans, are the read-only service account's.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", s.readonlyCredentials)
	waitForInventory(ctx, t, s.project, created, name)

	logged := s.importGoogle(ctx, t)
	coverage := readCoverage(t, s.out)
	checkNoAuthorizationErrors(t, logged, coverage, googleAuthorizationErrors)
	imported := s.checkGoogleImport(ctx, t, coverage, created)
	s.checkGoogleFound(t, coverage, created)
	checkGoogleLabels(ctx, t, s, coverage, created)
	checkGoogleFunction(t, coverage, created)
	checkGoogleDisks(t, coverage, created)
	s.checkGoogleQuery(ctx, t, created)
	if imported["google_project"] == 0 {
		t.Errorf("the import doesn't import the project %s", s.project)
	}
	s.checkGoogleDrift(ctx, t, estate)
}

// checkGoogleQuery takes the estate's network firewall policy through the
// query service as the read-only service account, in the project's
// global resources (see checkQueryRoundTrip). Call it once the test has
// signed in as that account.
func (s *googleSandbox) checkGoogleQuery(ctx context.Context, t *testing.T, created []*tfjson.StateResource) {
	t.Helper()
	name := ""
	for _, r := range created {
		if r.Type == sandboxGoogleQueried {
			name, _ = r.AttributeValues["name"].(string)
		}
	}
	if name == "" {
		t.Fatalf("the estate has no %s", sandboxGoogleQueried)
	}
	checkQueryRoundTrip(ctx, t, queryRoundTrip{
		command:  "google",
		scope:    []string{"--projects=" + s.project, "--regions=global"},
		typ:      sandboxGoogleQueried,
		identity: map[string]string{"project": s.project, "name": name},
		file:     "network.tf",
		plugins:  s.plugins,
	})
}

// googleSandbox is what the Google Cloud sandbox test runs with: the
// project and region, the credentials files of the two service accounts
// and the read-only one's email, the engine, and the directories that
// outlive the test, so that the workflow can destroy what a failed run
// left and upload the import.
type googleSandbox struct {
	project, region                         string
	fixtureCredentials, readonlyCredentials string
	readonlyAccount                         string
	engineName, execPath                    string
	fixtureDir, plugins, out                string
}

// newGoogleSandbox returns what the test runs with, from the environment
// the sandbox-google workflow sets: the project, the region, and a
// credentials file for each service account, which the workflow's sign-in
// writes (a workload identity federation configuration, with no key), and
// the read-only one's email. It skips the test if no project is named.
func newGoogleSandbox(ctx context.Context, t *testing.T) *googleSandbox {
	t.Helper()
	s := &googleSandbox{
		project:             os.Getenv("SANDBOX_GOOGLE_PROJECT"),
		region:              envOr("SANDBOX_GOOGLE_REGION", sandboxGoogleRegion),
		fixtureCredentials:  os.Getenv("SANDBOX_GOOGLE_FIXTURE_CREDENTIALS"),
		readonlyCredentials: os.Getenv("SANDBOX_GOOGLE_READONLY_CREDENTIALS"),
		readonlyAccount:     os.Getenv("SANDBOX_GOOGLE_READONLY_SERVICE_ACCOUNT"),
	}
	if s.project == "" || s.fixtureCredentials == "" || s.readonlyCredentials == "" || s.readonlyAccount == "" {
		skipUnconfigured(t, "SANDBOX_GOOGLE_PROJECT, SANDBOX_GOOGLE_FIXTURE_CREDENTIALS, SANDBOX_GOOGLE_READONLY_CREDENTIALS and SANDBOX_GOOGLE_READONLY_SERVICE_ACCOUNT name a sandbox project; see the sandbox-google workflow")
	}
	if os.Getenv(google.EndpointEnv) != "" {
		t.Fatalf("%s is set: the sandbox test runs against Google Cloud, not an emulator", google.EndpointEnv)
	}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "GOOGLE_") && strings.HasSuffix(k, "_CUSTOM_ENDPOINT") {
			t.Fatalf("%s is set: the sandbox test runs against Google Cloud, not an emulator", k)
		}
	}
	// What the google provider would sign in with before the credentials
	// file the test names, as either service account.
	for _, k := range []string{"GOOGLE_OAUTH_ACCESS_TOKEN", "GOOGLE_CREDENTIALS", "GOOGLE_CLOUD_KEYFILE_JSON", "GCLOUD_KEYFILE_JSON", "GOOGLE_IMPERSONATE_SERVICE_ACCOUNT"} {
		if os.Getenv(k) != "" {
			t.Fatalf("%s is set: each step must sign in with its service account's credentials file alone", k)
		}
	}
	if filepath.Clean(s.fixtureCredentials) == filepath.Clean(s.readonlyCredentials) {
		t.Fatal("the fixture and read-only credentials are the same: the import must run with what permissions/google grants alone")
	}
	for _, f := range []string{s.fixtureCredentials, s.readonlyCredentials} {
		if _, err := os.Stat(f); err != nil {
			t.Fatal(err)
		}
	}
	onEmulator = false
	s.fixtureDir = envOr("SANDBOX_FIXTURE_DIR", t.TempDir())
	s.plugins = envOr("SANDBOX_PLUGIN_CACHE", t.TempDir())
	s.out = envOr("SANDBOX_OUTPUT_DIR", t.TempDir())
	var binary tfrun.Binary
	s.engineName, binary = e2eEngine(t)
	var err error
	if s.execPath, err = binary.Find(ctx, "", filepath.Join(s.plugins, binary.Name)); err != nil {
		t.Fatal(err)
	}
	return s
}

// estateName returns what the names of the estate's resources start with,
// its output name.
func estateName(ctx context.Context, t *testing.T, estate *sandboxEstate) string {
	t.Helper()
	outputs, err := estate.tf.Output(ctx)
	if err != nil {
		t.Fatalf("seed: output: %v", err)
	}
	var name string
	if err := json.Unmarshal(outputs["name"].Value, &name); err != nil || !strings.HasPrefix(name, sandboxNamePrefix) {
		t.Fatalf("seed: output name %q: %v", name, err)
	}
	return name
}

// waitForInventory waits, up to inventoryWait, for Cloud Asset Inventory
// to find every resource of the estate it lists, and to no longer find
// those of earlier runs, which the estate's destroy deleted: the search
// lags behind changes, and the import lists through it. It searches once
// a minute, for the asset types of the estate alone. A search that is
// denied fails the test at once. Resources of earlier runs it still finds
// at the end are logged, and the test goes on: one a failed run left is
// there to import. It waits too for what Google creates for the estate's
// function (see googleManagedFor), which the default selection must leave
// out, and for the instance's boot disk (see googleBootDisks), but not
// for the function itself, whose search results lag by hours (see
// sandboxGoogleLagging).
func waitForInventory(ctx context.Context, t *testing.T, project string, created []*tfjson.StateResource, name string) {
	t.Helper()
	cat := catalogue.MustBuiltin("google")
	var (
		want       []*tfjson.StateResource
		assetTypes []string
	)
	for _, r := range created {
		if e, ok := cat.Entry(r.Type); ok && e.Inventory != nil && !slices.Contains(sandboxGoogleLagging, e.Inventory.Type) {
			want = append(want, r)
			if !slices.Contains(assetTypes, e.Inventory.Type) {
				assetTypes = append(assetTypes, e.Inventory.Type)
			}
		}
	}
	if len(want) == 0 {
		t.Fatal("the estate creates nothing Cloud Asset Inventory lists")
	}
	estateTypes := slices.Clone(assetTypes)
	managed := append(googleManagedFor(created), googleBootDisks(created)...)
	for _, m := range managed {
		if !slices.Contains(assetTypes, m.assetType) {
			assetTypes = append(assetTypes, m.assetType)
		}
	}
	slices.Sort(assetTypes)
	svc, err := cloudasset.NewService(ctx, option.WithScopes(cloudasset.CloudPlatformScope))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for {
		missing, earlier, err := searchEstate(ctx, svc, cat, project, assetTypes, estateTypes, want, managed, name)
		var apiErr *googleapi.Error
		switch {
		case errors.As(err, &apiErr) && apiErr.Code == http.StatusForbidden:
			t.Fatalf("Cloud Asset Inventory denies the read-only service account its search: %v", err)
		case err == nil && len(missing) == 0 && len(earlier) == 0:
			t.Logf("Cloud Asset Inventory finds the estate after %v", time.Since(start).Round(time.Second))
			return
		case time.Since(start) >= inventoryWait:
			if err != nil {
				t.Fatalf("searching Cloud Asset Inventory: %v", err)
			}
			if len(missing) > 0 {
				t.Fatalf("after %v, Cloud Asset Inventory doesn't find %v", inventoryWait, missing)
			}
			t.Logf("after %v, Cloud Asset Inventory still finds resources of earlier runs, which the import lists too: %v", inventoryWait, earlier)
			return
		}
		t.Logf("waiting for Cloud Asset Inventory: not found yet %v; of earlier runs %v; error %v", missing, earlier, err)
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(inventoryPoll):
		}
	}
}

// searchEstate searches the project once for resources of assetTypes, and
// returns the addresses of those of want and what of managed it doesn't
// find yet, and the names of those of earlier runs it finds, of the
// estate's asset types: named after the sandbox's prefix but not after
// this run's name.
func searchEstate(ctx context.Context, svc *cloudasset.Service, cat *catalogue.Catalogue, project string, assetTypes, estateTypes []string, want []*tfjson.StateResource, managed []googleManaged, name string) (missing, earlier []string, err error) {
	found := map[string]bool{}
	foundManaged := make([]bool, len(managed))
	err = svc.V1.SearchAllResources("projects/"+project).
		AssetTypes(assetTypes...).
		PageSize(500).
		ReadMask("name,assetType,project,displayName,state,additionalAttributes").
		Pages(ctx, func(page *cloudasset.SearchAllResourcesResponse) error {
			for _, r := range page.Results {
				if strings.Contains(strings.ToUpper(r.State), "DELET") {
					continue
				}
				keys := assetKeys(r, project)
				for _, k := range keys {
					found[k] = true
				}
				id := strings.TrimPrefix(keys[0], r.AssetType+" id:")
				for i, m := range managed {
					if m.assetType == r.AssetType && m.match(id) {
						foundManaged[i] = true
					}
				}
				named := strings.Join(keys, " ")
				if strings.Contains(named, sandboxNamePrefix) && !strings.Contains(named, name) && slices.Contains(estateTypes, r.AssetType) {
					earlier = append(earlier, r.Name)
				}
			}
			return nil
		})
	if err != nil {
		return nil, nil, err
	}
	for _, r := range want {
		e, _ := cat.Entry(r.Type)
		if !slices.ContainsFunc(stateKeys(r, e.Inventory.Type), func(k string) bool { return found[k] }) {
			missing = append(missing, r.Address)
		}
	}
	for i, m := range managed {
		if !foundManaged[i] {
			missing = append(missing, m.what)
		}
	}
	return missing, earlier, nil
}

// assetKeys are the ways the estate's state can name a resource the search
// found, each after its asset type: its ID, the full name without the
// API's host and with the project's ID for its number, as the import
// records it; its display name, such as a managed zone's name, whose full
// name ends with its numeric ID; and its email, for a service account.
func assetKeys(r *cloudasset.ResourceSearchResult, project string) []string {
	path := strings.TrimPrefix(r.Name, "//")
	_, path, _ = strings.Cut(path, "/")
	if number := strings.TrimPrefix(r.Project, "projects/"); number != "" && number != project {
		if rest, ok := strings.CutPrefix(path, "projects/"+number+"/"); ok {
			path = "projects/" + project + "/" + rest
		}
	}
	keys := []string{r.AssetType + " id:" + path}
	if r.DisplayName != "" {
		keys = append(keys, r.AssetType+" name:"+r.DisplayName)
	}
	var additional map[string]any
	if len(r.AdditionalAttributes) > 0 && json.Unmarshal(r.AdditionalAttributes, &additional) == nil {
		if email, _ := additional["email"].(string); email != "" {
			keys = append(keys, r.AssetType+" email:"+email)
		}
	}
	return keys
}

// stateKeys are the keys (see assetKeys) of a resource the estate created,
// of assetType: its ID, its name and its email, those it has.
func stateKeys(r *tfjson.StateResource, assetType string) []string {
	var keys []string
	for _, k := range []string{"id", "name", "email"} {
		if v, _ := r.AttributeValues[k].(string); v != "" {
			keys = append(keys, assetType+" "+k+":"+v)
		}
	}
	return keys
}

// importGoogle imports the project's global resources and the region,
// every service, as the read-only service account, into the output
// directory, and returns what the import logged.
func (s *googleSandbox) importGoogle(ctx context.Context, t *testing.T) string {
	t.Helper()
	logged := &syncBuffer{}
	logs := log.Writer()
	log.SetOutput(io.MultiWriter(logs, logged))
	defer log.SetOutput(logs)
	root := cli.NewCmdRoot()
	// The output's own layout isn't named here: checkGoogleImport finds
	// the roots.
	root.SetArgs([]string{
		"import", "google",
		"--engine=" + s.engineName,
		"--terraform-path=" + s.execPath,
		"--projects=" + s.project,
		"--regions=global," + s.region,
		"--services=*",
		"--out=" + s.out,
		"--all",
		"--allow-partial",
	})
	if err := root.ExecuteContext(ctx); importer.ExitCode(err) != report.ExitOK && importer.ExitCode(err) != report.ExitPartial {
		t.Fatalf("infraharvest import google: %v", err)
	}
	return logged.String()
}

// checkGoogleImport checks the import in the output directory: nothing
// failed or was left out, every check passed, there is a root for global
// and one for the region, each plans as imports with no changes, and each
// google type the estate created is imported at least as many times as it
// was created. It returns how many resources of each type the roots
// import.
func (s *googleSandbox) checkGoogleImport(ctx context.Context, t *testing.T, coverage report.Report, created []*tfjson.StateResource) map[string]int {
	t.Helper()
	t.Logf("totals: %+v", coverage.Totals)
	for _, f := range coverage.Failures {
		t.Errorf("failure: %+v", f)
	}
	for _, d := range coverage.Directories {
		for _, l := range d.LeftOut {
			t.Errorf("%s: %s (%s) left out: %v", d.Path, l.Address, l.ID, l.Errors)
		}
		for _, c := range d.Checks {
			if !c.Passed {
				t.Errorf("%s: %s failed: %v", d.Path, c.Name, c.Details)
			}
		}
	}

	// A root for each region of the project:
	// <out>/<provider>/<project>/<region>.
	dirs := generatedDirs(t, s.out)
	regions := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		dir = filepath.Clean(dir)
		regions = append(regions, filepath.Base(dir))
		if filepath.Base(filepath.Dir(dir)) != s.project {
			t.Errorf("root %s, want one named after the project %s and a region", dir, s.project)
		}
		if rejected, err := os.ReadFile(filepath.Join(dir, rootfile.RejectedFileName)); err == nil {
			t.Errorf("%s: resources left out:\n%s", dir, rejected)
		}
	}
	slices.Sort(regions)
	want := []string{"global", s.region}
	slices.Sort(want)
	if !slices.Equal(regions, want) {
		t.Errorf("roots %v, for the regions %v; want one for each of %v", dirs, regions, want)
	}
	imported := map[string]int{}
	for _, dir := range dirs {
		for typ, n := range checkNoChanges(ctx, t, dir, s.execPath, s.plugins, nil) {
			imported[typ] += n
		}
	}
	t.Logf("imported: %v", imported)
	checkApplied(ctx, t, s.out, dirs, s.execPath, s.plugins, map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": s.fixtureCredentials}, func(string) []tfexec.PlanOption { return nil })

	cat := catalogue.MustBuiltin("google")
	seeded := map[string]int{}
	for _, r := range created {
		// Not random_string, which only names the estate's resources, nor
		// what the import can't find yet (see googleListedNow).
		if strings.HasPrefix(r.Type, "google_") && googleListedNow(cat, r.Type) {
			seeded[r.Type]++
		}
	}
	for _, typ := range sortedKeys(seeded) {
		if imported[typ] < seeded[typ] {
			t.Errorf("%s: created %d, imported %d", typ, seeded[typ], imported[typ])
		}
	}
	return imported
}

// checkGoogleFound checks that the import imports every resource the
// estate created that it can find (see googleListedNow), by ID, which
// the import IDs of the google provider's types are, and the read-only
// service account's grant of each role permissions/google grants, from
// the project's IAM policy.
func (s *googleSandbox) checkGoogleFound(t *testing.T, coverage report.Report, created []*tfjson.StateResource) {
	t.Helper()
	imported := map[string]bool{}
	for _, d := range coverage.Directories {
		for _, r := range d.Imported {
			imported[r.ID] = true
		}
	}
	cat := catalogue.MustBuiltin("google")
	for _, r := range created {
		if !strings.HasPrefix(r.Type, "google_") || !googleListedNow(cat, r.Type) {
			continue
		}
		if id, _ := r.AttributeValues["id"].(string); id == "" || !imported[id] {
			t.Errorf("the import doesn't import %s %q", r.Address, id)
		}
	}
	for _, role := range sandboxGoogleReadonlyRoles {
		// google_project_iam_member's import ID: project, role and member.
		if id := s.project + " " + role + " serviceAccount:" + s.readonlyAccount; !imported[id] {
			t.Errorf("the import doesn't import the read-only service account's grant of %s (%q)", role, id)
		}
	}
}

// checkGoogleLabels checks that the generated configuration sets the
// labels of each resource the estate labelled, wherever the output puts
// them: on the resource, in a module call's arguments, or in local.labels,
// which the provider's default_labels take. A root's plan has as each
// resource's terraform_labels the labels its configuration and the
// provider's default_labels set, so each label must be in those of the
// resource's import, in the root that imports it. The plans with no
// changes (checkGoogleImport) check they are the resource's.
func checkGoogleLabels(ctx context.Context, t *testing.T, s *googleSandbox, coverage report.Report, created []*tfjson.StateResource) {
	t.Helper()
	roots := map[string]string{}
	for _, d := range coverage.Directories {
		for _, r := range d.Imported {
			roots[r.ID] = filepath.Join(s.out, filepath.FromSlash(d.Path))
		}
	}
	planned := map[string]map[string]map[string]any{}
	labelled := 0
	for _, r := range created {
		labels, _ := r.AttributeValues["labels"].(map[string]any)
		id, _ := r.AttributeValues["id"].(string)
		root, ok := roots[id]
		if len(labels) == 0 || !ok {
			continue // checkGoogleFound reports what isn't imported
		}
		labelled++
		if _, ok := planned[root]; !ok {
			planned[root] = plannedLabels(ctx, t, root, s.execPath, s.plugins)
		}
		got := planned[root][id]
		for key, value := range labels {
			if got[key] != value {
				t.Errorf("%s: the configuration doesn't set %s's label %s = %q: the plan's terraform_labels are %v", root, r.Address, key, value, got)
			}
		}
	}
	if labelled == 0 {
		t.Error("the import imports none of the resources the estate labelled")
	}
}

// plannedLabels plans the root in dir, which checkNoChanges initialised,
// and returns by import ID the terraform_labels of each resource it
// imports: the labels of the resource's configuration and the provider's
// default_labels.
func plannedLabels(ctx context.Context, t *testing.T, dir, execPath, plugins string) map[string]map[string]any {
	t.Helper()
	tf, err := tfrun.New(dir, execPath, plugins, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(t.TempDir(), "labels.tfplan")
	if _, err := tf.Plan(ctx, tfexec.Out(planFile), tfexec.Lock(false)); err != nil {
		t.Errorf("%s: terraform plan: %v", dir, err)
		return nil
	}
	plan, err := tf.ShowPlanFile(ctx, planFile)
	if err != nil {
		t.Fatalf("%s: terraform show: %v", dir, err)
	}
	labels := map[string]map[string]any{}
	for _, rc := range plan.ResourceChanges {
		if rc.Mode != tfjson.ManagedResourceMode || rc.Change == nil || rc.Change.Importing == nil {
			continue
		}
		after, _ := rc.Change.After.(map[string]any)
		labels[rc.Change.Importing.ID], _ = after["terraform_labels"].(map[string]any)
	}
	return labels
}

// googleListedNow reports whether the import must find the resources of
// typ the estate created: not those no lister lists (see
// sandboxGoogleUnlisted), nor those whose search results lag by hours
// (see sandboxGoogleLagging), which checkGoogleFunction looks for.
func googleListedNow(cat *catalogue.Catalogue, typ string) bool {
	if slices.Contains(sandboxGoogleUnlisted, typ) {
		return false
	}
	e, ok := cat.Entry(typ)
	return !ok || e.Inventory == nil || !slices.Contains(sandboxGoogleLagging, e.Inventory.Type)
}

// googleManaged is a resource Google creates for one the estate created,
// which the default selection must leave out: what it is, for messages,
// its type and asset type, and whether an ID, as the import records it,
// is its.
type googleManaged struct {
	what, typ, assetType string
	match                func(id string) bool
}

// googleManagedFor returns what Google creates for each 2nd gen function
// the estate created: the Cloud Run service it runs on, which its
// service_config names, and the bucket Cloud Functions copies its source
// to, gcf-v2-sources-<project number>-<region>, which outlives it.
func googleManagedFor(created []*tfjson.StateResource) []googleManaged {
	var managed []googleManaged
	for _, r := range created {
		if r.Type != "google_cloudfunctions2_function" {
			continue
		}
		location, _ := r.AttributeValues["location"].(string)
		service, _ := r.AttributeValues["name"].(string)
		if configs, ok := r.AttributeValues["service_config"].([]any); ok && len(configs) == 1 {
			if config, ok := configs[0].(map[string]any); ok {
				if name, _ := config["service"].(string); name != "" {
					service = name[strings.LastIndex(name, "/")+1:]
				}
			}
		}
		managed = append(managed,
			googleManaged{
				what:      "the Cloud Run service " + service + " that " + r.Address + " runs on",
				typ:       "google_cloud_run_v2_service",
				assetType: "run.googleapis.com/Service",
				match: func(id string) bool {
					return strings.HasSuffix(id, "/locations/"+location+"/services/"+service)
				},
			},
			googleManaged{
				what:      "the gcf-v2-sources bucket in " + location + " that holds the source of " + r.Address,
				typ:       "google_storage_bucket",
				assetType: "storage.googleapis.com/Bucket",
				match: func(id string) bool {
					return strings.HasPrefix(id, "gcf-v2-sources-") && strings.HasSuffix(id, "-"+location)
				},
			},
		)
	}
	return managed
}

// checkGoogleFunction checks what the import makes of the estate's 2nd
// gen function: the default selection leaves out, each with a reason, the
// Cloud Run service it runs on and the bucket Cloud Functions keeps its
// source in (see googleManagedFor), and imports the function, which
// checkNoChanges planned. Cloud Asset Inventory syncs functions every 7
// hours, so the search may not list this run's function yet: then the
// test logs it, and fails only if the default selection left it out.
func checkGoogleFunction(t *testing.T, coverage report.Report, created []*tfjson.StateResource) {
	t.Helper()
	managed := googleManagedFor(created)
	if len(managed) == 0 {
		t.Fatal("the estate creates no 2nd gen function")
	}
	for _, m := range managed {
		i := slices.IndexFunc(coverage.Excluded, func(e report.Excluded) bool { return e.Type == m.typ && m.match(e.ID) })
		switch {
		case i < 0:
			t.Errorf("the import doesn't leave out %s", m.what)
		case coverage.Excluded[i].Reason == "":
			t.Errorf("the import leaves out %s (%s) with no reason", m.what, coverage.Excluded[i].ID)
		default:
			t.Logf("%s left out: %s", coverage.Excluded[i].ID, coverage.Excluded[i].Reason)
		}
	}
	imported := map[string]bool{}
	for _, d := range coverage.Directories {
		for _, r := range d.Imported {
			imported[r.ID] = true
		}
	}
	for _, r := range created {
		if r.Type != "google_cloudfunctions2_function" {
			continue
		}
		id, _ := r.AttributeValues["id"].(string)
		i := slices.IndexFunc(coverage.Excluded, func(e report.Excluded) bool { return e.Type == r.Type && e.ID == id })
		switch {
		case imported[id]:
			t.Logf("%s imported", id)
		case i >= 0:
			t.Errorf("the import leaves out %s, the estate's function, by default: %s", id, coverage.Excluded[i].Reason)
		default:
			t.Logf("the import doesn't find %s: Cloud Asset Inventory syncs functions every 7 hours, and hasn't listed it yet", id)
		}
	}
}

// googleBootDisks returns the boot disk of each instance the estate
// created, which Google creates with the instance and names after it: its
// boot_disk block's source, or, without one, the disk of the instance's
// name in its zone.
func googleBootDisks(created []*tfjson.StateResource) []googleManaged {
	var disks []googleManaged
	for _, r := range created {
		if r.Type != "google_compute_instance" {
			continue
		}
		id, _ := r.AttributeValues["id"].(string)
		boot := strings.Replace(id, "/instances/", "/disks/", 1)
		if blocks, ok := r.AttributeValues["boot_disk"].([]any); ok && len(blocks) == 1 {
			if block, ok := blocks[0].(map[string]any); ok {
				if source, _ := block["source"].(string); strings.Contains(source, "/projects/") {
					boot = source[strings.Index(source, "/projects/")+1:]
				}
			}
		}
		disks = append(disks, googleManaged{
			what:      "the boot disk " + boot + " of " + r.Address,
			typ:       "google_compute_disk",
			assetType: "compute.googleapis.com/Disk",
			match:     func(id string) bool { return id == boot },
		})
	}
	return disks
}

// checkGoogleDisks checks what the import makes of the estate's instance
// and its disks: the default selection leaves out the boot disk, named
// after the instance, as the boot disk of an instance, which manages it
// in its boot_disk block, and the import imports the data disk on its
// own, as google_compute_disk, which the instance attaches by its
// source. No disk is left out because the project's instances couldn't
// be listed: the list's fields mask is one the Compute Engine API takes.
func checkGoogleDisks(t *testing.T, coverage report.Report, created []*tfjson.StateResource) {
	t.Helper()
	boots := googleBootDisks(created)
	if len(boots) == 0 {
		t.Fatal("the estate creates no instance")
	}
	for _, b := range boots {
		i := slices.IndexFunc(coverage.Excluded, func(e report.Excluded) bool { return e.Type == b.typ && b.match(e.ID) })
		switch {
		case i < 0:
			t.Errorf("the import doesn't leave out %s", b.what)
		case !strings.Contains(coverage.Excluded[i].Reason, "the boot disk of an instance"):
			t.Errorf("the import leaves out %s, but as %q, not as an instance's boot disk", b.what, coverage.Excluded[i].Reason)
		default:
			t.Logf("%s left out: %s", coverage.Excluded[i].ID, coverage.Excluded[i].Reason)
		}
	}
	for _, e := range coverage.Excluded {
		if (e.Type == "google_compute_disk" || e.Type == "google_compute_region_disk") && strings.Contains(e.Reason, "couldn't be listed") {
			t.Errorf("%s left out as the project's instances couldn't be listed: %s", e.ID, e.Reason)
		}
	}
	imported := map[string]string{}
	for _, d := range coverage.Directories {
		for _, r := range d.Imported {
			imported[r.ID] = r.Address
		}
	}
	data := 0
	for _, r := range created {
		if r.Type != "google_compute_disk" {
			continue
		}
		data++
		id, _ := r.AttributeValues["id"].(string)
		if address := imported[id]; !strings.HasPrefix(address, "google_compute_disk.") {
			t.Errorf("the import doesn't import the data disk %s as a disk of its own: %q", id, address)
		}
	}
	if data == 0 {
		t.Error("the estate creates no data disk")
	}
}

// checkGoogleDrift does what a team does when someone changes a resource
// of an applied root outside Terraform: it applies the global root, which
// has the estate's topic, in a copy of the output, as the fixture service
// account, as checkApplied does; gives the topic another value of its
// project label, which the code sets through the provider's
// default_labels, as the fixture service account; and runs infraharvest
// drift on the root, as the read-only one. A label the code doesn't set
// at all would be absorbed: the google provider manages only the labels
// the code sets. Drift must report the label as kept, exit with 4
// with --detailed-exitcode, and leave the code as it was; accepted, the
// label must be written into the code, which then plans no change but to
// the labels the provider keeps only in state. Only the topic, a resource
// of the estate, changes in the cloud; the estate's destroy deletes it as
// it is.
func (s *googleSandbox) checkGoogleDrift(ctx context.Context, t *testing.T, estate *sandboxEstate) {
	t.Helper()
	rel := filepath.Join("google", s.project, "global")
	work := t.TempDir()
	if err := copyOutput(s.out, work); err != nil {
		t.Fatalf("copy %s: %v", s.out, err)
	}
	applied := filepath.Join(work, rel)
	applyRoot(ctx, t, applied, s.execPath, s.plugins, map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": s.fixtureCredentials})
	checkPlanClean(t, rel+": after the apply", planRoot(ctx, t, applied, s.execPath, s.plugins))

	estate.apply(ctx, t, tfexec.Var("drift_label=reported"), tfexec.Target("google_pubsub_topic.events"))
	drift := driftRun{ctx: ctx, engineName: s.engineName, execPath: s.execPath, out: work}
	code := codeFiles(t, work)
	d := drift.mustExit(t, report.ExitDrift, "--root", filepath.ToSlash(rel), "--detailed-exitcode")
	checkCodeUnchanged(t, "drift", code, work)
	checkDriftChange(t, "drift", driftChange(t, "drift", d, "google_pubsub_topic", "labels.project"), "infraharvest-sandbox", "reported", report.DriftKeep, report.DriftDecidedByDefault, report.DriftKept)

	d = drift.mustExit(t, report.ExitOK, "--root", filepath.ToSlash(rel), "--accept", "type=google_pubsub_topic")
	checkDriftChange(t, "drift --accept", driftChange(t, "drift --accept", d, "google_pubsub_topic", "labels.project"), "infraharvest-sandbox", "reported", report.DriftAccept, report.DriftDecidedByFlag, report.DriftWritten)
	if !codeMatches(t, work, `project\s*=\s*"reported"`) {
		t.Errorf("drift --accept: the code doesn't have the topic's label project = \"reported\"")
	}
	checkPlanClean(t, rel+": drift accepted", planRoot(ctx, t, applied, s.execPath, s.plugins))
}
