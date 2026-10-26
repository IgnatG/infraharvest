// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import "testing"

// ResourceKey tells an azapi_resource apart by its ID without the API
// version, so that a newer one is still the same resource; other types'
// IDs stay as they are.
func TestResourceKey(t *testing.T) {
	t.Parallel()
	const id = "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Easm/workspaces/w"
	if ResourceKey("azapi_resource", id+"?api-version=2023-04-01") != ResourceKey("azapi_resource", id+"?api-version=2024-10-01") {
		t.Error("two API versions of an azapi_resource are two resources")
	}
	if ResourceKey("azapi_resource", id) != (External{Type: "azapi_resource", ID: id}) {
		t.Error("a bare ID changed")
	}
	if got := ResourceKey("aws_sqs_queue", "https://q?api-version=1"); got.ID != "https://q?api-version=1" {
		t.Errorf("another type's ID changed: %q", got.ID)
	}
}
