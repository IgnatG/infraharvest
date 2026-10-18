// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"

	"github.com/infraharvest/infraharvest-cli/internal/catalogue"
)

// The settings Select passes come back as they went in, the catalogue
// among them, and a missing one as its zero value.
func TestServiceSettingsRoundTrip(t *testing.T) {
	want := serviceSettings{
		subscriptionID: "sub",
		resourceGroup:  "rg1",
		credential:     fakeCredential{},
		clientOptions:  &arm.ClientOptions{},
		catalogue:      catalogue.MustBuiltin("azurerm"),
	}
	if got := settingsOf(want.args()); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := settingsOf(nil); got != (serviceSettings{}) {
		t.Errorf("no settings: got %+v", got)
	}
}
