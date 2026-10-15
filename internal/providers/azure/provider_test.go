// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"crypto/tls"
	"errors"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The capabilities the importer looks for.
var (
	_ harvest.Provider                      = (*Provider)(nil)
	_ harvest.ProviderWithScope             = (*Provider)(nil)
	_ harvest.ProviderWithConcurrentListing = (*Provider)(nil)
	_ harvest.ProviderWithDefaultTags       = (*Provider)(nil)
	_ harvest.ProviderWithCatalogue         = (*Provider)(nil)
	_ harvest.ProviderWithModuleProviders   = (*Provider)(nil)
	_ harvest.ProviderWithTags              = (*Provider)(nil)
	_ harvest.ProviderWithOptInServices     = (*Provider)(nil)
)

// The importer's defaults apply where Provider has no capability of
// its own: no child imports, no selection defaults, no Terraform
// environment, the hashicorp/azurerm source.
func TestCapabilitiesNotImplemented(t *testing.T) {
	var p harvest.Provider = &Provider{}
	for name, has := range map[string]bool{
		"ChildImports":        implements[harvest.ProviderWithChildImports](p),
		"ExcludedByDefault":   implements[harvest.ProviderWithSelectionDefaults](p),
		"TerraformEnv":        implements[harvest.ProviderWithTerraformEnv](p),
		"RegistrySource":      implements[harvest.ProviderWithRegistrySource](p),
		"DefaultTagsArgument": implements[harvest.ProviderWithDefaultTagsArgument](p),
	} {
		if has {
			t.Errorf("Provider has %s", name)
		}
	}
}

func implements[I any](p harvest.Provider) bool {
	_, ok := p.(I)
	return ok
}

// clearARMEnvironment unsets the ARM_* variables that would pick a sign-in
// method or a cloud, so that Init signs in with the Azure CLI, which sends
// nothing until a token is needed.
func clearARMEnvironment(t *testing.T) {
	for _, name := range []string{
		"ARM_SUBSCRIPTION_ID", "ARM_TENANT_ID", "ARM_CLIENT_ID", "ARM_CLIENT_SECRET",
		"ARM_CLIENT_CERTIFICATE_PATH", "ARM_CLIENT_CERTIFICATE_PASSWORD",
		"ARM_USE_OIDC", "ARM_USE_MSI", "ARM_MSI_ENDPOINT",
		"ARM_AUXILIARY_TENANT_IDS", "ARM_ENVIRONMENT", "ARM_METADATA_HOSTNAME",
	} {
		t.Setenv(name, "")
	}
}

// Init takes the resource group from args[0] and the subscription from
// args[1], else from ARM_SUBSCRIPTION_ID.
func TestInitTakesTheScope(t *testing.T) {
	clearARMEnvironment(t)
	t.Setenv("ARM_SUBSCRIPTION_ID", "env-sub")
	for _, tc := range []struct {
		args                        []string
		subscription, resourceGroup string
	}{
		{[]string{""}, "env-sub", ""},
		{[]string{"rg1"}, "env-sub", "rg1"},
		{[]string{"rg1", ""}, "env-sub", "rg1"},
		{[]string{"", "sub-2"}, "sub-2", ""},
	} {
		p := &Provider{}
		if err := p.Init(t.Context(), tc.args); err != nil {
			t.Fatalf("%q: %v", tc.args, err)
		}
		if p.subscriptionID != tc.subscription || p.resourceGroup != tc.resourceGroup {
			t.Errorf("%q: subscription %q, resource group %q", tc.args, p.subscriptionID, p.resourceGroup)
		}
		if p.credential == nil || p.clientOptions == nil {
			t.Errorf("%q: credential %v, client options %v", tc.args, p.credential, p.clientOptions)
		}
		if p.customCloud != nil {
			t.Errorf("%q: a custom cloud for Azure's own: %+v", tc.args, p.customCloud)
		}
	}
}

// The clients go to the configured cloud, as infraharvest, for the
// auxiliary tenants too, through the subscription's limiter.
func TestSignInClientOptions(t *testing.T) {
	cfg := authConfig{subscriptionID: "sub-options", cloud: cloud.AzureChina, auxiliaryTenants: []string{"t1", "t2"}}
	got, credential, options, err := signIn(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if credential == nil {
		t.Error("no credential")
	}
	if !reflect.DeepEqual(got.cloud, cloud.AzureChina) || got.subscriptionID != "sub-options" {
		t.Errorf("config %+v", got)
	}
	if !reflect.DeepEqual(options.Cloud, cloud.AzureChina) {
		t.Errorf("cloud %+v", options.Cloud)
	}
	if options.Telemetry.ApplicationID != "infraharvest" {
		t.Errorf("telemetry application ID %q", options.Telemetry.ApplicationID)
	}
	if !slices.Equal(options.AuxiliaryTenants, []string{"t1", "t2"}) {
		t.Errorf("auxiliary tenants %v", options.AuxiliaryTenants)
	}
	if options.Transport == nil {
		t.Error("no transport")
	}
}

// The metadata request takes the caller's context, so an interrupt stops
// it. Before the fix it ran under a context of its own, which only its
// timeout ended.
func TestSignInStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cfg := authConfig{subscriptionID: "sub-cancelled", metadataHost: "management.stack.example"}
	if _, _, _, err := signIn(ctx, cfg); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want %v", err, context.Canceled)
	}
}

// Every sign-in for a subscription shares its limiter; other subscriptions
// have their own.
func TestSubscriptionLimiterIsShared(t *testing.T) {
	a := subscriptionLimiter("sub-limiter-a")
	if a == nil || subscriptionLimiter("sub-limiter-a") != a {
		t.Error("the same subscription got another limiter")
	}
	if subscriptionLimiter("sub-limiter-b") == a {
		t.Error("two subscriptions share a limiter")
	}
}

// Resource Manager requests use Go's default transport, with its proxy
// settings, and TLS 1.2 or later.
func TestResourceManagerHTTPClient(t *testing.T) {
	client := resourceManagerHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport %T", client.Transport)
	}
	if transport == http.DefaultTransport {
		t.Error("the default transport itself, not a copy")
	}
	if transport.Proxy == nil {
		t.Error("no proxy from the environment")
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Errorf("TLS config %+v", transport.TLSClientConfig)
	}
	if !transport.ForceAttemptHTTP2 {
		t.Error("no HTTP/2")
	}
}

func TestProviderBlock(t *testing.T) {
	p := &Provider{subscriptionID: "sub-1"}
	want := map[string]interface{}{
		"provider": map[string]interface{}{
			"azurerm": map[string]interface{}{
				"features":        map[string]interface{}{},
				"subscription_id": "sub-1",
			},
		},
	}
	if got := p.ProviderBlock(); !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
	if got := p.Name(); got != "azurerm" {
		t.Errorf("name %q", got)
	}
}

// Every call returns listers of its own, so concurrent services never
// share one.
func TestServicesAreFresh(t *testing.T) {
	p := &Provider{}
	first, second := p.Services(), p.Services()
	if len(first) != 11 {
		t.Errorf("%d services: %v", len(first), slices.Sorted(maps.Keys(first)))
	}
	for name, g := range first {
		if second[name] == g {
			t.Errorf("%s: the same lister twice", name)
		}
	}
}

// Select hands the service the provider's settings and makes it the
// current one; an unknown service is an error.
func TestSelect(t *testing.T) {
	cat := catalogue.MustBuiltin("azurerm")
	credential := azcore.TokenCredential(fakeCredential{})
	options := &arm.ClientOptions{}
	p := &Provider{subscriptionID: "sub", resourceGroup: "rg1", credential: credential, clientOptions: options}
	p.SetCatalogue(cat)

	if err := p.Select("virtual_network", true); err != nil {
		t.Fatal(err)
	}
	s, ok := p.Selected().(*VirtualNetworkLister)
	if !ok {
		t.Fatalf("service %T", p.Selected())
	}
	if s.Name() != "virtual_network" || !s.Verbose() || s.Provider() != "azurerm" {
		t.Errorf("name %q, verbose %v, provider %q", s.Name(), s.Verbose(), s.Provider())
	}
	want := map[string]interface{}{
		"subscription_id": "sub",
		"credential":      credential,
		"client_options":  options,
		"resource_group":  "rg1",
		"catalogue":       cat,
	}
	if got := s.Settings(); !reflect.DeepEqual(got, want) {
		t.Errorf("args %#v, want %#v", got, want)
	}

	previous := p.Selected()
	if err := p.Select("virtual_network", false); err != nil || p.Selected() == previous {
		t.Errorf("a second Select: %v, same lister %v", err, p.Selected() == previous)
	}

	err := p.Select("virtual_machine", false)
	if err == nil || err.Error() != `not a service of azure; "infraharvest services azure" lists them` {
		t.Errorf("unknown service: %v", err)
	}
}

// Scope is the subscription and the resource group, "all" for the whole
// subscription.
func TestScope(t *testing.T) {
	for resourceGroup, want := range map[string]string{"rg1": "rg1", "": "all"} {
		p := &Provider{subscriptionID: "sub", resourceGroup: resourceGroup}
		account, region, err := p.Scope(t.Context())
		if err != nil || account != "sub" || region != want {
			t.Errorf("resource group %q: got %q, %q, %v", resourceGroup, account, region, err)
		}
	}
}

func TestDefaultTagsAndConcurrency(t *testing.T) {
	p := &Provider{}
	if attribute, block, reserved := p.DefaultTags(); attribute != "tags" || block != "" || reserved != "hidden-" {
		t.Errorf("default tags %q, %q, %q", attribute, block, reserved)
	}
	if !p.ConcurrentListing() {
		t.Error("services list one at a time")
	}
}
