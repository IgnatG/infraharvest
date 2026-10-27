// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

func TestNewestStable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		versions []string
		want     string
	}{
		{[]string{"2024-05-01-preview", "2024-11-01", "2025-07-01-preview", "2023-01-01"}, "2024-11-01"},
		{[]string{"2025-01-01-preview", "2025-02-01-beta"}, ""},
		{nil, ""},
	} {
		if got := newestStable(tc.versions); got != tc.want {
			t.Errorf("%v: got %q, want %q", tc.versions, got, tc.want)
		}
	}
}

// providersAnswer is what GET /subscriptions/{id}/providers/Microsoft.AzureFleet
// answers, cut down to the resource types and their API versions.
const providersAnswer = `{"namespace": "Microsoft.AzureFleet", "resourceTypes": [
  {"resourceType": "fleets", "apiVersions": ["2025-07-01-preview", "2024-11-01", "2024-05-01-preview"]},
  {"resourceType": "fleets/previews", "apiVersions": ["2025-01-01-preview"]}
]}`

// Each azapi_resource gets the newest stable API version of its type,
// from one request per namespace for the run; a type with only previews
// gets none, which leaves the version to azapi, and other types aren't
// asked about.
func TestWithAPIVersions(t *testing.T) {
	const rg = "/subscriptions/sub/resourceGroups/rg1"
	fleet := rg + "/providers/Microsoft.AzureFleet/fleets/"
	resources := []harvest.Resource{
		harvest.Record(fleet+"batch", "batch", azapiType, "azurerm", map[string]string{}),
		harvest.Record(fleet+"web", "web", azapiType, "azurerm", map[string]string{}),
		harvest.Record(fleet+"batch/previews/p", "p", azapiType, "azurerm", map[string]string{}),
		harvest.Record(rg, "rg1", "azurerm_resource_group", "azurerm", map[string]string{}),
	}
	transport := &fakeTransport{body: providersAnswer}
	options := &arm.ClientOptions{ClientOptions: policy.ClientOptions{Transport: transport}}
	cache := &versionCache{}

	withAPIVersions(t.Context(), cache, fakeCredential{}, options, "sub", resources)
	got := map[string]string{}
	for _, r := range resources {
		got[r.RawName] = r.Observed.Attributes[apiVersionAttribute]
	}
	if want := map[string]string{"batch": "2024-11-01", "web": "2024-11-01", "p": "", "rg1": ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("API versions: got %v, want %v", got, want)
	}
	if want := []string{"/subscriptions/sub/providers/Microsoft.AzureFleet"}; !reflect.DeepEqual(transport.paths, want) {
		t.Errorf("requests: got %v, want %v", transport.paths, want)
	}

	// Another subscription of the same cloud asks no more.
	withAPIVersions(t.Context(), cache, fakeCredential{}, options, "sub-2", resources[:1])
	if len(transport.paths) != 1 {
		t.Errorf("requests after a second listing: %v", transport.paths)
	}
	// Without an azapi_resource, nothing is asked.
	idle := &fakeTransport{body: providersAnswer}
	withAPIVersions(t.Context(), &versionCache{}, fakeCredential{}, &arm.ClientOptions{ClientOptions: policy.ClientOptions{Transport: idle}}, "sub", resources[3:])
	if len(idle.paths) != 0 {
		t.Errorf("requests without azapi resources: %v", idle.paths)
	}
}

// refusingTransport refuses the first request, as for a subscription the
// credential may not read, and answers the rest as fakeTransport does.
type refusingTransport struct {
	fakeTransport
	refused bool
}

func (f *refusingTransport) Do(r *http.Request) (*http.Response, error) {
	if !f.refused {
		f.refused = true
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error": {"code": "AuthorizationFailed", "message": "no"}}`)),
			Request:    r,
		}, nil
	}
	return f.fakeTransport.Do(r)
}

// A namespace whose request failed, as in one subscription, is asked about
// again by the next subscription, rather than left without versions for
// the rest of the run.
func TestWithAPIVersionsAsksAgainAfterAFailure(t *testing.T) {
	transport := &refusingTransport{fakeTransport: fakeTransport{body: providersAnswer}}
	options := &arm.ClientOptions{ClientOptions: policy.ClientOptions{Transport: transport}}
	cache := &versionCache{}
	id := "/subscriptions/%s/resourceGroups/rg1/providers/Microsoft.AzureFleet/fleets/batch"

	first := []harvest.Resource{harvest.Record(fmt.Sprintf(id, "sub"), "batch", azapiType, "azurerm", map[string]string{})}
	withAPIVersions(t.Context(), cache, fakeCredential{}, options, "sub", first)
	if v := first[0].Observed.Attributes[apiVersionAttribute]; v != "" {
		t.Errorf("refused: API version %q", v)
	}
	second := []harvest.Resource{harvest.Record(fmt.Sprintf(id, "sub-2"), "batch", azapiType, "azurerm", map[string]string{})}
	withAPIVersions(t.Context(), cache, fakeCredential{}, options, "sub-2", second)
	if v := second[0].Observed.Attributes[apiVersionAttribute]; v != "2024-11-01" {
		t.Errorf("after a refusal: API version %q, want 2024-11-01", v)
	}
	if want := []string{"/subscriptions/sub/providers/Microsoft.AzureFleet", "/subscriptions/sub-2/providers/Microsoft.AzureFleet"}; !reflect.DeepEqual(transport.paths, want) {
		t.Errorf("requests: got %v, want %v", transport.paths, want)
	}
}
