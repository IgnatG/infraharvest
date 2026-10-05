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
	"fmt"
	"os"
	"strings"

	"github.com/Azure/go-autorest/autorest"
	"github.com/hashicorp/go-azure-helpers/authentication"
	"github.com/hashicorp/go-azure-helpers/sender"
	"github.com/manicminer/hamilton/environments"

	"github.com/IgnatG/infraharvest/terraformutils"
)

type AzureProvider struct { //nolint
	terraformutils.Provider
	config        authentication.Config
	authorizer    autorest.Authorizer
	resourceGroup string
}

func (p *AzureProvider) setEnvConfig() error {
	subscriptionID := os.Getenv("ARM_SUBSCRIPTION_ID")
	if subscriptionID == "" {
		return errors.New("set ARM_SUBSCRIPTION_ID env var")
	}
	var auxTenants []string
	if v := os.Getenv("ARM_AUXILIARY_TENANT_IDS"); v != "" {
		auxTenants = strings.Split(v, ";")
		if len(auxTenants) > 3 {
			return fmt.Errorf("the provider only supports 3 auxiliary tenant IDs for ARM_AUXILIARY_TENANT_IDS")
		}
	}
	builder := &authentication.Builder{
		ClientID:            os.Getenv("ARM_CLIENT_ID"),
		SubscriptionID:      subscriptionID,
		TenantID:            os.Getenv("ARM_TENANT_ID"),
		AuxiliaryTenantIDs:  auxTenants,
		Environment:         os.Getenv("ARM_ENVIRONMENT"),
		MetadataHost:        os.Getenv("ARM_METADATA_HOSTNAME"),
		MsiEndpoint:         os.Getenv("ARM_MSI_ENDPOINT"),
		ClientSecret:        os.Getenv("ARM_CLIENT_SECRET"),
		ClientCertPath:      os.Getenv("ARM_CLIENT_CERTIFICATE_PATH"),
		ClientCertPassword:  os.Getenv("ARM_CLIENT_CERTIFICATE_PASSWORD"),
		IDTokenRequestToken: os.Getenv("ARM_OIDC_REQUEST_TOKEN"),
		IDTokenRequestURL:   os.Getenv("ARM_OIDC_REQUEST_URL"),

		// Feature Toggles
		SupportsAzureCliToken:          true,
		SupportsClientSecretAuth:       true,
		SupportsClientCertAuth:         true,
		SupportsManagedServiceIdentity: os.Getenv("ARM_USE_MSI") != "",
		SupportsOIDCAuth:               os.Getenv("ARM_USE_OIDC") != "",
		UseMicrosoftGraph:              os.Getenv("ARM_USE_ADAL") == "",
	}

	if builder.Environment == "" {
		builder.Environment = "public"
	}
	config, err := builder.Build()
	if err != nil {
		return nil
	}
	p.config = *config

	return nil
}

func (p *AzureProvider) getAuthorizer() (autorest.Authorizer, error) {
	env, err := authentication.DetermineEnvironment(p.config.Environment)
	if err != nil {
		return nil, err
	}
	p.config.CustomResourceManagerEndpoint = env.ResourceManagerEndpoint
	oauthConfig, err := p.config.BuildOAuthConfig(env.ActiveDirectoryEndpoint)
	if err != nil {
		return nil, err
	}
	if oauthConfig == nil {
		return nil, fmt.Errorf("unable to configure OAuthConfig for tenant %s", p.config.TenantID)
	}
	sender := sender.BuildSender("infraharvest")
	ctx := context.Background()
	var auth autorest.Authorizer

	if p.config.UseMicrosoftGraph {
		hamiltonEnv, ero := environments.EnvironmentFromString(p.config.Environment)
		if ero != nil {
			return nil, ero
		}
		auth, err = p.config.GetMSALToken(ctx, hamiltonEnv.ResourceManager, sender, oauthConfig, env.TokenAudience)
	} else {
		// Deprecated
		auth, err = p.config.GetADALToken(ctx, sender, oauthConfig, env.ResourceManagerEndpoint)
	}
	if err != nil {
		return nil, err
	}
	return auth, nil
}

func (p *AzureProvider) Init(args []string) error {
	err := p.setEnvConfig()
	if err != nil {
		return err
	}

	authorizer, err := p.getAuthorizer()
	if err != nil {
		return err
	}
	p.authorizer = authorizer
	p.resourceGroup = args[0]

	return nil
}

func (p *AzureProvider) GetName() string {
	return "azurerm"
}

// GetProviderData returns the azurerm provider block. The engine pins the
// provider version in versions.tf; azurerm requires an (empty) features block.
func (p *AzureProvider) GetProviderData(arg ...string) map[string]interface{} {
	return map[string]interface{}{
		"provider": map[string]interface{}{
			"azurerm": map[string]interface{}{
				"features": map[string]interface{}{},
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
		"config":         p.config,
		"authorizer":     p.authorizer,
		"resource_group": p.resourceGroup,
	})
	return nil
}
