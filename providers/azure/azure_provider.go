// Copyright 2019 The Terraformer Authors.
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

package azure

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"github.com/IgnatG/infraharvest/terraformutils"
)

type AzureProvider struct { //nolint
	terraformutils.Provider
	subscriptionID string
	credential     azcore.TokenCredential
	clientOptions  *arm.ClientOptions
	resourceGroup  string
}

// errNoSubscription says which subscription to import is missing.
var errNoSubscription = errors.New("set ARM_SUBSCRIPTION_ID env var, or use --subscriptions or --management-group")

// Init signs in and takes the resource group (args[0], "" for all) and the
// subscription (args[1], else ARM_SUBSCRIPTION_ID) to import.
func (p *AzureProvider) Init(args []string) error {
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
	cfg, credential, options, err := signIn(cfg)
	if err != nil {
		return err
	}
	p.subscriptionID = cfg.subscriptionID
	p.credential = credential
	p.clientOptions = options
	p.resourceGroup = args[0]
	return nil
}

// signIn signs in with cfg, reading a custom cloud's endpoints first.
func signIn(cfg authConfig) (authConfig, azcore.TokenCredential, *arm.ClientOptions, error) {
	var err error
	if cfg.metadataHost != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if cfg.cloud, err = cloudFromMetadata(ctx, http.DefaultClient, cfg.metadataHost); err != nil {
			return authConfig{}, nil, nil, err
		}
	}
	credential, err := newCredential(cfg)
	if err != nil {
		return authConfig{}, nil, nil, err
	}
	return cfg, credential, &arm.ClientOptions{
		ClientOptions: policy.ClientOptions{
			Cloud:     cfg.cloud,
			Telemetry: policy.TelemetryOptions{ApplicationID: "infraharvest"},
		},
		AuxiliaryTenants: cfg.auxiliaryTenants,
	}, nil
}

func (p *AzureProvider) GetName() string {
	return "azurerm"
}

// GetProviderData returns the azurerm provider block, for the subscription
// imported. The engine pins the provider version in versions.tf; azurerm
// requires an (empty) features block.
func (p *AzureProvider) GetProviderData(_ ...string) map[string]interface{} {
	return map[string]interface{}{
		"provider": map[string]interface{}{
			"azurerm": map[string]interface{}{
				"features":        map[string]interface{}{},
				"subscription_id": p.subscriptionID,
			},
		},
	}
}

func (p *AzureProvider) GetSupportedService() map[string]terraformutils.ServiceGenerator {
	return map[string]terraformutils.ServiceGenerator{
		"analysis":                             &AnalysisGenerator{},
		"app_service":                          &AppServiceGenerator{},
		"application_gateway":                  &ApplicationGatewayGenerator{},
		"cosmosdb":                             &CosmosDBGenerator{},
		"container":                            &ContainerGenerator{},
		"database":                             &DatabasesGenerator{},
		"databricks":                           &DatabricksGenerator{},
		"data_factory":                         &DataFactoryGenerator{},
		"disk":                                 &DiskGenerator{},
		"dns":                                  &DNSGenerator{},
		"eventhub":                             &EventHubGenerator{},
		"keyvault":                             &KeyVaultGenerator{},
		"load_balancer":                        &LoadBalancerGenerator{},
		"management_lock":                      &ManagementLockGenerator{},
		"network_interface":                    &NetworkInterfaceGenerator{},
		"network_security_group":               &NetworkSecurityGroupGenerator{},
		"network_watcher":                      &NetworkWatcherGenerator{},
		"private_dns":                          &PrivateDNSGenerator{},
		"private_endpoint":                     &PrivateEndpointGenerator{},
		"public_ip":                            &PublicIPGenerator{},
		"purview":                              &PurviewGenerator{},
		"redis":                                &RedisGenerator{},
		"resource_group":                       &ResourceGroupGenerator{},
		"resource_graph":                       &ResourceGraphGenerator{},
		"route_table":                          &RouteTableGenerator{},
		"scaleset":                             &ScaleSetGenerator{},
		"security_center_contact":              &SecurityCenterContactGenerator{},
		"security_center_subscription_pricing": &SecurityCenterSubscriptionPricingGenerator{},
		"ssh_public_key":                       &SSHPublicKeyGenerator{},
		"storage_account":                      &StorageAccountGenerator{},
		"storage_blob":                         &StorageBlobGenerator{},
		"storage_container":                    &StorageContainerGenerator{},
		"synapse":                              &SynapseGenerator{},
		"subnet":                               &SubnetGenerator{},
		"virtual_machine":                      &VirtualMachineGenerator{},
		"virtual_network":                      &VirtualNetworkGenerator{},
	}
}

func (p *AzureProvider) InitService(serviceName string, verbose bool) error {
	var isSupported bool
	if _, isSupported = p.GetSupportedService()[serviceName]; !isSupported {
		return errors.New("azurerm: " + serviceName + " not supported service")
	}
	p.Service = p.GetSupportedService()[serviceName]
	p.Service.SetName(serviceName)
	p.Service.SetVerbose(verbose)
	p.Service.SetProviderName(p.GetName())
	p.Service.SetArgs(map[string]interface{}{
		"subscription_id": p.subscriptionID,
		"credential":      p.credential,
		"client_options":  p.clientOptions,
		"resource_group":  p.resourceGroup,
	})
	return nil
}

// Scope names the subscription and the resource group this import covers,
// for the output layout: all for the whole subscription. Azure resources
// of one subscription span locations, so roots are split by resource group
// rather than by location.
func (p *AzureProvider) Scope(context.Context) (account, region string, err error) {
	region = p.resourceGroup
	if region == "" {
		region = "all"
	}
	return p.subscriptionID, region, nil
}
