// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"reflect"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The settings Select passes come back as they went in, the catalogue and
// what the other services listed among them, and a missing one as its
// zero value.
func TestServiceSettingsRoundTrip(t *testing.T) {
	want := serviceSettings{
		subscriptionID: "sub",
		resourceGroup:  "rg1",
		credential:     fakeCredential{},
		clientOptions:  &arm.ClientOptions{},
		catalogue:      catalogue.MustBuiltin("azurerm"),
		listed:         []harvest.Resource{harvest.RecordBare("/subscriptions/sub/resourceGroups/rg1", "rg1", "azurerm_resource_group", "azurerm")},
	}
	if got := settingsOf(want.args()); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := settingsOf(nil); !reflect.DeepEqual(got, serviceSettings{}) {
		t.Errorf("no settings: got %+v", got)
	}
}
