// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/compute/v1"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/providers/providertest"
)

// Every lister of the provider's services keeps the listers' contract
// (see providertest.Contract) against a fake Google Cloud: nothing listed
// from an empty project; only the types it declares (see listedTypes)
// listed from one with a resource of each list it asks for; a refusal
// (403 PERMISSION_DENIED) classified by googleCodes as a failure; and a
// canceled context honoured. Each lists the region it lists in: global
// for the project's own, us-central1 for the regional. The query service
// lists through Terraform, not Google's APIs. As fakeGoogle sets the
// environment, the listers take turns.
func TestListerContract(t *testing.T) {
	regions := map[string]string{cloudSQLService: "us-central1", schedulerJobService: "us-central1"}
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for name := range services {
			if name != harvest.QueryService && !yield(name) {
				return
			}
		}
	}) {
		t.Run(name, func(t *testing.T) {
			providertest.Contract{
				Declared: declaredTypes(name),
				List: func(ctx context.Context, t *testing.T, cloud *providertest.Cloud) ([]harvest.Resource, error) {
					t.Setenv(EndpointEnv, cloud.Serve(t))
					g := services[name]().(harvest.SettingsTaker[listerSettings])
					g.SetSettings(listerSettings{project: "acme", region: compute.Region{Name: regions[name]}})
					return g.(harvest.Lister).List(fastRetriesIn(ctx), harvest.Settings{Provider: "google"})
				},
				Empty:  func(providertest.Request) providertest.Answer { return providertest.JSON(`{}`) },
				Listed: listedAnswer,
				Denied: func(providertest.Request) providertest.Answer {
					return providertest.Status(http.StatusForbidden, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"The caller does not have permission"}}`)
				},
				Codes: googleCodes,
			}.Run(t)
		})
	}
}

// declaredTypes are the types the service called name declares it lists
// (see listedTypes).
func declaredTypes(name string) []string {
	var declared []string
	for typ, service := range listedTypes {
		if service == name {
			declared = append(declared, typ)
		}
	}
	return declared
}

// listedAnswer answers each list a lister asks for with a resource of
// the project acme: an asset, an IAM binding, a Cloud SQL instance in
// us-central1 and its database, a scheduler job, and a DNS zone and its
// record set.
func listedAnswer(r providertest.Request) providertest.Answer {
	switch {
	case strings.HasSuffix(r.Path, ":searchAllResources"):
		return jsonOf(map[string]any{"results": []map[string]any{
			asset("pubsub.googleapis.com/Topic", "//pubsub.googleapis.com/projects/acme/topics/one", "global", nil),
		}})
	case strings.HasSuffix(r.Path, ":getIamPolicy"):
		return providertest.JSON(`{"version": 3, "bindings": [{"role": "roles/viewer", "members": ["user:ana@acme.example"]}]}`)
	case r.Path == "/v1/projects/acme/instances":
		return providertest.JSON(`{"items": [{"name": "orders", "region": "us-central1", "instanceType": "CLOUD_SQL_INSTANCE"}]}`)
	case r.Path == "/v1/projects/acme/instances/orders/databases":
		return providertest.JSON(`{"items": [{"name": "app", "instance": "orders", "project": "acme"}]}`)
	case r.Path == "/v1/projects/acme/locations/us-central1/jobs":
		return providertest.JSON(`{"jobs": [{"name": "projects/acme/locations/us-central1/jobs/nightly"}]}`)
	case r.Path == "/dns/v1/projects/acme/managedZones":
		return providertest.JSON(`{"managedZones": [{"name": "example-com", "dnsName": "example.com."}]}`)
	case r.Path == "/dns/v1/projects/acme/managedZones/example-com/rrsets":
		return providertest.JSON(`{"rrsets": [{"name": "www.example.com.", "type": "A", "ttl": 300, "rrdatas": ["203.0.113.10"]}]}`)
	}
	return unexpected(r)
}
