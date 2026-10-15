// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"os"
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

// providerName is the Terraform provider the generated configuration uses,
// and the provider of every listed resource.
const providerName = "azurerm"

// directoryName is the name of the provider's command, azure, which the
// roots go under (see Directory), as AWS's and Google's go under theirs.
const directoryName = "azure"

// telemetryApplicationID is how infraharvest's requests introduce
// themselves to Azure, in their User-Agent.
const telemetryApplicationID = "infraharvest"

// errNoSubscription is Init's error when nothing names the subscription to
// import.
var errNoSubscription = errors.New("no Azure subscription to import: set ARM_SUBSCRIPTION_ID, or name subscriptions with --subscriptions or --management-group")

// services makes a new lister for each service the provider lists, by
// --services name. resourceManagerServices (see resource_graph.go) says
// which of them * leaves out.
var services = map[string]func() harvest.Lister{
	resourceGraphService:                   func() harvest.Lister { return &ResourceGraphLister{} },
	"management_lock":                      func() harvest.Lister { return &ManagementLockLister{} },
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
}

// Init takes the resource group (args[0], "" for the whole subscription)
// and, optionally, the subscription (args[1]), which otherwise comes from
// ARM_SUBSCRIPTION_ID. It reads the ARM_* environment and builds the
// credential; with no subscription it fails with errNoSubscription before
// any request. A custom cloud's endpoints are read from its metadata host,
// with ctx.
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
	return nil
}

// signIn completes cfg's cloud, reading a metadata host's endpoints with
// ctx when cfg has one, and returns cfg so completed, the credential cfg
// signs in with and the options of the Resource Manager clients. It sends
// no request other than the metadata one: the credential signs in on its
// first token.
func signIn(ctx context.Context, cfg authConfig) (authConfig, azcore.TokenCredential, *arm.ClientOptions, error) {
	if cfg.metadataHost != "" {
		ctx, cancel := context.WithTimeout(ctx, signInHTTPTimeout)
		defer cancel()
		described, err := cloudFromMetadata(ctx, http.DefaultClient, cfg.metadataHost)
		if err != nil {
			return authConfig{}, nil, nil, err
		}
		cfg.cloud = described
	}
	credential, err := newCredential(cfg)
	if err != nil {
		return authConfig{}, nil, nil, err
	}
	options := &arm.ClientOptions{
		ClientOptions: policy.ClientOptions{
			Cloud:     cfg.cloud,
			Telemetry: policy.TelemetryOptions{ApplicationID: telemetryApplicationID},
			Transport: subscriptionLimiter(cfg.subscriptionID).Client(resourceManagerHTTPClient()),
		},
		AuxiliaryTenants: cfg.auxiliaryTenants,
		// The SDK otherwise answers a MissingSubscriptionRegistration
		// error by registering the resource provider: infraharvest only
		// reads, and the Reader role can't register one anyway.
		DisableRPRegistration: true,
	}
	return cfg, credential, options, nil
}

// limiters holds each subscription's *ratelimit.Limiter, by subscription
// ID: Azure throttles Resource Manager reads per subscription, so every
// service and request of a subscription shares one limit.
var limiters sync.Map

// subscriptionLimiter returns the subscription's limiter, made with
// Default's rate the first time it is asked for.
func subscriptionLimiter(subscriptionID string) *ratelimit.Limiter {
	if l, ok := limiters.Load(subscriptionID); ok {
		return l.(*ratelimit.Limiter)
	}
	l, _ := limiters.LoadOrStore(subscriptionID, ratelimit.Like(ratelimit.Default()))
	return l.(*ratelimit.Limiter)
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

// Directory returns "azure": roots go under <out>/azure/, not azurerm.
func (*Provider) Directory() string {
	return directoryName
}

// ProviderBlock returns the provider "azurerm" block: the subscription,
// the features block azurerm requires, and resource_provider_registrations
// "none". azurerm 4 otherwise registers dozens of resource providers on
// the subscription before every plan, which takes a role that can write
// to the subscription: with the Reader role infraharvest needs, every
// plan failed. Registering them isn't the import's job, and the generated
// configuration is the user's to keep, so it registers none, as azurerm 5
// does by default. The roots use azurerm 4 or 5 (4 when a module holds
// the provider back), and the argument means the same in both.
func (p *Provider) ProviderBlock() map[string]interface{} {
	return map[string]interface{}{
		"provider": map[string]interface{}{
			providerName: map[string]interface{}{
				"features":                        map[string]interface{}{},
				"resource_provider_registrations": "none",
				"subscription_id":                 p.subscriptionID,
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
		Settings: map[string]interface{}{
			"subscription_id": p.subscriptionID,
			"credential":      p.credential,
			"client_options":  p.clientOptions,
			"resource_group":  p.resourceGroup,
			"catalogue":       p.catalogue,
		},
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

// ConcurrentListing says the services of a subscription may list at once:
// each has its own lister, and what they share, the credential and the
// subscription's limiter, is safe to share.
func (*Provider) ConcurrentListing() bool {
	return true
}

// DefaultTags says azurerm resources take their tags in the tags
// attribute, and that Azure reserves the hidden- prefix for its own.
func (*Provider) DefaultTags() (attribute, block, reservedPrefix string) {
	return "tags", "", "hidden-"
}

// SetCatalogue sets the import's catalogue, which the services get.
func (p *Provider) SetCatalogue(c *catalogue.Catalogue) {
	p.catalogue = c
}

// ModuleProviders configures azapi, which curated modules may call, for the
// same subscription and, for a custom cloud, its endpoints in place of the
// public cloud's.
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
