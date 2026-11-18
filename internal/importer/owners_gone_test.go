// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"maps"
	"slices"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

func TestListedByImportIDNamesWhatIsImportedByItsListedID(t *testing.T) {
	t.Parallel()
	server := harvest.Record("/subscriptions/s/resourceGroups/g/providers/Microsoft.DBforPostgreSQL/flexibleServers/db", "db", "azurerm_postgresql_flexible_server", "azurerm", nil)
	other := harvest.Record("listed-id", "q", "aws_sqs_queue", "aws", nil)
	selected := map[string][]harvest.Resource{"svc": {server, other}}
	importID := func(r harvest.Resource) (string, bool) {
		if r.Address.Type == "aws_sqs_queue" {
			return "another-id", true
		}
		// Azure IDs fold case.
		return "/subscriptions/s/resourcegroups/g/providers/Microsoft.DBforPostgreSQL/flexibleServers/db", true
	}
	got := slices.Sorted(maps.Keys(listedByImportID(selected, importID)))
	want := []string{"azurerm_postgresql_flexible_server /subscriptions/s/resourcegroups/g/providers/Microsoft.DBforPostgreSQL/flexibleServers/db"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
