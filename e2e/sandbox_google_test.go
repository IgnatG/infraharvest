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
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tfjson "github.com/hashicorp/terraform-json"
	"google.golang.org/api/cloudasset/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/engine"
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

// TestSandboxGoogle creates testdata/sandbox/google in a real project as
// the fixture service account (the credentials file
// SANDBOX_GOOGLE_FIXTURE_CREDENTIALS names), waits for Cloud Asset
// Inventory to find it, imports the project's global resources and its
// region as the read-only service account
// (SANDBOX_GOOGLE_READONLY_CREDENTIALS), which has what permissions/google
// grants, as a user would, and checks that nothing was denied, that the
// engine plans every generated resource as an import with no changes, and
// that the import found what the estate created, by ID, with its labels,
// the project and the read-only account's grants. It destroys the estate
// when it is done. The sandbox-google workflow runs it, after approval;
// see e2e/README.md.
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
	checkGoogleLabels(t, s.out, coverage, created)
	if imported["google_project"] == 0 {
		t.Errorf("the import doesn't import the project %s", s.project)
	}
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
		t.Skip("SANDBOX_GOOGLE_PROJECT, SANDBOX_GOOGLE_FIXTURE_CREDENTIALS, SANDBOX_GOOGLE_READONLY_CREDENTIALS and SANDBOX_GOOGLE_READONLY_SERVICE_ACCOUNT name a sandbox project; see the sandbox-google workflow")
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
	var binary engine.Binary
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
// there to import.
func waitForInventory(ctx context.Context, t *testing.T, project string, created []*tfjson.StateResource, name string) {
	t.Helper()
	cat := catalogue.MustBuiltin("google")
	var (
		want       []*tfjson.StateResource
		assetTypes []string
	)
	for _, r := range created {
		if e, ok := cat.Entry(r.Type); ok && e.Inventory != nil {
			want = append(want, r)
			if !slices.Contains(assetTypes, e.Inventory.Type) {
				assetTypes = append(assetTypes, e.Inventory.Type)
			}
		}
	}
	if len(want) == 0 {
		t.Fatal("the estate creates nothing Cloud Asset Inventory lists")
	}
	slices.Sort(assetTypes)
	svc, err := cloudasset.NewService(ctx, option.WithScopes(cloudasset.CloudPlatformScope))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for {
		missing, earlier, err := searchEstate(ctx, svc, cat, project, assetTypes, want, name)
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
// returns the addresses of those of want it doesn't find yet and the
// names of those of earlier runs it finds: named after the sandbox's
// prefix but not after this run's name.
func searchEstate(ctx context.Context, svc *cloudasset.Service, cat *catalogue.Catalogue, project string, assetTypes []string, want []*tfjson.StateResource, name string) (missing, earlier []string, err error) {
	found := map[string]bool{}
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
				named := strings.Join(keys, " ")
				if strings.Contains(named, sandboxNamePrefix) && !strings.Contains(named, name) {
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
		if rejected, err := os.ReadFile(filepath.Join(dir, engine.RejectedFileName)); err == nil {
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

	seeded := map[string]int{}
	for _, r := range created {
		// Not random_string, which only names the estate's resources.
		if strings.HasPrefix(r.Type, "google_") {
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
// estate created, by ID, which the import IDs of the google provider's
// types are, and the read-only service account's grant of each role
// permissions/google grants, from the project's IAM policy.
func (s *googleSandbox) checkGoogleFound(t *testing.T, coverage report.Report, created []*tfjson.StateResource) {
	t.Helper()
	imported := map[string]bool{}
	for _, d := range coverage.Directories {
		for _, r := range d.Imported {
			imported[r.ID] = true
		}
	}
	for _, r := range created {
		if !strings.HasPrefix(r.Type, "google_") {
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

// checkGoogleLabels checks that the generated configuration keeps the
// labels of each resource the estate labelled: each label is in the root
// that imports the resource, on the resource, in a module call's
// arguments or in local.labels, which the provider's default_labels take.
// The plans with no changes check they are the resource's.
func checkGoogleLabels(t *testing.T, out string, coverage report.Report, created []*tfjson.StateResource) {
	t.Helper()
	roots := map[string]string{}
	for _, d := range coverage.Directories {
		for _, r := range d.Imported {
			roots[r.ID] = filepath.Join(out, filepath.FromSlash(d.Path))
		}
	}
	configurations := map[string]string{}
	labelled := 0
	for _, r := range created {
		labels, _ := r.AttributeValues["labels"].(map[string]any)
		id, _ := r.AttributeValues["id"].(string)
		root, ok := roots[id]
		if len(labels) == 0 || !ok {
			continue // checkGoogleFound reports what isn't imported
		}
		labelled++
		if _, ok := configurations[root]; !ok {
			configurations[root] = configuration(t, root)
		}
		for key, value := range labels {
			v, _ := value.(string)
			label := regexp.MustCompile(`"?` + regexp.QuoteMeta(key) + `"?\s*=\s*"` + regexp.QuoteMeta(v) + `"`)
			if !label.MatchString(configurations[root]) {
				t.Errorf("%s: the configuration doesn't have %s's label %s = %q", root, r.Address, key, v)
			}
		}
	}
	if labelled == 0 {
		t.Error("the import imports none of the resources the estate labelled")
	}
}
