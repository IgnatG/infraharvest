// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/sqladmin/v1"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// sqlAdmin serves the SQL Admin API's instance and database lists: two
// pages of instances, and the databases of each primary in us-central1.
// The instance stopped can't list its databases. Every request's path is
// recorded, so a test can tell which instances were asked about.
func sqlAdmin(t *testing.T) (server *httptest.Server, requested func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			http.Error(w, r.Method+" "+r.URL.Path, http.StatusMethodNotAllowed)
			return
		}
		switch {
		case r.URL.Path == "/v1/projects/acme/instances" && r.URL.Query().Get("pageToken") == "":
			_, _ = w.Write([]byte(`{"items": [
				{"name": "orders", "region": "us-central1", "databaseVersion": "MYSQL_8_0", "instanceType": "CLOUD_SQL_INSTANCE"},
				{"name": "orders-replica", "region": "us-central1", "databaseVersion": "MYSQL_8_0", "instanceType": "READ_REPLICA_INSTANCE", "masterInstanceName": "acme:orders"},
				{"name": "reports", "region": "europe-west1", "databaseVersion": "POSTGRES_16", "instanceType": "CLOUD_SQL_INSTANCE"}
			], "nextPageToken": "p2"}`))
		case r.URL.Path == "/v1/projects/acme/instances" && r.URL.Query().Get("pageToken") == "p2":
			_, _ = w.Write([]byte(`{"items": [
				{"name": "billing", "region": "us-central1", "databaseVersion": "POSTGRES_16", "instanceType": "CLOUD_SQL_INSTANCE"},
				{"name": "stopped", "region": "us-central1", "databaseVersion": "POSTGRES_16", "instanceType": "CLOUD_SQL_INSTANCE"},
				{"name": "legacy-source", "region": "us-central1", "databaseVersion": "MYSQL_5_7", "instanceType": "ON_PREMISES_INSTANCE"}
			]}`))
		case r.URL.Path == "/v1/projects/acme/instances/orders/databases":
			_, _ = w.Write([]byte(`{"items": [{"name": "app", "instance": "orders", "project": "acme"}, {"name": "mysql", "instance": "orders", "project": "acme"}]}`))
		case r.URL.Path == "/v1/projects/acme/instances/billing/databases":
			_, _ = w.Write([]byte(`{"items": [{"name": "postgres", "instance": "billing", "project": "acme"}, {"name": "ledger", "instance": "billing", "project": "acme"}]}`))
		case r.URL.Path == "/v1/projects/acme/instances/stopped/databases":
			http.Error(w, `{"error": {"code": 400, "message": "Invalid request since instance is not running."}}`, http.StatusBadRequest)
		default:
			http.Error(w, `{"error": {"code": 404, "message": "`+r.URL.Path+`"}}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

// newCloudSQL is a lister of region of the project acme, with none of the
// instance lists of other tests kept: a server on the same port would
// otherwise give a test another's.
func newCloudSQL(t *testing.T, region string) *CloudSQLLister {
	t.Helper()
	forgetSQLInstances(t)
	return cloudSQLLister(t, region)
}

// forgetSQLInstances drops the instance lists the process has kept, now
// and when the test ends.
func forgetSQLInstances(t *testing.T) {
	t.Helper()
	sqlInstanceLists.forget()
	t.Cleanup(sqlInstanceLists.forget)
}

// cloudSQLLister is a lister of region of the project acme, with the
// test's context.
func cloudSQLLister(t *testing.T, region string) *CloudSQLLister {
	t.Helper()
	g := &CloudSQLLister{}
	g.Configure(harvest.ListerConfig{Provider: "google", Settings: map[string]interface{}{"project": "acme", "region": compute.Region{Name: region}}})
	g.SetContext(t.Context())
	return g
}

func TestCloudSQLList(t *testing.T) {
	server, requested := sqlAdmin(t)
	t.Setenv(EndpointEnv, server.URL)

	g := newCloudSQL(t, "us-central1")
	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, r := range g.Resources {
		got = append(got, r.Address.Type+" "+r.Observed.ID+" "+r.RawName)
	}
	sort.Strings(got)
	// Both pages of instances, only those in us-central1, no replica or
	// external source, and nothing of the instance whose databases can't
	// be listed. The catalogue, not the lister, leaves out mysql and
	// postgres.
	want := []string{
		"google_sql_database projects/acme/instances/billing/databases/ledger billing-ledger",
		"google_sql_database projects/acme/instances/billing/databases/postgres billing-postgres",
		"google_sql_database projects/acme/instances/orders/databases/app orders-app",
		"google_sql_database projects/acme/instances/orders/databases/mysql orders-mysql",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("resources:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	for _, r := range g.Resources {
		if r.Observed.ID != "projects/acme/instances/orders/databases/app" {
			continue
		}
		wantAttributes := map[string]string{"project": "acme", "instance": "orders", "name": "app", "database_version": "MYSQL_8_0"}
		if !reflect.DeepEqual(r.Observed.Attributes, wantAttributes) {
			t.Errorf("attributes %v, want %v", r.Observed.Attributes, wantAttributes)
		}
		if r.Provider != "google" {
			t.Errorf("provider %q", r.Provider)
		}
	}

	for _, path := range requested() {
		for _, instance := range []string{"orders-replica", "reports", "legacy-source"} {
			if path == "/v1/projects/acme/instances/"+instance+"/databases" {
				t.Errorf("asked for the databases of %s", instance)
			}
		}
	}
}

// The imports of a project's regions share one instance list, each taking
// the instances in its region, also when they list at once. Before, each
// listed every instance of the project again.
func TestCloudSQLListsInstancesOncePerProject(t *testing.T) {
	server, requested := sqlAdmin(t)
	t.Setenv(EndpointEnv, server.URL)
	forgetSQLInstances(t)

	regions := []string{"us-central1", "europe-west1", "us-central1", "asia-east1"}
	listers := make([]*CloudSQLLister, len(regions))
	errs := make([]error, len(regions))
	var wg sync.WaitGroup
	for i, region := range regions {
		listers[i] = cloudSQLLister(t, region)
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = listers[i].List()
		}()
	}
	wg.Wait()

	for i, want := range []int{4, 0, 4, 0} {
		if errs[i] != nil {
			t.Fatalf("%s: %v", regions[i], errs[i])
		}
		if got := len(listers[i].Resources); got != want {
			t.Errorf("%s: got %d resources, want %d", regions[i], got, want)
		}
	}
	lists := 0
	for _, path := range requested() {
		if path == "/v1/projects/acme/instances" {
			lists++
		}
	}
	// Both pages, once.
	if lists != 2 {
		t.Errorf("got %d instance list requests, want 2: %v", lists, requested())
	}
}

// An instance list that fails is kept, so that the imports of the
// project's other regions don't make the failing call again: they fail
// too, without a request. A process that lists afresh lists again.
func TestCloudSQLKeepsAFailure(t *testing.T) {
	server, _ := sqlAdmin(t)
	var mu sync.Mutex
	failing := true
	requests := 0
	failOnce := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		f := failing
		failing = false
		requests++
		mu.Unlock()
		if f {
			http.Error(w, `{"error": {"message": "failed"}}`, http.StatusForbidden)
			return
		}
		server.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(failOnce.Close)
	t.Setenv(EndpointEnv, failOnce.URL)

	if err := newCloudSQL(t, "us-central1").List(); err == nil {
		t.Fatal("the failed list succeeded")
	}
	if err := cloudSQLLister(t, "europe-west1").List(); err == nil {
		t.Error("another region's import listed again after the failure")
	}
	mu.Lock()
	made := requests
	mu.Unlock()
	if made != 1 {
		t.Errorf("got %d requests, want the failing one", made)
	}
	g := newCloudSQL(t, "us-central1")
	if err := g.List(); err != nil || len(g.Resources) != 4 {
		t.Errorf("listing afresh: got %d resources, %v", len(g.Resources), err)
	}
}

// No instance is global, so the import of global asks nothing.
func TestCloudSQLGlobal(t *testing.T) {
	server, requested := sqlAdmin(t)
	t.Setenv(EndpointEnv, server.URL)

	g := newCloudSQL(t, "")
	if err := g.List(); err != nil {
		t.Fatal(err)
	}
	if len(g.Resources) != 0 {
		t.Errorf("%d resources, want none", len(g.Resources))
	}
	if paths := requested(); len(paths) != 0 {
		t.Errorf("requests %v, want none", paths)
	}
}

// A region with no instances lists nothing, without asking for databases.
func TestCloudSQLRegionWithoutInstances(t *testing.T) {
	server, requested := sqlAdmin(t)
	t.Setenv(EndpointEnv, server.URL)

	g := newCloudSQL(t, "asia-east1")
	if err := g.List(); err != nil {
		t.Fatal(err)
	}
	if len(g.Resources) != 0 {
		t.Errorf("%d resources, want none", len(g.Resources))
	}
	for _, path := range requested() {
		if strings.HasSuffix(path, "/databases") {
			t.Errorf("asked %s", path)
		}
	}
}

// A failure to list the instances, on any page, is an error, not a
// partial listing.
func TestCloudSQLInstanceListFails(t *testing.T) {
	for _, tt := range []struct {
		name   string
		failOn string
		status int
	}{
		{"first page denied", "", http.StatusForbidden},
		{"second page fails", "p2", http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Query().Get("pageToken") == tt.failOn {
					http.Error(w, `{"error": {"message": "failed"}}`, tt.status)
					return
				}
				_, _ = w.Write([]byte(`{"items": [{"name": "orders", "region": "us-central1", "instanceType": "CLOUD_SQL_INSTANCE"}], "nextPageToken": "p2"}`))
			}))
			defer server.Close()
			t.Setenv(EndpointEnv, server.URL)

			g := newCloudSQL(t, "us-central1")
			err := g.List()
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), "acme") {
				t.Errorf("error %q doesn't name the project", err)
			}
			if len(g.Resources) != 0 {
				t.Errorf("%d resources, want none", len(g.Resources))
			}
		})
	}
}

func TestOwnsDatabases(t *testing.T) {
	for _, tt := range []struct {
		instanceType, master string
		want                 bool
	}{
		{"CLOUD_SQL_INSTANCE", "", true},
		{"", "", true},
		{"READ_REPLICA_INSTANCE", "acme:orders", false},
		{"READ_POOL_INSTANCE", "acme:orders", false},
		{"ON_PREMISES_INSTANCE", "", false},
		// A replica of a server outside Cloud SQL names its source.
		{"CLOUD_SQL_INSTANCE", "legacy-source", false},
	} {
		instance := &sqladmin.DatabaseInstance{InstanceType: tt.instanceType, MasterInstanceName: tt.master}
		if got := ownsDatabases(instance); got != tt.want {
			t.Errorf("%q with primary %q: got %v, want %v", tt.instanceType, tt.master, got, tt.want)
		}
	}
}

// A project without the SQL Admin API enabled has no instances, which
// isn't a failure: --services=* lists cloudsql in every project.
func TestCloudSQLAPIDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error": {"code": 403, "status": "PERMISSION_DENIED", "message": "Cloud SQL Admin API has not been used in project acme before or it is disabled.", "details": [{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "SERVICE_DISABLED", "domain": "googleapis.com"}]}}`))
	}))
	defer server.Close()
	t.Setenv(EndpointEnv, server.URL)

	g := newCloudSQL(t, "us-central1")
	if err := g.List(); err != nil || len(g.Resources) != 0 {
		t.Errorf("got %v, %v; want nothing and no error", g.Resources, err)
	}
}
