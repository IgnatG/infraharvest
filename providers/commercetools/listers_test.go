// Copyright 2018 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package commercetools

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/labd/commercetools-go-sdk/platform"
)

func TestListAllPagesByID(t *testing.T) {
	var ids []string
	for i := 0; i < pageSize+3; i++ {
		ids = append(ids, fmt.Sprintf("id-%04d", i))
	}
	var calls [][]string
	got, err := listAll(func(where []string) ([]string, error) {
		calls = append(calls, where)
		start := 0
		if len(where) > 0 {
			start = pageSize
		}
		end := min(start+pageSize, len(ids))
		return ids[start:end], nil
	}, func(id string) string { return id })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, ids) {
		t.Fatalf("got %d ids, want %d", len(got), len(ids))
	}
	want := [][]string{nil, {fmt.Sprintf("id > %q", ids[pageSize-1])}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("where predicates %q, want %q", calls, want)
	}
}

func TestListAllStopsOnError(t *testing.T) {
	_, err := listAll(func([]string) ([]string, error) {
		return nil, errors.New("boom")
	}, func(id string) string { return id })
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestResourceNames(t *testing.T) {
	key := "my-key"
	tests := []struct {
		name     string
		got      string
		wantName string
	}{
		{"product type key", productTypeResources([]platform.ProductType{{ID: "1", Key: &key, Name: "Ignored"}})[0].RawName, "my-key"},
		{"product type name", productTypeResources([]platform.ProductType{{ID: "1", Name: "My Type"}})[0].RawName, "my-type"},
		{"zone key", shippingZoneResources([]platform.Zone{{ID: "1", Key: &key, Name: "Ignored"}})[0].RawName, "my-key"},
		{"zone name", shippingZoneResources([]platform.Zone{{ID: "1", Name: "Europe"}})[0].RawName, "Europe"},
		{"extension without key", apiExtensionResources([]platform.Extension{{ID: "1"}})[0].RawName, ""},
		{"channel", channelResources([]platform.Channel{{ID: "1", Key: "web"}})[0].RawName, "web"},
	}
	for _, tt := range tests {
		if tt.got != tt.wantName {
			t.Errorf("%s: name %q, want %q", tt.name, tt.got, tt.wantName)
		}
	}
}

func TestResourceIDsAndTypes(t *testing.T) {
	r := taxCategoryResources([]platform.TaxCategory{{ID: "tax-1"}})[0]
	if r.InstanceState.ID != "tax-1" || r.InstanceInfo.Type != "commercetools_tax_category" {
		t.Fatalf("got %s %s", r.InstanceState.ID, r.InstanceInfo.Type)
	}
	r = typeResources([]platform.Type{{ID: "type-1", Key: "k"}})[0]
	if r.InstanceState.ID != "type-1" || r.InstanceInfo.Type != "commercetools_type" {
		t.Fatalf("got %s %s", r.InstanceState.ID, r.InstanceInfo.Type)
	}
}
