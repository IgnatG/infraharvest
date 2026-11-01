// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"testing"

	"github.com/magodo/azlist/azlist"
	"github.com/magodo/aztft/aztft"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// A flexible server Resource Graph returns is mapped to its azurerm type
// with its parameters, those at the service's defaults recorded for the
// catalogue's skip rule to leave out, with its reason (the importer
// reports them as excluded), and the server itself is neither skipped nor
// left out. aztft maps from the ID alone, so nothing is asked of Azure.
func TestGraphResourcesMapsFlexibleServerWithItsParameters(t *testing.T) {
	t.Parallel()
	const server = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.DBforPostgreSQL/flexibleServers/pg"
	parameter := func(source string) map[string]interface{} {
		return map[string]interface{}{"properties": map[string]interface{}{"source": source}}
	}
	cat := catalogue.MustBuiltin("azurerm")
	resources := graphResources([]azlist.AzureResource{
		listed(t, server, map[string]interface{}{"properties": map[string]interface{}{"version": "16"}}),
		listed(t, server+"/configurations/work_mem", parameter("user-override")),
		listed(t, server+"/configurations/anon.salt", parameter("system-default")),
	}, cat, func(id string) ([]aztft.Type, []string, bool, error) {
		return aztft.QueryTypeAndId(id, nil)
	}, nil)
	reasons := map[string]string{}
	types := map[string]string{}
	for _, r := range resources {
		types[r.Observed.ID] = r.Address.Type
		reasons[r.Observed.ID] = cat.Skipped(r.Address.Type, r.Observed.ID, r.Observed.Attributes)
	}
	want := map[string]string{
		server:                               "azurerm_postgresql_flexible_server",
		server + "/configurations/work_mem":  "azurerm_postgresql_flexible_server_configuration",
		server + "/configurations/anon.salt": "azurerm_postgresql_flexible_server_configuration",
	}
	if len(types) != len(want) {
		t.Fatalf("got %v, want %v", types, want)
	}
	for id, typ := range want {
		if types[id] != typ {
			t.Errorf("%s: type %q, want %q", id, types[id], typ)
		}
	}
	if reasons[server] != "" || reasons[server+"/configurations/work_mem"] != "" {
		t.Errorf("the server or a parameter someone set is skipped: %v", reasons)
	}
	if reasons[server+"/configurations/anon.salt"] == "" {
		t.Error("a parameter at the service's default is not skipped, so it would vanish with no reason")
	}
}
