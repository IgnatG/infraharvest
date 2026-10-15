// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/magodo/azlist/azlist"
	"github.com/magodo/aztft/aztft"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// kindTransport answers a list of a workspace's data sources by its
// $filter, as Resource Manager does: 400 KindNotSpecified without one,
// and the body registered for the filter, or a 403, otherwise. It records
// each request as its path and filter.
type kindTransport struct {
	bodies   map[string]string
	requests []string
}

func (f *kindTransport) Do(r *http.Request) (*http.Response, error) {
	filter := r.URL.Query().Get("$filter")
	f.requests = append(f.requests, r.URL.Path+" "+filter)
	status, body := http.StatusOK, f.bodies[filter]
	switch {
	case filter == "":
		status, body = http.StatusBadRequest, `{"error":{"code":"KindNotSpecified","message":"kind is required"}}`
	case body == "":
		status, body = http.StatusForbidden, `{"error":{"code":"AuthorizationFailed","message":"no"}}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

// Data sources are asked for one supported kind at a time, once per
// workspace, and for nothing else: before the fix, azlist listed them
// without a kind, which Resource Manager refuses with KindNotSpecified, so
// none was ever imported. A kind that fails to list leaves the others.
// The IDs are in the form azurerm parses, whatever the case of the ones
// listed, and the types come from the kind asked for.
func TestWorkspaceDataSourcesListsSupportedKinds(t *testing.T) {
	const (
		group     = "/subscriptions/sub/resourceGroups/rg1"
		workspace = group + "/providers/Microsoft.OperationalInsights/workspaces/ws1"
		path      = workspace + "/dataSources"
		lower     = "/subscriptions/sub/resourcegroups/rg1/providers/microsoft.operationalinsights/workspaces/ws1/datasources/"
	)
	for _, tc := range []struct {
		name   string
		bodies map[string]string
		want   map[string]string
	}{
		{
			name: "both kinds",
			bodies: map[string]string{
				"kind eq 'WindowsEvent'":              `{"value":[{"id":"` + lower + `system","name":"system","kind":"WindowsEvent","tags":{"team":"ops"},"properties":{"eventLogName":"System"}}]}`,
				"kind eq 'WindowsPerformanceCounter'": `{"value":[{"id":"` + lower + `cpu","name":"cpu","kind":"WindowsPerformanceCounter","properties":{}}]}`,
			},
			want: map[string]string{
				path + "/system": "azurerm_log_analytics_datasource_windows_event",
				path + "/cpu":    "azurerm_log_analytics_datasource_windows_performance_counter",
			},
		},
		{
			name: "one kind fails",
			bodies: map[string]string{
				"kind eq 'WindowsPerformanceCounter'": `{"value":[{"id":"` + lower + `cpu","name":"cpu","kind":"WindowsPerformanceCounter","properties":{}}]}`,
			},
			want: map[string]string{path + "/cpu": "azurerm_log_analytics_datasource_windows_performance_counter"},
		},
	} {
		transport := &kindTransport{bodies: tc.bodies}
		options := &arm.ClientOptions{ClientOptions: policy.ClientOptions{Transport: transport}}
		inputs := []azlist.AzureResource{
			listed(t, group, nil),
			listed(t, workspace, nil),
			listed(t, group+"/providers/Microsoft.Network/virtualNetworks/net1", nil),
		}

		found, types, err := workspaceDataSources(context.Background(), fakeCredential{}, options, inputs)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		wantRequests := []string{path + " kind eq 'WindowsEvent'", path + " kind eq 'WindowsPerformanceCounter'"}
		if !reflect.DeepEqual(transport.requests, wantRequests) {
			t.Errorf("%s: requested %q, want %q", tc.name, transport.requests, wantRequests)
		}
		wantTypes := map[string]string{}
		for id, tfType := range tc.want {
			wantTypes[strings.ToUpper(id)] = tfType
		}
		if !reflect.DeepEqual(types, wantTypes) {
			t.Errorf("%s: types %v, want %v", tc.name, types, wantTypes)
		}
		var ids []string
		for _, res := range found {
			ids = append(ids, res.Id.String())
			if _, ok := tc.want[res.Id.String()]; !ok {
				t.Errorf("%s: listed %s, want one of %v", tc.name, res.Id, tc.want)
			}
		}
		if len(ids) != len(tc.want) {
			t.Errorf("%s: listed %v, want %v", tc.name, ids, tc.want)
		}
	}
}

// A data source becomes a resource of the type its kind gives, without
// asking aztft, which would read each one from Resource Manager to learn
// its kind; other resources still go to aztft.
func TestKnownTypesSkipsTheQuery(t *testing.T) {
	const (
		workspace = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg1/providers/Microsoft.OperationalInsights/workspaces/ws1"
		source    = workspace + "/dataSources/system"
	)
	var asked []string
	query := knownTypes(map[string]string{strings.ToUpper(source): "azurerm_log_analytics_datasource_windows_event"},
		func(id string) ([]aztft.Type, []string, bool, error) {
			asked = append(asked, id)
			return nil, nil, true, nil
		})
	resources := graphResources([]azlist.AzureResource{
		listed(t, workspace, nil),
		listed(t, source, map[string]interface{}{"kind": "WindowsEvent", "tags": map[string]interface{}{"team": "ops"}}),
	}, catalogue.MustBuiltin("azurerm"), query)

	if !reflect.DeepEqual(asked, []string{workspace}) {
		t.Errorf("asked aztft about %v, want only the workspace", asked)
	}
	want := []listedResource{{
		Type:       "azurerm_log_analytics_datasource_windows_event",
		ID:         source,
		Name:       "system",
		Attributes: map[string]string{"name": "system", "resource_group_name": "rg1", "tags.team": "ops"},
	}}
	if got := listedResources(resources); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// azlist would list a workspace's data sources as its child resources,
// without a kind; the lister's schema tree drops them, and only them.
func TestWithoutWorkspaceDataSources(t *testing.T) {
	tree, err := azlist.BuildARMSchemaTree(azlist.ARMSchemaFile)
	if err != nil {
		t.Fatal(err)
	}
	workspace := tree[strings.ToUpper(workspaceType)]
	if workspace == nil || workspace.Children["DATASOURCES"] == nil || workspace.Children["TABLES"] == nil {
		t.Fatal("azlist's schema has no workspace data sources or tables any more; the test needs another child type")
	}

	withoutWorkspaceDataSources(tree)

	if _, ok := workspace.Children["DATASOURCES"]; ok {
		t.Error("data sources are still a workspace's child type")
	}
	if workspace.Children["TABLES"] == nil {
		t.Error("tables are no longer a workspace's child type")
	}
}
