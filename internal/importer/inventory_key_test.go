// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"path/filepath"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/providers/aws"
	"github.com/IgnatG/infraharvest/internal/providers/azure"
	"github.com/IgnatG/infraharvest/internal/providers/google"
)

// A call's inventory is where the hash of its settings' key says, as it
// was where the hash of the positional strings those settings replace
// said: the hashes below are those of the strings, taken before the
// settings were typed, so that an inventory saved by an earlier release
// is still found by --reuse-inventory.
func TestInventoryPathIsThatOfTheSettingsKey(t *testing.T) {
	t.Parallel()
	const (
		role   = "arn:aws:iam::111122223333:role/reader"
		client = "11111111-1111-1111-1111-111111111111"
	)
	for _, tc := range []struct {
		name     string
		provider string
		settings harvest.ProviderSettings
		hash     string
	}{
		{"aws, no role", "aws", aws.Settings{Region: "eu-west-2", Profile: "default"}, "a921b17be4d8b1e9"},
		{"aws, a role", "aws", aws.Settings{Region: "eu-west-2", Profile: "prod", Role: aws.AssumeRole{ARN: role}}, "fb6f6c76848a7aa9"},
		{"aws, an external ID", "aws", aws.Settings{Region: "eu-west-2", Profile: "prod", Role: aws.AssumeRole{ARN: role, ExternalID: "ext-1"}}, "bafe010c2af4b88c"},
		{"aws, an external ID and a session name", "aws", aws.Settings{Region: "eu-west-2", Profile: "prod", Role: aws.AssumeRole{ARN: role, ExternalID: "ext-1", SessionName: "ci-42"}}, "765618dd82434028"},
		{"aws, a session name", "aws", aws.Settings{Region: "eu-west-2", Profile: "prod", Role: aws.AssumeRole{ARN: role, SessionName: "ci-42"}}, "c0a07debfa6c5181"},
		{"azure, a resource group", "azurerm", azure.Settings{ResourceGroup: "rg"}, "5a5000a3f3ff8686"},
		{"azure, the subscription", "azurerm", azure.Settings{}, "e3b0c44298fc1c14"},
		{"azure, a subscription", "azurerm", azure.Settings{ResourceGroup: "rg", Subscription: "sub-1"}, "f6d6d868ff0f7383"},
		{"azure, azapi", "azurerm", azure.Settings{ResourceGroup: "rg", AzAPI: true}, "5d5a924e056e4b44"},
		{"azure, azapi in a subscription", "azurerm", azure.Settings{ResourceGroup: "rg", Subscription: "sub-1", AzAPI: true}, "5030f4a91817232a"},
		{"azure, an identity", "azurerm", azure.Settings{ResourceGroup: "rg", Subscription: "sub-1", TenantID: "t.example.com", ClientID: client}, "45b947611cab792f"},
		{"azure, a tenant that pads", "azurerm", azure.Settings{TenantID: "t.example.com"}, "e2f011947ca240ab"},
		{"google", "google", google.Settings{Region: "europe-west1", Project: "acme"}, "d811a86a2fe1a3bd"},
		{"google, beta", "google-beta", google.Settings{Region: "global", Project: "acme", Beta: true}, "4b509c04abcbf763"},
		{"google, a service account", "google", google.Settings{Region: "global", Project: "acme", ServiceAccount: "sa@acme.iam.gserviceaccount.com"}, "6db5ae1e1e42f580"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want := filepath.Join("out", CheckpointDir, "inventory", tc.provider+"-"+tc.hash+".json")
			if got := inventoryPath("out", tc.provider, tc.settings); got != want {
				t.Errorf("got %s, want %s", got, want)
			}
		})
	}
}

// The same settings have the same path, and settings that differ, another;
// a call with none has one too.
func TestInventoryPathFollowsTheSettings(t *testing.T) {
	t.Parallel()
	a := inventoryPath("out", "aws", aws.Settings{Region: "eu-west-1", Profile: "prod"})
	if b := inventoryPath("out", "aws", aws.Settings{Region: "eu-west-1", Profile: "prod"}); a != b {
		t.Errorf("the same settings: %s and %s", a, b)
	}
	if b := inventoryPath("out", "aws", aws.Settings{Region: "eu-west-2", Profile: "prod"}); a == b {
		t.Errorf("another region has the path %s too", b)
	}
	if inventoryPath("out", "aws", nil) == "" {
		t.Error("no settings, no path")
	}
}
