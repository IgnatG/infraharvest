// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"strings"

	"github.com/magodo/azlist/azlist"
	"github.com/magodo/aztft/aztft"
)

// The Resource Manager types of a flexible server's parameters, and the
// azurerm types they map to.
const (
	postgresqlConfigurationType = "Microsoft.DBforPostgreSQL/flexibleServers/configurations"
	mysqlConfigurationType      = "Microsoft.DBforMySQL/flexibleServers/configurations"

	postgresqlConfigurationTFType = "azurerm_postgresql_flexible_server_configuration"
	mysqlConfigurationTFType      = "azurerm_mysql_flexible_server_configuration"
)

// configurationTypes maps the upper-case Resource Manager type of a
// flexible server's parameters to their azurerm type.
var configurationTypes = map[string]string{
	strings.ToUpper(postgresqlConfigurationType): postgresqlConfigurationTFType,
	strings.ToUpper(mysqlConfigurationType):      mysqlConfigurationTFType,
}

// configurationSource returns where a flexible server parameter's value
// comes from, as the listing says it in properties.source: "system-default"
// for one at the service's default, "user-override" for one that was set.
// It returns "" for a resource that isn't a parameter, or one listed
// without a source.
func configurationSource(res azlist.AzureResource) string {
	if _, ok := configurationTypes[strings.ToUpper(res.Id.TypeString())]; !ok {
		return ""
	}
	properties, _ := res.Properties["properties"].(map[string]interface{})
	source, _ := properties["source"].(string)
	return source
}

// configurationFromListing maps a flexible server parameter from what the
// listing returned, with no request. A server has hundreds of parameters,
// all of which the listing returns, and azurerm needs a value for every
// one it manages, which one at the service's default doesn't have: the
// catalogue's skip rule leaves out those whose configuration_source
// (builtInAttributes) is system-default, so only the parameters a user set
// are imported. It returns false for a parameter listed without its
// source, which is left to aztft, as is any other resource.
func configurationFromListing(res azlist.AzureResource, attributes map[string]string) (mapping, bool) {
	typ, ok := configurationTypes[strings.ToUpper(res.Id.TypeString())]
	if !ok || attributes["configuration_source"] == "" {
		return mapping{}, false
	}
	// A parameter's import ID is its Azure ID; aztft builds it from the ID.
	id := res.Id.String()
	importID, err := aztft.QueryId(id, typ, nil)
	if err != nil {
		return mapping{leftOut: ": " + err.Error()}, true
	}
	return mapping{types: []aztft.Type{{AzureId: res.Id, TFType: typ}}, ids: []string{importID}}, true
}
