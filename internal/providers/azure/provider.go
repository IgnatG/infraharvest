// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

// The capabilities Provider has, checked when it builds: one whose method
// no longer matches its interface would otherwise be dropped in silence,
// as the importer asserts them at run time.
var (
	_ harvest.ProviderWithListedByOthers  = (*Provider)(nil)
	_ harvest.ProviderWithModuleProviders = (*Provider)(nil)
	_ harvest.ProviderWithPartialTypes    = (*Provider)(nil)
	_ harvest.ProviderWithScope           = (*Provider)(nil)
	_ harvest.ProviderWithCatalogue       = (*Provider)(nil)
	_ harvest.ProviderWithUnwantedTypes   = (*Provider)(nil)
)

// providerName is the Terraform provider the generated configuration uses,
// and the provider of every listed resource.
const providerName = "azurerm"

// commandName is the name of the provider's command, azure, which the
// roots go under (see Profile), as AWS's and Google's go under theirs.
const commandName = "azure"

// telemetryApplicationID is how infraharvest's requests introduce
// themselves to Azure, in their User-Agent.
const telemetryApplicationID = "infraharvest"

// errNoSubscription is Init's error when nothing names the subscription to
// import.
var errNoSubscription = errors.New("no Azure subscription to import: set ARM_SUBSCRIPTION_ID, or name subscriptions with --subscriptions or --management-group")

// services makes a new lister for each service the provider lists, by
// --services name. Its profile's OptInServices says which of them *
// leaves out.
var services = map[string]func() harvest.Lister{
	resourceGraphService:                   func() harvest.Lister { return &ResourceGraphLister{} },
	diagnosticSettingService:               func() harvest.Lister { return &DiagnosticSettingLister{} },
	"management_lock":                      func() harvest.Lister { return &ManagementLockLister{} },
	"role_assignment":                      func() harvest.Lister { return &RoleAssignmentLister{} },
	"security_center_contact":              func() harvest.Lister { return &SecurityCenterContactLister{} },
	"security_center_subscription_pricing": func() harvest.Lister { return &SecurityCenterSubscriptionPricingLister{} },
	"managed_identity":                     func() harvest.Lister { return &ManagedIdentityLister{} },
	"network_interface":                    func() harvest.Lister { return &NetworkInterfaceLister{} },
	"network_security_group":               func() harvest.Lister { return &NetworkSecurityGroupLister{} },
	"public_ip":                            func() harvest.Lister { return &PublicIPLister{} },
	"resource_group":                       func() harvest.Lister { return &ResourceGroupLister{} },
	"subnet":                               func() harvest.Lister { return &SubnetLister{} },
	"virtual_network":                      func() harvest.Lister { return &VirtualNetworkLister{} },
}

// Provider lists one Azure subscription, or one resource group in it
// (see Init). The zero value is ready for Init.
type Provider struct {
	harvest.ProviderBase

	// subscriptionID is the subscription to list.
	subscriptionID string
	// resourceGroup is --resource-group, "" for the whole subscription.
	resourceGroup string
	// credential signs every request in.
	credential azcore.TokenCredential
	// clientOptions are the options of every Resource Manager client: the
	// cloud, the telemetry ID, the auxiliary tenants and the subscription's
	// rate-limited transport.
	clientOptions *arm.ClientOptions
	// customCloud is the cloud a metadata host described
	// (ARM_ENVIRONMENT=stack), nil for Azure's own clouds.
	customCloud *cloud.Configuration
	// catalogue is the import's catalogue (see SetCatalogue), nil for the
	// built-in one.
	catalogue *catalogue.Catalogue
	// listed is what the import's other services listed (see
	// SetListedByOthers), for the services that list from it.
	listed []harvest.Resource
	// azapi is --azapi (Init's args[2]): resource_graph imports what
	// azurerm has no type for as azapi_resource (see azapiCandidates).
	azapi bool
	// unwanted reports the types the import leaves out whatever is listed
	// (see SetUnwantedTypes); nil wants every type.
	unwanted func(typ string) bool
}

// Init takes the resource group (args[0], "" for the whole subscription),
// optionally the subscription (args[1]), which otherwise comes from
// ARM_SUBSCRIPTION_ID, and the provider type (args[2]): azapi for
// --azapi, else "". It reads the ARM_* environment and builds the
// credential; with no subscription it fails with errNoSubscription before
// any request. A custom cloud's endpoints are read from its metadata host,
// with ctx, and the requests are limited at the rate of the run's limiter
// in ctx (see ratelimit.FromContext).
func (p *Provider) Init(ctx context.Context, args []string) error {
	cfg, err := loadAuthConfig(os.Getenv)
	if err != nil {
		return err
	}
	if len(args) > 1 && args[1] != "" {
		cfg.subscriptionID = args[1]
	}
	if cfg.subscriptionID == "" {
		return errNoSubscription
	}
	cfg, credential, options, err := signIn(ctx, cfg)
	if err != nil {
		return err
	}
	p.subscriptionID = cfg.subscriptionID
	p.credential = credential
	p.clientOptions = options
	p.customCloud = nil
	if cfg.metadataHost != "" {
		described := cfg.cloud
		p.customCloud = &described
	}
	p.resourceGroup = ""
	if len(args) > 0 {
		p.resourceGroup = args[0]
	}
	p.azapi = len(args) > 2 && args[2] == azapiProviderType
	return nil
}

// signIn completes cfg's cloud, reading a metadata host's endpoints with
// ctx when cfg has one, and returns cfg so completed, the credential cfg
// signs in with and the options of the Resource Manager clients, whose
// requests the limiters of ctx's run limit (see limitedClient). It sends
// no request other than the metadata one: the credential signs in on its
// first token. Every sign-in of the run with the same configuration, for
// whichever subscription, shares one credential, and so its tokens, and
// reads the metadata host once (see signIns).
func signIn(ctx context.Context, cfg authConfig) (authConfig, azcore.TokenCredential, *arm.ClientOptions, error) {
	session, err := signInOnce(ctx, cfg)
	if err != nil {
		return authConfig{}, nil, nil, err
	}
	cfg.cloud = session.cloud
	options := &arm.ClientOptions{
		ClientOptions: policy.ClientOptions{
			Cloud:     cfg.cloud,
			Telemetry: policy.TelemetryOptions{ApplicationID: telemetryApplicationID},
			Transport: limitedClient(ratelimit.FromContext(ctx), cfg.subscriptionID, resourceManagerHTTPClient()),
		},
		AuxiliaryTenants: cfg.auxiliaryTenants,
		// The SDK otherwise answers a MissingSubscriptionRegistration
		// error by registering the resource provider: infraharvest only
		// reads, and the Reader role can't register one anyway.
		DisableRPRegistration: true,
	}
	return cfg, session.credential, options, nil
}

// signedIn is what signing in with one configuration made: the cloud,
// completed from a metadata host when there is one, and the credential.
type signedIn struct {
	cloud      cloud.Configuration
	credential azcore.TokenCredential
}

// signIns holds the signedIn of each configuration signed in with, by
// signInKey. Every service of an import initialises a provider of its own,
// and every subscription of a run signs in again, all with the same
// configuration: without it each would get a credential of its own, and
// fetch its own token from the Azure CLI, Entra or the managed identity
// endpoint on its first request.
var signIns sync.Map

// signInOnce returns the signedIn kept for cfg's configuration, or signs
// in and keeps it; a failure isn't kept.
func signInOnce(ctx context.Context, cfg authConfig) (signedIn, error) {
	key := signInKey(cfg)
	if kept, ok := signIns.Load(key); ok {
		return kept.(signedIn), nil
	}
	if cfg.metadataHost != "" {
		ctx, cancel := context.WithTimeout(ctx, signInHTTPTimeout)
		defer cancel()
		described, err := cloudFromMetadata(ctx, http.DefaultClient, cfg.metadataHost)
		if err != nil {
			return signedIn{}, err
		}
		cfg.cloud = described
	}
	credential, err := newCredential(cfg)
	if err != nil {
		return signedIn{}, err
	}
	kept, _ := signIns.LoadOrStore(key, signedIn{cloud: cfg.cloud, credential: credential})
	return kept.(signedIn), nil
}

// signInKey is a digest of everything in cfg but the subscription, which
// the credential doesn't depend on. It is a digest so that the secrets cfg
// holds aren't kept a second time as a map key.
func signInKey(cfg authConfig) string {
	cfg.subscriptionID = ""
	sum := sha256.Sum256([]byte(fmt.Sprintf("%#v", cfg)))
	return hex.EncodeToString(sum[:])
}

// runLimiters are the limiters of one run's requests, kept with the run's
// limiter (see limitersOf), so that another run, with a rate of its own,
// has its own.
type runLimiters struct {
	// rate is the run's limiter, whose rate each subscription gets.
	rate *ratelimit.Limiter
	// graph is the limiter of every Resource Graph query, of graphRate, or
	// the run's rate if that is lower. Azure throttles Resource Graph per
	// user, not per subscription, so the queries of every subscription of
	// a run share one limit.
	graph *ratelimit.Limiter
	// mu guards subscriptions.
	mu sync.Mutex
	// subscriptions holds each subscription's limiter, by subscription ID:
	// Azure throttles Resource Manager reads per subscription, so every
	// service and Resource Manager request of a subscription shares one
	// limit.
	subscriptions map[string]*ratelimit.Limiter
}

// runLimitersKey is the key of a run's limiters kept with its limiter.
type runLimitersKey struct{}

// limitersOf returns the limiters of the run whose limiter is run, made
// the first time it is asked for.
func limitersOf(run *ratelimit.Limiter) *runLimiters {
	return ratelimit.Kept(run, runLimitersKey{}, func() *runLimiters {
		return &runLimiters{
			rate:          run,
			graph:         ratelimit.New(min(graphRate, run.Rate())),
			subscriptions: map[string]*ratelimit.Limiter{},
		}
	})
}

// subscription returns the subscription's limiter, made with the run's
// rate the first time it is asked for.
func (r *runLimiters) subscription(subscriptionID string) *ratelimit.Limiter {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.subscriptions[subscriptionID]
	if !ok {
		l = ratelimit.Like(r.rate)
		r.subscriptions[subscriptionID] = l
	}
	return l
}

// graphRate is how many Resource Graph queries a second a run sends at
// most: Azure allows a user 15 every 5 seconds.
const graphRate = 3.0

// graphPathPrefix is how the path of every Resource Graph request begins,
// in lower case.
const graphPathPrefix = "/providers/microsoft.resourcegraph/"

// limitedClient sends each request through base once its limit in the
// run whose limiter is run allows: a Resource Graph query the run's graph
// limiter's, any other request the subscription's (see runLimiters).
func limitedClient(run *ratelimit.Limiter, subscriptionID string, base ratelimit.Doer) ratelimit.Doer {
	limiters := limitersOf(run)
	return &limitedDoer{
		resourceManager: limiters.subscription(subscriptionID).Client(base),
		graph:           limiters.graph.Client(base),
	}
}

// limitedDoer is limitedClient's Doer.
type limitedDoer struct {
	resourceManager, graph ratelimit.Doer
}

func (d *limitedDoer) Do(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(strings.ToLower(req.URL.Path), graphPathPrefix) {
		return d.graph.Do(req)
	}
	return d.resourceManager.Do(req)
}

// resourceManagerHTTPClient is the HTTP client Resource Manager requests
// go through: Go's default transport, with its proxy settings from the
// environment and HTTP/2, that takes TLS 1.2 or later only.
func resourceManagerHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	return &http.Client{Transport: transport}
}

// Name returns "azurerm".
func (*Provider) Name() string {
	return providerName
}

// ProviderBlock returns the provider "azurerm" block: the subscription and
// the features block azurerm requires, empty. The roots pin azurerm's
// newest major release, 5 or later (a module that holds it to an older
// one isn't called), whose defaults suit an import with the Reader role,
// so the block sets nothing else:
//
//   - azurerm 5 registers no resource providers on the subscription by
//     default (resource_provider_registrations defaults to "none"), which
//     a Reader can't: azurerm 4 did before every plan unless told not to;
//   - azurerm 5 has dropped a storage account's queue_properties and
//     static_website blocks, for the separate azurerm_storage_account_*
//     resources, so it reads the account through Resource Manager alone
//     and copes with being refused its keys: features.storage's
//     data_plane_available, which azurerm 4 needed to do the same, no
//     longer changes how the account is read.
func (p *Provider) ProviderBlock() map[string]interface{} {
	return map[string]interface{}{
		"provider": map[string]interface{}{
			providerName: map[string]interface{}{
				"features":        map[string]interface{}{},
				"subscription_id": p.subscriptionID,
			},
		},
	}
}

// Services returns a new lister for every service.
func (*Provider) Services() map[string]harvest.Lister {
	supported := make(map[string]harvest.Lister, len(services))
	for name, newLister := range services {
		supported[name] = newLister()
	}
	return supported
}

// Select makes the service called name the provider's current one,
// with a new lister, and passes it the provider's settings.
func (p *Provider) Select(name string, verbose bool) error {
	newLister, ok := services[name]
	if !ok {
		return errors.New("not a service of azure; \"infraharvest services azure\" lists them")
	}
	service := newLister()
	p.Current = service
	service.Configure(harvest.ListerConfig{
		Service:  name,
		Provider: providerName,
		Verbose:  verbose,
		Settings: serviceSettings{
			subscriptionID: p.subscriptionID,
			resourceGroup:  p.resourceGroup,
			credential:     p.credential,
			clientOptions:  p.clientOptions,
			catalogue:      p.catalogue,
			listed:         p.listed,
			azapi:          p.azapi,
			unwanted:       p.unwanted,
		}.args(),
	})
	return nil
}

// Scope is the subscription and the resource group, "all" for the whole
// subscription. It sends no request.
func (p *Provider) Scope(context.Context) (account, region string, err error) {
	region = p.resourceGroup
	if region == "" {
		region = "all"
	}
	return p.subscriptionID, region, nil
}

// Profile describes Azure: an import covers a subscription, whose ID is
// a GUID, in either case; * leaves out the services that list what
// resource_graph lists too (see resourceManagerServices), and
// diagnostic_setting, which takes a request per resource (see
// DiagnosticSettingLister) and lists after the others, asking only the
// resources they found for their diagnostic settings; Azure Verified
// Modules are its curated modules; roots go under <out>/azure/, not
// azurerm; the services of a subscription may list at once, as each has
// its own lister and what they share, the credential and the
// subscription's limiter, is safe to share; azurerm resources take their
// tags in the tags attribute, with no provider block to apply them to
// all, and Azure reserves the hidden- prefix for its own; Resource
// Graph's types go in files by their family; and a root that
// environments share reads the subscription from the provider.
func (*Provider) Profile() harvest.Profile {
	return harvest.Profile{
		Command: commandName,
		Scope: harvest.Scope{
			Noun:     "subscription",
			Field:    "subscriptions",
			FoldCase: true,
			Flags:    []string{"subscriptions", "management-group", "resource-group", "azapi"},
			Describe: describeScope,
		},
		Modules:           commandName,
		ModulesName:       "Azure Verified Modules",
		OptInServices:     append(slices.Clone(resourceManagerServices), diagnosticSettingService),
		DependentServices: []string{diagnosticSettingService},
		ConcurrentListing: true,
		DefaultTags:       &harvest.DefaultTags{Attribute: "tags", ReservedPrefix: "hidden-"},
		Layout: harvest.Layout{
			NetworkServices: []string{
				"network_interface", "network_security_group", "public_ip", "subnet", "virtual_network",
			},
			NetworkTypes: []string{
				"application_security_group", "nat_gateway", "network", "private_dns", "private_endpoint",
				"public_ip", "route", "subnet", "virtual_network",
			},
			TypeFamilies: map[string]string{
				"cosmosdb": "cosmosdb", "dns": "dns", "key_vault": "key_vault", "kubernetes_cluster": "kubernetes_cluster",
				"lb": "lb", "linux_virtual_machine": "virtual_machine", "log_analytics": "log_analytics", "monitor": "monitor",
				"mssql": "mssql", "mysql": "mysql", "postgresql": "postgresql", "storage": "storage",
				"virtual_machine": "virtual_machine", "windows_virtual_machine": "virtual_machine",
			},
		},
		Merge: harvest.Merge{
			ScopeData:  "azurerm_client_config.current",
			ScopeLocal: "subscription_id",
			ScopeExpr:  "data.azurerm_client_config.current.subscription_id",
		},
	}
}

// describeScope says what the azure command reads with flags: the
// subscription of ARM_SUBSCRIPTION_ID, or those of --subscriptions and
// under --management-group, the resource group of --resource-group in
// them, and with --azapi what azurerm has no type for.
func describeScope(flags map[string][]string) string {
	var parts []string
	if subscriptions := flags["subscriptions"]; len(subscriptions) > 0 {
		parts = append(parts, "subscriptions "+strings.Join(subscriptions, ", "))
	}
	if group := flags["management-group"]; len(group) > 0 {
		parts = append(parts, "every enabled subscription under management group "+group[0])
	}
	if len(parts) == 0 {
		parts = append(parts, "subscription of ARM_SUBSCRIPTION_ID")
	}
	text := "the azure " + strings.Join(parts, " and ")
	if group := flags["resource-group"]; len(group) > 0 {
		text = "resource group " + group[0] + " of " + text
	}
	if len(flags["azapi"]) > 0 {
		text += ", what azurerm has no type for through azapi"
	}
	return text
}

// SetUnwantedTypes takes what reports the types the import leaves out
// whatever is listed, which the services get: they skip the calls that
// list only those (see Service.wants).
func (p *Provider) SetUnwantedTypes(unwanted func(typ string) bool) {
	p.unwanted = unwanted
}

// SetCatalogue sets the import's catalogue, which the services get.
func (p *Provider) SetCatalogue(c *catalogue.Catalogue) {
	p.catalogue = c
}

// SetListedByOthers sets what the import's other services listed, which
// the services get.
func (p *Provider) SetListedByOthers(resources []harvest.Resource) {
	p.listed = resources
}

// ModuleProviders configures azapi, which curated modules may call, and
// which the azapi_resource resources of --azapi come through, for the
// same subscription and, for a custom cloud, its endpoints in place of the
// public cloud's. A root configures it only where one of them is in it.
func (p *Provider) ModuleProviders() map[string]harvest.ModuleProvider {
	config := map[string]interface{}{"subscription_id": p.subscriptionID}
	if c := p.customCloud; c != nil {
		resourceManager := c.Services[cloud.ResourceManager]
		config["environment"] = "public"
		config["endpoint"] = cty.TupleVal([]cty.Value{cty.ObjectVal(map[string]cty.Value{
			"active_directory_authority_host": cty.StringVal(c.ActiveDirectoryAuthorityHost),
			"resource_manager_endpoint":       cty.StringVal(resourceManager.Endpoint),
			"resource_manager_audience":       cty.StringVal(resourceManager.Audience),
		})})
	}
	return map[string]harvest.ModuleProvider{
		"azapi": {Source: "Azure/azapi", Config: config},
	}
}
