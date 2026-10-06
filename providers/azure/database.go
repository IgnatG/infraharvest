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

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/mariadb/armmariadb"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/mysql/armmysql"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresql"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/sql/armsql"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type DatabasesGenerator struct {
	AzureService
}

// serverResourceGroup returns the resource group of a database server.
func serverResourceGroup(serverID *string) (string, error) {
	id, err := ParseAzureResourceID(*serverID)
	if err != nil {
		return "", err
	}
	return id.ResourceGroup, nil
}

func (g *DatabasesGenerator) getMariaDBServers() ([]*armmariadb.Server, error) {
	ctx := context.Background()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	client, err := armmariadb.NewServersClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	if resourceGroup != "" {
		return listAll(ctx, client.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armmariadb.ServersClientListByResourceGroupResponse) []*armmariadb.Server { return p.Value })
	}
	return listAll(ctx, client.NewListPager(nil),
		func(p armmariadb.ServersClientListResponse) []*armmariadb.Server { return p.Value })
}

func (g *DatabasesGenerator) createMariaDBServerResources(servers []*armmariadb.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource

	for _, server := range servers {
		resources = append(resources, terraformutils.NewResource(
			*server.ID,
			*server.Name,
			"azurerm_mariadb_server",
			g.ProviderName,
			map[string]string{}))
	}

	return resources, nil
}

func (g *DatabasesGenerator) createMariaDBConfigurationResources(servers []*armmariadb.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armmariadb.NewConfigurationsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}
		configs, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armmariadb.ConfigurationsClientListByServerResponse) []*armmariadb.Configuration {
				return p.Value
			})
		if err != nil {
			return nil, err
		}

		for _, config := range configs {
			resources = append(resources, terraformutils.NewSimpleResource(
				*config.ID,
				*config.Name+"-"+*server.Name,
				"azurerm_mariadb_configuration",
				g.ProviderName))
		}
	}

	return resources, nil
}

func (g *DatabasesGenerator) createMariaDBDatabaseResources(servers []*armmariadb.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armmariadb.NewDatabasesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}
		databases, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armmariadb.DatabasesClientListByServerResponse) []*armmariadb.Database { return p.Value })
		if err != nil {
			return nil, err
		}

		for _, database := range databases {
			resources = append(resources, terraformutils.NewSimpleResource(
				*database.ID,
				*database.Name+"-"+*server.Name,
				"azurerm_mariadb_database",
				g.ProviderName))
		}
	}

	return resources, nil
}

func (g *DatabasesGenerator) createMariaDBFirewallRuleResources(servers []*armmariadb.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armmariadb.NewFirewallRulesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}
		rules, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armmariadb.FirewallRulesClientListByServerResponse) []*armmariadb.FirewallRule { return p.Value })
		if err != nil {
			return nil, err
		}
		for _, rule := range rules {
			resources = append(resources, terraformutils.NewSimpleResource(
				*rule.ID,
				*rule.Name,
				"azurerm_mariadb_firewall_rule",
				g.ProviderName))
		}
	}

	return resources, nil
}

func (g *DatabasesGenerator) createMariaDBVirtualNetworkRuleResources(servers []*armmariadb.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armmariadb.NewVirtualNetworkRulesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}
		rules, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armmariadb.VirtualNetworkRulesClientListByServerResponse) []*armmariadb.VirtualNetworkRule {
				return p.Value
			})
		if err != nil {
			return nil, err
		}
		for _, rule := range rules {
			resources = append(resources, terraformutils.NewSimpleResource(
				*rule.ID,
				*rule.Name,
				"azurerm_mariadb_virtual_network_rule",
				g.ProviderName))
		}
	}
	return resources, nil
}

func (g *DatabasesGenerator) getMySQLServers() ([]*armmysql.Server, error) {
	ctx := context.Background()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	client, err := armmysql.NewServersClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	if resourceGroup != "" {
		return listAll(ctx, client.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armmysql.ServersClientListByResourceGroupResponse) []*armmysql.Server { return p.Value })
	}
	return listAll(ctx, client.NewListPager(nil),
		func(p armmysql.ServersClientListResponse) []*armmysql.Server { return p.Value })
}

func (g *DatabasesGenerator) createMySQLServerResources(servers []*armmysql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource

	for _, server := range servers {
		resources = append(resources, terraformutils.NewResource(
			*server.ID,
			*server.Name,
			"azurerm_mysql_server",
			g.ProviderName,
			map[string]string{}))
	}

	return resources, nil
}

func (g *DatabasesGenerator) createMySQLConfigurationResources(servers []*armmysql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armmysql.NewConfigurationsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}

		configs, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armmysql.ConfigurationsClientListByServerResponse) []*armmysql.Configuration { return p.Value })
		if err != nil {
			return nil, err
		}
		for _, config := range configs {
			resources = append(resources, terraformutils.NewSimpleResource(
				*config.ID,
				*config.Name+"-"+*server.Name,
				"azurerm_mysql_configuration",
				g.ProviderName))
		}
	}

	return resources, nil
}

func (g *DatabasesGenerator) createMySQLDatabaseResources(servers []*armmysql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armmysql.NewDatabasesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}
		databases, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armmysql.DatabasesClientListByServerResponse) []*armmysql.Database { return p.Value })
		if err != nil {
			return nil, err
		}

		for _, database := range databases {
			resources = append(resources, terraformutils.NewSimpleResource(
				*database.ID,
				*database.Name+"-"+*server.Name,
				"azurerm_mysql_database",
				g.ProviderName))
		}
	}
	return resources, nil
}

func (g *DatabasesGenerator) createMySQLFirewallRuleResources(servers []*armmysql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armmysql.NewFirewallRulesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}
		rules, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armmysql.FirewallRulesClientListByServerResponse) []*armmysql.FirewallRule { return p.Value })
		if err != nil {
			return nil, err
		}

		for _, rule := range rules {
			resources = append(resources, terraformutils.NewSimpleResource(
				*rule.ID,
				*rule.Name,
				"azurerm_mysql_firewall_rule",
				g.ProviderName))
		}
	}

	return resources, nil
}

func (g *DatabasesGenerator) createMySQLVirtualNetworkRuleResources(servers []*armmysql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armmysql.NewVirtualNetworkRulesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}

		rules, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armmysql.VirtualNetworkRulesClientListByServerResponse) []*armmysql.VirtualNetworkRule {
				return p.Value
			})
		if err != nil {
			return nil, err
		}

		for _, rule := range rules {
			resources = append(resources, terraformutils.NewSimpleResource(
				*rule.ID,
				*rule.Name,
				"azurerm_mysql_virtual_network_rule",
				g.ProviderName))
		}
	}

	return resources, nil
}

func (g *DatabasesGenerator) getPostgreSQLServers() ([]*armpostgresql.Server, error) {
	ctx := context.Background()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	client, err := armpostgresql.NewServersClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	if resourceGroup != "" {
		return listAll(ctx, client.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armpostgresql.ServersClientListByResourceGroupResponse) []*armpostgresql.Server { return p.Value })
	}
	return listAll(ctx, client.NewListPager(nil),
		func(p armpostgresql.ServersClientListResponse) []*armpostgresql.Server { return p.Value })
}

func (g *DatabasesGenerator) createPostgreSQLServerResources(servers []*armpostgresql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource

	for _, server := range servers {
		resources = append(resources, terraformutils.NewResource(
			*server.ID,
			*server.Name,
			"azurerm_postgresql_server",
			g.ProviderName,
			map[string]string{}))
	}

	return resources, nil
}

func (g *DatabasesGenerator) createPostgreSQLDatabaseResources(servers []*armpostgresql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armpostgresql.NewDatabasesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}
		databases, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armpostgresql.DatabasesClientListByServerResponse) []*armpostgresql.Database { return p.Value })
		if err != nil {
			return nil, err
		}

		for _, database := range databases {
			resources = append(resources, terraformutils.NewSimpleResource(
				*database.ID,
				*database.Name+"-"+*server.Name,
				"azurerm_postgresql_database",
				g.ProviderName))
		}
	}
	return resources, nil
}

func (g *DatabasesGenerator) createPostgreSQLConfigurationResources(servers []*armpostgresql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armpostgresql.NewConfigurationsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}
		configs, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armpostgresql.ConfigurationsClientListByServerResponse) []*armpostgresql.Configuration {
				return p.Value
			})
		if err != nil {
			return nil, err
		}

		for _, config := range configs {
			resources = append(resources, terraformutils.NewSimpleResource(
				*config.ID,
				*config.Name+"-"+*server.Name,
				"azurerm_postgresql_configuration",
				g.ProviderName))
		}
	}
	return resources, nil
}

func (g *DatabasesGenerator) createPostgreSQLFirewallRuleResources(servers []*armpostgresql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armpostgresql.NewFirewallRulesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}
		rules, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armpostgresql.FirewallRulesClientListByServerResponse) []*armpostgresql.FirewallRule {
				return p.Value
			})
		if err != nil {
			return nil, err
		}

		for _, rule := range rules {
			resources = append(resources, terraformutils.NewSimpleResource(
				*rule.ID,
				*rule.Name,
				"azurerm_postgresql_firewall_rule",
				g.ProviderName))
		}
	}
	return resources, nil
}

func (g *DatabasesGenerator) createPostgreSQLVirtualNetworkRuleResources(servers []*armpostgresql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armpostgresql.NewVirtualNetworkRulesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}
		rules, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armpostgresql.VirtualNetworkRulesClientListByServerResponse) []*armpostgresql.VirtualNetworkRule {
				return p.Value
			})
		if err != nil {
			return nil, err
		}

		for _, rule := range rules {
			resources = append(resources, terraformutils.NewSimpleResource(
				*rule.ID,
				*rule.Name,
				"azurerm_postgresql_virtual_network_rule",
				g.ProviderName))
		}
	}
	return resources, nil
}

func (g *DatabasesGenerator) getSQLServers() ([]*armsql.Server, error) {
	ctx := context.Background()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	client, err := armsql.NewServersClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	if resourceGroup != "" {
		return listAll(ctx, client.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armsql.ServersClientListByResourceGroupResponse) []*armsql.Server { return p.Value })
	}
	return listAll(ctx, client.NewListPager(nil),
		func(p armsql.ServersClientListResponse) []*armsql.Server { return p.Value })
}

func (g *DatabasesGenerator) createSQLServerResources(servers []*armsql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource

	for _, server := range servers {
		resources = append(resources, terraformutils.NewResource(
			*server.ID,
			*server.Name,
			"azurerm_mssql_server",
			g.ProviderName,
			map[string]string{}))
	}

	return resources, nil
}

func (g *DatabasesGenerator) createSQLDatabaseResources(servers []*armsql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armsql.NewDatabasesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}
		databases, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armsql.DatabasesClientListByServerResponse) []*armsql.Database { return p.Value })
		if err != nil {
			return nil, err
		}

		for _, database := range databases {
			resources = append(resources, terraformutils.NewSimpleResource(
				*database.ID,
				*database.Name+"-"+*server.Name,
				"azurerm_mssql_database",
				g.ProviderName))
		}
	}
	return resources, nil
}

func (g *DatabasesGenerator) createSQLFirewallRuleResources(servers []*armsql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armsql.NewFirewallRulesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}
		rules, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armsql.FirewallRulesClientListByServerResponse) []*armsql.FirewallRule { return p.Value })
		if err != nil {
			return nil, err
		}

		for _, rule := range rules {
			resources = append(resources, terraformutils.NewSimpleResource(
				*rule.ID,
				*rule.Name,
				"azurerm_mssql_firewall_rule",
				g.ProviderName))
		}
	}
	return resources, nil
}

func (g *DatabasesGenerator) createSQLVirtualNetworkRuleResources(servers []*armsql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armsql.NewVirtualNetworkRulesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}
		rules, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armsql.VirtualNetworkRulesClientListByServerResponse) []*armsql.VirtualNetworkRule {
				return p.Value
			})
		if err != nil {
			return nil, err
		}

		for _, rule := range rules {
			resources = append(resources, terraformutils.NewSimpleResource(
				*rule.ID,
				*rule.Name,
				"azurerm_sql_virtual_network_rule",
				g.ProviderName))
		}
	}
	return resources, nil
}

func (g *DatabasesGenerator) createSQLElasticPoolResources(servers []*armsql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armsql.NewElasticPoolsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}
		pools, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armsql.ElasticPoolsClientListByServerResponse) []*armsql.ElasticPool { return p.Value })
		if err != nil {
			return nil, err
		}

		for _, pool := range pools {
			resources = append(resources, terraformutils.NewSimpleResource(
				*pool.ID,
				*pool.Name,
				"azurerm_sql_elasticpool",
				g.ProviderName))
		}
	}
	return resources, nil
}

func (g *DatabasesGenerator) createSQLFailoverResources(servers []*armsql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armsql.NewFailoverGroupsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}

		failoverGroups, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armsql.FailoverGroupsClientListByServerResponse) []*armsql.FailoverGroup { return p.Value })
		if err != nil {
			return nil, err
		}

		for _, failoverGroup := range failoverGroups {
			resources = append(resources, terraformutils.NewSimpleResource(
				*failoverGroup.ID,
				*failoverGroup.Name,
				"azurerm_sql_failover_group",
				g.ProviderName))
		}
	}
	return resources, nil
}

func (g *DatabasesGenerator) createSQLADAdministratorResources(servers []*armsql.Server) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	client, err := armsql.NewServerAzureADAdministratorsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	for _, server := range servers {
		resourceGroup, err := serverResourceGroup(server.ID)
		if err != nil {
			return nil, err
		}

		administrators, err := listAll(ctx, client.NewListByServerPager(resourceGroup, *server.Name, nil),
			func(p armsql.ServerAzureADAdministratorsClientListByServerResponse) []*armsql.ServerAzureADAdministrator {
				return p.Value
			})
		if err != nil {
			return nil, err
		}

		for _, administrator := range administrators {
			resources = append(resources, terraformutils.NewSimpleResource(
				*administrator.ID,
				*administrator.Name,
				"azurerm_sql_active_directory_administrator",
				g.ProviderName))
		}
	}
	return resources, nil
}

func (g *DatabasesGenerator) InitResources() error {
	mariadbServers, err := g.getMariaDBServers()
	if err != nil {
		return err
	}

	mysqlServers, err := g.getMySQLServers()
	if err != nil {
		return err
	}

	postgresqlServers, err := g.getPostgreSQLServers()
	if err != nil {
		return err
	}

	sqlServers, err := g.getSQLServers()
	if err != nil {
		return err
	}

	mariadbFunctions := []func([]*armmariadb.Server) ([]terraformutils.Resource, error){
		g.createMariaDBServerResources,
		g.createMariaDBDatabaseResources,
		g.createMariaDBConfigurationResources,
		g.createMariaDBFirewallRuleResources,
		g.createMariaDBVirtualNetworkRuleResources,
	}

	mysqlFunctions := []func([]*armmysql.Server) ([]terraformutils.Resource, error){
		g.createMySQLServerResources,
		g.createMySQLDatabaseResources,
		g.createMySQLConfigurationResources,
		g.createMySQLFirewallRuleResources,
		g.createMySQLVirtualNetworkRuleResources,
	}

	postgresqlFunctions := []func([]*armpostgresql.Server) ([]terraformutils.Resource, error){
		g.createPostgreSQLServerResources,
		g.createPostgreSQLDatabaseResources,
		g.createPostgreSQLConfigurationResources,
		g.createPostgreSQLFirewallRuleResources,
		g.createPostgreSQLVirtualNetworkRuleResources,
	}

	sqlFunctions := []func([]*armsql.Server) ([]terraformutils.Resource, error){
		g.createSQLServerResources,
		g.createSQLDatabaseResources,
		g.createSQLADAdministratorResources,
		g.createSQLElasticPoolResources,
		g.createSQLFailoverResources,
		g.createSQLFirewallRuleResources,
		g.createSQLVirtualNetworkRuleResources,
	}

	for _, f := range mariadbFunctions {
		resources, err := f(mariadbServers)
		if err != nil {
			return err
		}
		g.Resources = append(g.Resources, resources...)
	}

	for _, f := range mysqlFunctions {
		resources, err := f(mysqlServers)
		if err != nil {
			return err
		}
		g.Resources = append(g.Resources, resources...)
	}

	for _, f := range postgresqlFunctions {
		resources, err := f(postgresqlServers)
		if err != nil {
			return err
		}
		g.Resources = append(g.Resources, resources...)
	}

	for _, f := range sqlFunctions {
		resources, err := f(sqlServers)
		if err != nil {
			return err
		}
		g.Resources = append(g.Resources, resources...)
	}

	return nil
}
