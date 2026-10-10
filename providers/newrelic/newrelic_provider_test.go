// Copyright 2026 The Terraformer Authors.
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

package newrelic

import (
	"encoding/base64"
	"testing"
)

func TestInitFallsBackToEnvironment(t *testing.T) {
	t.Setenv("NEW_RELIC_API_KEY", "env-key")
	t.Setenv("NEW_RELIC_ACCOUNT_ID", "42")
	t.Setenv("NEW_RELIC_REGION", "eu")
	var p NewRelicProvider
	if err := p.Init([]string{"", "", ""}); err != nil {
		t.Fatal(err)
	}
	if p.APIKey != "env-key" || p.accountID != 42 || p.Region != "EU" {
		t.Errorf("got key %q, account %d, region %q; want env-key, 42, EU", p.APIKey, p.accountID, p.Region)
	}

	if err := p.Init([]string{"flag-key", "7", "jp"}); err != nil {
		t.Fatal(err)
	}
	if p.APIKey != "flag-key" || p.accountID != 7 || p.Region != "JP" {
		t.Errorf("got key %q, account %d, region %q; want flag-key, 7, JP", p.APIKey, p.accountID, p.Region)
	}
}

func TestInitDefaultsAndRejects(t *testing.T) {
	t.Setenv("NEW_RELIC_API_KEY", "")
	t.Setenv("NEW_RELIC_ACCOUNT_ID", "")
	t.Setenv("NEW_RELIC_REGION", "")
	var p NewRelicProvider
	if err := p.Init([]string{"key", "", ""}); err != nil {
		t.Fatal(err)
	}
	if p.Region != "US" || p.accountID != 0 {
		t.Errorf("got region %q, account %d; want US, 0", p.Region, p.accountID)
	}
	if err := p.Init([]string{"key", "abc", ""}); err == nil {
		t.Error("an account ID that isn't a number: no error")
	}
	if err := p.Init([]string{"key", "1", "mars"}); err == nil {
		t.Error("an unknown region: no error")
	}
}

func TestServicesUseTheRegion(t *testing.T) {
	var p NewRelicProvider
	if err := p.Init([]string{"key", "42", "EU"}); err != nil {
		t.Fatal(err)
	}
	if err := p.InitService("infra", false); err != nil {
		t.Fatal(err)
	}
	g := p.Service.(*InfraGenerator)
	reg, err := g.Region()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := reg.InfrastructureURL("/alerts/conditions"), "https://infra-api.eu.newrelic.com/v2/alerts/conditions"; got != want {
		t.Errorf("infrastructure URL %q, want %q", got, want)
	}
	if g.accountID() != 42 {
		t.Errorf("account %d, want 42", g.accountID())
	}
	provider := p.GetProviderData()["provider"].(map[string]interface{})["newrelic"].(map[string]interface{})
	if provider["region"] != "EU" || provider["account_id"] != 42 {
		t.Errorf("provider block %v, want region EU and account_id 42", provider)
	}
}

func TestEntityGUID(t *testing.T) {
	guid := entityGUID(42, "SYNTH", "MONITOR", "abc-123")
	decoded, err := base64.RawStdEncoding.DecodeString(string(guid))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(decoded), "42|SYNTH|MONITOR|abc-123"; got != want {
		t.Errorf("GUID decodes to %q, want %q", got, want)
	}
}
