// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"sync/atomic"
	"testing"

	"github.com/magodo/azlist/azlist"
	"github.com/magodo/aztft/aztft"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// A flexible server's parameters are listed with the server, each with
// where its value comes from. The lister records that and maps them from
// the listing, without a request to aztft, and the built-in skip rules
// leave out those at the service's defaults, which have no value to
// import (azurerm requires one); the ones a user set, and one listed
// without a source, are kept. Before the fix, every parameter of a server
// was imported and failed the plan with "value is required".
func TestGraphResourcesSkipsServerParametersAtDefaults(t *testing.T) {
	t.Parallel()
	const (
		postgres = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.DBforPostgreSQL/flexibleServers/pg"
		mysql    = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.DBforMySQL/flexibleServers/my"
	)
	parameter := func(source string) map[string]interface{} {
		properties := map[string]interface{}{"value": "x", "defaultValue": "y"}
		if source != "" {
			properties["source"] = source
		}
		return map[string]interface{}{"properties": properties}
	}
	type want struct {
		typ     string
		skipped bool
		source  string
	}
	wants := map[string]want{
		postgres + "/configurations/work_mem":         {"azurerm_postgresql_flexible_server_configuration", false, "user-override"},
		postgres + "/configurations/azure.extensions": {"azurerm_postgresql_flexible_server_configuration", false, "user-override"},
		postgres + "/configurations/anon.salt":        {"azurerm_postgresql_flexible_server_configuration", true, "system-default"},
		postgres + "/configurations/application_name": {"azurerm_postgresql_flexible_server_configuration", true, "System-Default"},
		mysql + "/configurations/slow_query_log":      {"azurerm_mysql_flexible_server_configuration", false, "user-override"},
		mysql + "/configurations/sql_mode":            {"azurerm_mysql_flexible_server_configuration", true, "system-default"},
		// Listed without its source: left to aztft, and kept.
		postgres + "/configurations/bonjour_name": {"azurerm_postgresql_flexible_server_configuration", false, ""},
	}
	var listedResources []azlist.AzureResource
	for id, w := range wants {
		listedResources = append(listedResources, listed(t, id, parameter(w.source)))
	}
	var asked atomic.Int32
	cat := catalogue.MustBuiltin("azurerm")
	resources := graphResources(listedResources, cat, func(id string) ([]aztft.Type, []string, bool, error) {
		asked.Add(1)
		if id != postgres+"/configurations/bonjour_name" {
			t.Errorf("aztft was asked about %s, which the listing maps", id)
		}
		return aztft.QueryTypeAndId(id, nil)
	}, nil)
	if asked.Load() != 1 {
		t.Errorf("aztft was asked about %d resources, want 1", asked.Load())
	}
	if len(resources) != len(wants) {
		t.Fatalf("got %d resources, want %d: %+v", len(resources), len(wants), resources)
	}
	for _, r := range resources {
		w, ok := wants[r.Observed.ID]
		if !ok {
			t.Errorf("unexpected %s %s", r.Address.Type, r.Observed.ID)
			continue
		}
		if r.Address.Type != w.typ {
			t.Errorf("%s: type %s, want %s", r.Observed.ID, r.Address.Type, w.typ)
		}
		if got := r.Observed.Attributes["configuration_source"]; got != w.source {
			t.Errorf("%s: configuration_source %q, want %q", r.Observed.ID, got, w.source)
		}
		if reason := cat.Skipped(r.Address.Type, r.Observed.ID, r.Observed.Attributes); w.skipped != (reason != "") {
			t.Errorf("%s: skipped %q, want skipped %v", r.Observed.ID, reason, w.skipped)
		}
	}
}

// Only a flexible server's parameters have a source recorded; another
// resource with a source property is not one.
func TestConfigurationSource(t *testing.T) {
	t.Parallel()
	const server = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.DBforPostgreSQL/flexibleServers/pg"
	properties := map[string]interface{}{"properties": map[string]interface{}{"source": "user-override"}}
	if got := configurationSource(listed(t, server, properties)); got != "" {
		t.Errorf("a server: source %q", got)
	}
	properties = map[string]interface{}{"properties": map[string]interface{}{"source": "user-override"}}
	if got := configurationSource(listed(t, server+"/configurations/work_mem", properties)); got != "user-override" {
		t.Errorf("a parameter: source %q", got)
	}
	if got := configurationSource(listed(t, server+"/configurations/work_mem", nil)); got != "" {
		t.Errorf("a parameter listed without properties: source %q", got)
	}
}
