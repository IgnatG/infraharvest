// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// testPage is a page of a fake list: its items, and the index of the page
// after it.
type testPage struct {
	Items []*string
	Next  int
}

var errPageFailed = errors.New("page failed")

// pagerOf pages through pages, failing with errPageFailed when it reaches
// the page at index failing (-1: none fails).
func pagerOf(pages [][]*string, failing int) *runtime.Pager[testPage] {
	return runtime.NewPager(runtime.PagingHandler[testPage]{
		More: func(p testPage) bool { return p.Next < len(pages) },
		Fetcher: func(_ context.Context, current *testPage) (testPage, error) {
			i := 0
			if current != nil {
				i = current.Next
			}
			if i == failing {
				return testPage{}, errPageFailed
			}
			return testPage{Items: pages[i], Next: i + 1}, nil
		},
	})
}

func pageItems(p testPage) []*string { return p.Items }

// values dereferences items.
func values(items []*string) []string {
	var out []string
	for _, item := range items {
		out = append(out, *item)
	}
	return out
}

// Every page is walked in order; nil items and empty pages add nothing.
func TestListAllWalksEveryPage(t *testing.T) {
	pages := [][]*string{
		{to.Ptr("a"), nil, to.Ptr("b")},
		{},
		{nil},
		{to.Ptr("c")},
	}
	for name, list := range map[string]func(context.Context, *runtime.Pager[testPage], func(testPage) []*string) ([]*string, error){
		"listAll":        listAll[testPage, string],
		"listAllLenient": listAllLenient[testPage, string],
	} {
		got, err := list(t.Context(), pagerOf(pages, -1), pageItems)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if want := []string{"a", "b", "c"}; !reflect.DeepEqual(values(got), want) {
			t.Errorf("%s: got %v, want %v", name, values(got), want)
		}

		got, err = list(t.Context(), pagerOf([][]*string{{}}, -1), pageItems)
		if err != nil || len(got) != 0 {
			t.Errorf("%s, one empty page: got %v, %v", name, values(got), err)
		}
	}
}

// listAll stops at the first page that fails, with the items of the pages
// before it and the error.
func TestListAllStopsAtAFailingPage(t *testing.T) {
	pages := [][]*string{{to.Ptr("a")}, {to.Ptr("b")}, {to.Ptr("c")}}
	got, err := listAll(t.Context(), pagerOf(pages, 1), pageItems)
	if !errors.Is(err, errPageFailed) {
		t.Errorf("error %v, want %v", err, errPageFailed)
	}
	if want := []string{"a"}; !reflect.DeepEqual(values(got), want) {
		t.Errorf("got %v, want %v", values(got), want)
	}

	got, err = listAll(t.Context(), pagerOf(pages, 0), pageItems)
	if !errors.Is(err, errPageFailed) || len(got) != 0 {
		t.Errorf("first page failing: got %v, %v", values(got), err)
	}
}

// listAllLenient keeps what it listed when a later page fails, but a
// first page that fails is still an error.
func TestListAllLenientKeepsEarlierPages(t *testing.T) {
	pages := [][]*string{{to.Ptr("a")}, {to.Ptr("b")}, {to.Ptr("c")}}
	got, err := listAllLenient(t.Context(), pagerOf(pages, 2), pageItems)
	if err != nil {
		t.Errorf("a later page failing: %v", err)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(values(got), want) {
		t.Errorf("got %v, want %v", values(got), want)
	}

	got, err = listAllLenient(t.Context(), pagerOf(pages, 0), pageItems)
	if !errors.Is(err, errPageFailed) || len(got) != 0 {
		t.Errorf("first page failing: got %v, %v", values(got), err)
	}
}

// clientSettings reads what Select passes, and gives zero values for
// arguments that are missing or of another type rather than panicking.
func TestClientSettings(t *testing.T) {
	options := &arm.ClientOptions{}
	s := &Service{}
	s.Configure(harvest.ListerConfig{Settings: map[string]interface{}{
		"subscription_id": "sub",
		"resource_group":  "rg1",
		"credential":      azcore.TokenCredential(fakeCredential{}),
		"client_options":  options,
	}})
	subscriptionID, resourceGroup, credential, gotOptions := s.clientSettings()
	if subscriptionID != "sub" || resourceGroup != "rg1" || credential == nil || gotOptions != options {
		t.Errorf("got %q, %q, %v, %v", subscriptionID, resourceGroup, credential, gotOptions)
	}

	for name, args := range map[string]map[string]interface{}{
		"no arguments": nil,
		"other types": {
			"subscription_id": 1,
			"resource_group":  true,
			"credential":      "token",
			"client_options":  arm.ClientOptions{},
		},
	} {
		s := &Service{}
		s.Configure(harvest.ListerConfig{Settings: args})
		subscriptionID, resourceGroup, credential, options := s.clientSettings()
		if subscriptionID != "" || resourceGroup != "" || credential != nil || options != nil {
			t.Errorf("%s: got %q, %q, %v, %v", name, subscriptionID, resourceGroup, credential, options)
		}
	}
}

// recordBare adds a resource of the service's provider with no
// attributes.
func TestRecordBare(t *testing.T) {
	s := &Service{}
	s.Configure(harvest.ListerConfig{Provider: "azurerm"})
	const id = "/subscriptions/sub/resourceGroups/rg1"
	s.recordBare(id, "rg1", "azurerm_resource_group")

	want := []listedResource{{Type: "azurerm_resource_group", ID: id, Name: "rg1"}}
	if got := listedResources(s.Resources); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := s.Resources[0].Provider; got != "azurerm" {
		t.Errorf("provider %q", got)
	}
}
