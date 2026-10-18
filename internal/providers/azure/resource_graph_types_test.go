// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/magodo/azlist/azlist"

	"github.com/infraharvest/infraharvest-cli/internal/catalogue"
)

const testWorkspace = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-logs/providers/Microsoft.OperationalInsights/workspaces/Logs"

// listedTable is a workspace's table as Resource Manager lists it, with
// its type, plan and whether its retentions are the workspace's.
func listedTable(t *testing.T, name, tableType, plan string, asDefault bool) azlist.AzureResource {
	t.Helper()
	id := testWorkspace + "/tables/" + name
	return listed(t, id, map[string]interface{}{
		"id":   id,
		"name": name,
		"type": "Microsoft.OperationalInsights/workspaces/tables",
		"properties": map[string]interface{}{
			"plan":                          plan,
			"retentionInDaysAsDefault":      asDefault,
			"totalRetentionInDaysAsDefault": asDefault,
			"schema":                        map[string]interface{}{"name": name, "tableType": tableType},
		},
	})
}

// testLookup is newLookup with every request that reaches Resource
// Manager answered by transport.
func testLookup(ctx context.Context, transport *fakeTransport, listedResources []azlist.AzureResource) (queryType, *atomic.Int64) {
	return newLookup(ctx, fakeCredential{}, arm.ClientOptions{ClientOptions: policy.ClientOptions{Transport: transport}}, listedResources)
}

// A workspace's tables get their azurerm type from the table type they
// are listed with, with no request: before, aztft sent a GET for each, and
// a workspace in the Azure sandbox took two and a half minutes to map its
// 697 tables. Tables of restored logs and of search jobs' results, which
// azurerm has no type for, are left out, with no request either.
func TestWorkspaceTablesMapFromTheListing(t *testing.T) {
	listedResources := []azlist.AzureResource{
		listedTable(t, "AACAudit", "Microsoft", "Analytics", true),
		listedTable(t, "AppTraces", "Microsoft", "Basic", true),
		listedTable(t, "Orders_CL", "CustomLog", "Analytics", true),
		listedTable(t, "Orders_RST", "RestoredLogs", "Analytics", false),
		listedTable(t, "Errors_SRCH", "SearchResults", "Analytics", false),
	}
	transport := &fakeTransport{body: `{}`}
	query, sent := testLookup(t.Context(), transport, listedResources)
	cat := catalogue.MustBuiltin("azurerm")
	resources := graphResources(listedResources, cat, query)
	if len(transport.paths) != 0 || sent.Load() != 0 {
		t.Errorf("sent %d requests: %v", sent.Load(), transport.paths)
	}
	got := map[string]string{}
	for _, r := range resources {
		got[r.Observed.ID] = r.Address.Type
	}
	want := map[string]string{
		testWorkspace + "/tables/AACAudit":  microsoftTableType,
		testWorkspace + "/tables/AppTraces": microsoftTableType,
		testWorkspace + "/tables/Orders_CL": customLogTableType,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, r := range resources {
		reason := cat.Skipped(r.Address.Type, r.Observed.ID, r.Observed.Attributes)
		if builtIn := strings.HasSuffix(r.Observed.ID, "/AACAudit"); builtIn != (reason != "") {
			t.Errorf("%s %s: skipped %q", r.Address.Type, r.Observed.ID, reason)
		}
	}
}

// The requests that mapping a workspace's tables takes don't grow with
// the tables: none, however many there are, and the built-in ones are
// still recorded, for the report to count them as excluded, with the
// catalogue's reason. Listed without their type, each table costs aztft
// a GET, as every table did before.
func TestBuiltInTablesTakeNoRequests(t *testing.T) {
	cat := catalogue.MustBuiltin("azurerm")
	for _, n := range []int{1, 10, 100} {
		var withType, withoutType []azlist.AzureResource
		for i := range n {
			name := fmt.Sprintf("Table%03d", i)
			withType = append(withType, listedTable(t, name, "Microsoft", "Analytics", true))
			withoutType = append(withoutType, listed(t, testWorkspace+"/tables/"+name, nil))
		}

		transport := &fakeTransport{body: `{}`}
		query, sent := testLookup(t.Context(), transport, withType)
		resources := graphResources(withType, cat, query)
		if len(transport.paths) != 0 || sent.Load() != 0 {
			t.Errorf("%d tables: sent %d requests", n, sent.Load())
		}
		if len(resources) != n {
			t.Fatalf("%d tables: recorded %d", n, len(resources))
		}
		for _, r := range resources {
			if r.Address.Type != microsoftTableType || cat.Skipped(r.Address.Type, r.Observed.ID, r.Observed.Attributes) == "" {
				t.Errorf("%d tables: %s %s isn't skipped as built in", n, r.Address.Type, r.Observed.ID)
			}
		}

		transport = &fakeTransport{body: `{"id":"table","properties":{"schema":{"tableType":"Microsoft"}}}`}
		query, sent = testLookup(t.Context(), transport, withoutType)
		resources = graphResources(withoutType, cat, query)
		if got := int(sent.Load()); got != n || len(transport.paths) != n {
			t.Errorf("%d tables listed without their type: sent %d requests, want one each", n, got)
		}
		if len(resources) != n {
			t.Errorf("%d tables listed without their type: recorded %d", n, len(resources))
		}
	}
}

// What aztft reads to choose between types, or to find a resource's parts
// that azurerm imports as resources of their own, it reads from the
// listing: a function app's kind, a virtual machine's operating system
// and data disks. It asks Resource Manager only about a resource the
// listing didn't return, or returned without what it reads.
func TestLookupReadsTheListing(t *testing.T) {
	const rg = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg1"
	functionApp := rg + "/providers/Microsoft.Web/sites/func"
	vm := rg + "/providers/Microsoft.Compute/virtualMachines/vm1"
	unlisted := rg + "/providers/Microsoft.Web/sites/unlisted"
	partial := rg + "/providers/Microsoft.Web/sites/partial"
	listedResources := []azlist.AzureResource{
		listed(t, functionApp, map[string]interface{}{
			"id":         functionApp,
			"name":       "func",
			"type":       "microsoft.web/sites",
			"kind":       "functionapp,linux",
			"properties": map[string]interface{}{"sku": "Dynamic"},
		}),
		listed(t, vm, map[string]interface{}{
			"id":   vm,
			"name": "vm1",
			"type": "microsoft.compute/virtualmachines",
			"properties": map[string]interface{}{
				"osProfile": map[string]interface{}{"computerName": "vm1", "linuxConfiguration": map[string]interface{}{}},
				"storageProfile": map[string]interface{}{
					"osDisk":    map[string]interface{}{"name": "vm1-os", "osType": "Linux"},
					"dataDisks": []interface{}{},
				},
			},
		}),
		listed(t, unlisted, nil),
		// Listed without its kind: aztft can't tell from the listing.
		listed(t, partial, map[string]interface{}{"id": partial, "name": "partial", "properties": map[string]interface{}{}}),
	}
	transport := &fakeTransport{body: `{"kind":"app,linux","properties":{}}`}
	query, sent := testLookup(t.Context(), transport, listedResources)
	resources := graphResources(listedResources, catalogue.MustBuiltin("azurerm"), query)
	got := map[string]string{}
	for _, r := range resources {
		got[r.Observed.ID] = r.Address.Type
	}
	want := map[string]string{
		functionApp: "azurerm_linux_function_app",
		vm:          "azurerm_linux_virtual_machine",
		unlisted:    "azurerm_linux_web_app",
		partial:     "azurerm_linux_web_app",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	paths := slices.Clone(transport.paths)
	slices.Sort(paths)
	if sent.Load() != 2 || len(paths) != 2 || !strings.HasSuffix(paths[0], "/sites/partial") || !strings.HasSuffix(paths[1], "/sites/unlisted") {
		t.Errorf("sent %d requests: %v, want one for each site the listing didn't say enough about", sent.Load(), paths)
	}
}

// Once the import is interrupted, no lookup reaches Resource Manager:
// aztft sends its requests without a context. A request of a lookup under
// way fails with an error the Azure SDK doesn't retry, seconds apart.
func TestLookupStopsWhenInterrupted(t *testing.T) {
	const site = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg1/providers/Microsoft.Web/sites/app"
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	listedResources := []azlist.AzureResource{listed(t, site, nil)}
	transport := &fakeTransport{body: `{"kind":"app,linux","properties":{}}`}
	query, sent := testLookup(ctx, transport, listedResources)
	if got := graphResources(listedResources, catalogue.MustBuiltin("azurerm"), query); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
	if len(transport.paths) != 0 || sent.Load() != 0 {
		t.Errorf("sent %d requests after the interrupt: %v", sent.Load(), transport.paths)
	}

	underWay := &listingTransport{ctx: ctx, next: transport, sent: sent}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://management.azure.com"+site+"?api-version=2024-04-01", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	var nonRetriable interface{ NonRetriable() }
	if _, err := underWay.Do(req); !errors.Is(err, context.Canceled) || !errors.As(err, &nonRetriable) {
		t.Errorf("a request after the interrupt: %v, want a non-retriable context.Canceled", err)
	}
	if len(transport.paths) != 0 {
		t.Errorf("sent %v after the interrupt", transport.paths)
	}
}

// Lookups run up to lookupParallel at once, and each item once.
func TestInParallelBoundsLookups(t *testing.T) {
	items := make([]int, 5*lookupParallel)
	for i := range items {
		items[i] = i
	}
	var mu sync.Mutex
	active, most := 0, 0
	done := make([]int, len(items))
	inParallel(items, func(i int) {
		mu.Lock()
		active++
		most = max(most, active)
		done[i]++
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
	})
	if most > lookupParallel || most < 2 {
		t.Errorf("%d ran at once, want 2 to %d", most, lookupParallel)
	}
	for i, n := range done {
		if n != 1 {
			t.Errorf("item %d done %d times", i, n)
		}
	}
}
