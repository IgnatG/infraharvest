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
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

// The capabilities the importer looks for.
var (
	_ harvest.Provider                    = (*Provider)(nil)
	_ harvest.ProviderWithScope           = (*Provider)(nil)
	_ harvest.ProviderWithCatalogue       = (*Provider)(nil)
	_ harvest.ProviderWithModuleProviders = (*Provider)(nil)
	_ harvest.ProviderWithListedByOthers  = (*Provider)(nil)
	_ harvest.ProviderWithPartialTypes    = (*Provider)(nil)
)

// The importer's defaults apply where Provider has no capability of
// its own: no child imports, no selection defaults, no Terraform
// environment, the hashicorp/azurerm source.
func TestCapabilitiesNotImplemented(t *testing.T) {
	t.Parallel()
	var p harvest.Provider = &Provider{}
	for name, has := range map[string]bool{
		"ChildImports":      implements[harvest.ProviderWithChildImports](p),
		"ExcludedByDefault": implements[harvest.ProviderWithSelectionDefaults](p),
		"TerraformEnv":      implements[harvest.ProviderWithTerraformEnv](p),
		// The listers record the tags themselves, from their answers.
		"Tags": implements[harvest.ProviderWithTags](p),
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
		"ARM_CLIENT_ID_FILE_PATH", "ARM_CLIENT_SECRET_FILE_PATH", "ARM_CLIENT_CERTIFICATE",
		"ARM_USE_OIDC", "ARM_OIDC_TOKEN", "ARM_OIDC_TOKEN_FILE_PATH", "ARM_USE_AKS_WORKLOAD_IDENTITY",
		"ARM_ADO_PIPELINE_SERVICE_CONNECTION_ID", "ARM_OIDC_AZURE_SERVICE_CONNECTION_ID",
		"ARM_USE_MSI", "ARM_MSI_ENDPOINT", "ARM_MSI_API_VERSION", "ARM_USE_CLI",
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
		azapi                       bool
	}{
		{[]string{""}, "env-sub", "", false},
		{[]string{"rg1"}, "env-sub", "rg1", false},
		{[]string{"rg1", ""}, "env-sub", "rg1", false},
		{[]string{"", "sub-2"}, "sub-2", "", false},
		{[]string{"", "", "azapi"}, "env-sub", "", true},
		{[]string{"rg1", "sub-2", ""}, "sub-2", "rg1", false},
	} {
		p := &Provider{}
		if err := p.Init(t.Context(), tc.args); err != nil {
			t.Fatalf("%q: %v", tc.args, err)
		}
		if p.subscriptionID != tc.subscription || p.resourceGroup != tc.resourceGroup || p.azapi != tc.azapi {
			t.Errorf("%q: subscription %q, resource group %q, azapi %v", tc.args, p.subscriptionID, p.resourceGroup, p.azapi)
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
	t.Parallel()
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
	// infraharvest only reads: the SDK mustn't register resource providers.
	if !options.DisableRPRegistration {
		t.Error("resource provider registration left on")
	}
}

// The metadata request takes the caller's context, so an interrupt stops
// it. Before the fix it ran under a context of its own, which only its
// timeout ended.
func TestSignInStopsWithItsContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cfg := authConfig{subscriptionID: "sub-cancelled", metadataHost: "management.stack.example"}
	if _, _, _, err := signIn(ctx, cfg); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want %v", err, context.Canceled)
	}
}

// Sign-ins with the same configuration share one credential, and so its
// tokens, whichever subscription they are for; another configuration gets
// a credential of its own.
func TestSignInSharesTheCredential(t *testing.T) {
	t.Parallel()
	sign := func(cfg authConfig) azcore.TokenCredential {
		t.Helper()
		_, credential, _, err := signIn(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		return credential
	}
	first := sign(authConfig{subscriptionID: "sub-shared-a", tenantID: "tenant-shared"})
	again := sign(authConfig{subscriptionID: "sub-shared-a", tenantID: "tenant-shared"})
	other := sign(authConfig{subscriptionID: "sub-shared-b", tenantID: "tenant-shared"})
	elsewhere := sign(authConfig{subscriptionID: "sub-shared-a", tenantID: "tenant-elsewhere"})

	if again != first || other != first {
		t.Error("the same configuration got another credential")
	}
	if elsewhere == first {
		t.Error("another tenant got the same credential")
	}
}

// Every sign-in of a run for a subscription shares its limiter, of the
// run's rate; other subscriptions have their own.
func TestSubscriptionLimiterIsShared(t *testing.T) {
	t.Parallel()
	run := limitersOf(ratelimit.New(7))
	a := run.subscription("sub-limiter-a")
	if a == nil || run.subscription("sub-limiter-a") != a {
		t.Error("the same subscription got another limiter")
	}
	if run.subscription("sub-limiter-b") == a {
		t.Error("two subscriptions share a limiter")
	}
	if a.Rate() != 7 {
		t.Errorf("the subscription's rate is %v, want the run's 7", a.Rate())
	}
}

// Two runs in one process, at once, sign in for the same subscription
// with their own rates: each has its own subscription and Resource Graph
// limiters, at its own rate, the graph's no more than graphRate, and the
// requests of one never wait for the other's. Run with -race.
func TestRunsKeepTheirOwnLimits(t *testing.T) {
	t.Parallel()
	type run struct {
		limiter     *ratelimit.Limiter
		graph, subs float64
		waits       bool
		client      ratelimit.Doer
	}
	runs := []*run{
		{limiter: ratelimit.New(1), graph: 1, subs: 1, waits: true},
		{limiter: ratelimit.New(1000), graph: graphRate, subs: 1000},
	}
	var wg sync.WaitGroup
	for _, r := range runs {
		wg.Go(func() {
			ctx := ratelimit.NewContext(t.Context(), r.limiter)
			if _, _, options, err := signIn(ctx, authConfig{subscriptionID: "sub-runs", tenantID: "tenant-runs"}); err != nil || options.Transport == nil {
				t.Errorf("sign-in: %v", err)
			}
			r.client = limitedClient(r.limiter, "sub-runs", &recordingDoer{})
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	for _, r := range runs {
		limiters := limitersOf(r.limiter)
		if got := limiters.graph.Rate(); got != r.graph {
			t.Errorf("run of %v: Resource Graph rate %v, want %v", r.subs, got, r.graph)
		}
		if got := limiters.subscription("sub-runs").Rate(); got != r.subs {
			t.Errorf("run of %v: subscription rate %v", r.subs, got)
		}
		// The second request waits past its deadline only at 1 a second.
		var waited bool
		for i := range 2 {
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://management.azure.com/subscriptions/sub-runs/resourceGroups", http.NoBody)
			_, err := r.client.Do(req)
			cancel()
			if err != nil {
				waited = i == 1
				break
			}
		}
		if waited != r.waits {
			t.Errorf("run of %v: the second request waited: %t, want %t", r.subs, waited, r.waits)
		}
	}
	if limitersOf(runs[0].limiter) == limitersOf(runs[1].limiter) {
		t.Error("the runs share their limiters")
	}
}

// Resource Manager requests use Go's default transport, with its proxy
// settings, and TLS 1.2 or later.
func TestResourceManagerHTTPClient(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	p := &Provider{subscriptionID: "sub-1"}
	// Nothing but the subscription and the features block: azurerm 5
	// registers no resource providers by default, which the Reader role
	// can't, and reads storage accounts without their keys, so neither
	// resource_provider_registrations = "none" nor storage's
	// data_plane_available = false, which azurerm 4 needed, is written.
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
	// The roots go under the command's name; the provider stays azurerm.
	if got := p.Profile().Command; got != "azure" {
		t.Errorf("directory %q", got)
	}
}

// Every call returns listers of its own, so concurrent services never
// share one: those that keep what they list.
func TestServicesAreFresh(t *testing.T) {
	t.Parallel()
	p := &Provider{}
	first, second := p.Services(), p.Services()
	if len(first) != 14 {
		t.Errorf("%d services: %v", len(first), slices.Sorted(maps.Keys(first)))
	}
	for name, g := range first {
		if name == harvest.QueryService {
			// It keeps nothing, so a lister of its own is the same as another.
			continue
		}
		if second[name] == g {
			t.Errorf("%s: the same lister twice", name)
		}
	}
}

// Lister hands the service the provider's settings, in a lister of its own
// each time; an unknown service is an error.
func TestLister(t *testing.T) {
	t.Parallel()
	cat := catalogue.MustBuiltin("azurerm")
	credential := azcore.TokenCredential(fakeCredential{})
	options := &arm.ClientOptions{}
	p := &Provider{subscriptionID: "sub", resourceGroup: "rg1", credential: credential, clientOptions: options}
	p.SetCatalogue(cat)
	listed := []harvest.Resource{harvest.RecordBare("/subscriptions/sub/resourceGroups/rg1", "rg1", "azurerm_resource_group", "azurerm")}
	p.SetListedByOthers(listed)

	lister, err := p.Lister("virtual_network")
	if err != nil {
		t.Fatal(err)
	}
	s, ok := lister.(*VirtualNetworkLister)
	if !ok {
		t.Fatalf("service %T", lister)
	}
	want := serviceSettings{
		subscriptionID: "sub",
		credential:     credential,
		clientOptions:  options,
		resourceGroup:  "rg1",
		catalogue:      cat,
		listed:         listed,
	}
	if got := s.Settings(); !reflect.DeepEqual(got, want) {
		t.Errorf("settings %#v, want %#v", got, want)
	}

	again, err := p.Lister("virtual_network")
	if err != nil || again == lister {
		t.Errorf("a second Lister: %v, same lister %v", err, again == lister)
	}

	_, err = p.Lister("virtual_machine")
	if err == nil || err.Error() != `not a service of azure; "infraharvest services azure" lists them` {
		t.Errorf("unknown service: %v", err)
	}
}

// Scope is the subscription and the resource group, "all" for the whole
// subscription.
func TestScope(t *testing.T) {
	t.Parallel()
	for resourceGroup, want := range map[string]string{"rg1": "rg1", "": "all"} {
		p := &Provider{subscriptionID: "sub", resourceGroup: resourceGroup}
		account, region, err := p.Scope(t.Context())
		if err != nil || account != "sub" || region != want {
			t.Errorf("resource group %q: got %q, %q, %v", resourceGroup, account, region, err)
		}
	}
}

func TestDefaultTagsAndConcurrency(t *testing.T) {
	t.Parallel()
	profile := (&Provider{}).Profile()
	if got, want := profile.DefaultTags, (&harvest.DefaultTags{Attribute: "tags", ReservedPrefix: "hidden-"}); !reflect.DeepEqual(got, want) {
		t.Errorf("default tags %+v, want %+v", got, want)
	}
	if !profile.ConcurrentListing {
		t.Error("services list one at a time")
	}
}

// recordingDoer records that a request reached it.
type recordingDoer struct{ got []string }

func (d *recordingDoer) Do(req *http.Request) (*http.Response, error) {
	d.got = append(d.got, req.URL.Path)
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
}

// Resource Graph queries share one limiter across subscriptions, as Azure
// throttles them per user; every other request keeps its subscription's.
func TestLimitedClientSharesTheGraphLimiter(t *testing.T) {
	t.Parallel()
	run := ratelimit.New(ratelimit.DefaultRate)
	a := limitedClient(run, "sub-graph-a", nil).(*limitedDoer)
	b := limitedClient(run, "sub-graph-b", nil).(*limitedDoer)
	limiters := limitersOf(run)
	if graph := limiters.graph; graph == nil || limitersOf(run).graph != graph {
		t.Fatal("Resource Graph queries got more than one limiter")
	}
	if got, want := limiters.graph.Rate(), min(graphRate, run.Rate()); got != want {
		t.Errorf("Resource Graph queries are limited to %v a second, want %v", got, want)
	}
	if limiters.graph == limiters.subscription("sub-graph-a") || limiters.subscription("sub-graph-a") == limiters.subscription("sub-graph-b") {
		t.Error("Resource Manager requests don't keep their subscription's limiter")
	}
	if a == b {
		t.Fatal("two subscriptions share a client")
	}

	resourceManager, graph := &recordingDoer{}, &recordingDoer{}
	a.resourceManager, a.graph = resourceManager, graph
	for _, path := range []string{
		"/providers/Microsoft.ResourceGraph/resources",
		"/subscriptions/sub-graph-a/resourceGroups",
		"/providers/microsoft.resourcegraph/resources",
		"/subscriptions/sub-graph-a/providers/Microsoft.Network/virtualNetworks",
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://management.azure.com"+path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Do(req); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"/providers/Microsoft.ResourceGraph/resources", "/providers/microsoft.resourcegraph/resources"}; !slices.Equal(graph.got, want) {
		t.Errorf("through the Resource Graph limiter: %v, want %v", graph.got, want)
	}
	if want := []string{"/subscriptions/sub-graph-a/resourceGroups", "/subscriptions/sub-graph-a/providers/Microsoft.Network/virtualNetworks"}; !slices.Equal(resourceManager.got, want) {
		t.Errorf("through the subscription's limiter: %v, want %v", resourceManager.got, want)
	}
}

// What Terraform's list resources return is in a --resource-group import
// when its identity names the group, in either case, which the list block
// narrows to; the subscription's import takes it all.
func TestInQueryScope(t *testing.T) {
	t.Parallel()
	inGroup := map[string]string{"name": "vault", "resource_group_name": "APP", "subscription_id": "s"}
	other := map[string]string{"name": "vault", "resource_group_name": "data", "subscription_id": "s"}
	all := &Provider{}
	if !all.InQueryScope(inGroup) || !all.InQueryScope(other) || all.QueryConfig() != nil {
		t.Error("the subscription's import doesn't take everything")
	}
	group := &Provider{resourceGroup: "app"}
	if !group.InQueryScope(inGroup) || group.InQueryScope(other) {
		t.Error("the resource group's import doesn't take only its own")
	}
	if got := group.QueryConfig(); !maps.Equal(got, map[string]string{"resource_group_name": "app"}) {
		t.Errorf("config %v", got)
	}
}
