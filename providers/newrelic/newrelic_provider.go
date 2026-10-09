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

package newrelic

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/newrelic/newrelic-client-go/v2/pkg/region"
)

type NewRelicProvider struct { //nolint
	terraformutils.Provider
	accountID int
	APIKey    string
	Region    string
}

// Init takes the API key, account ID and region from args, each falling
// back to its NEW_RELIC_* environment variable when empty: the flags are
// empty unless set.
func (p *NewRelicProvider) Init(args []string) error {
	arg := func(i int, env string) string {
		if len(args) > i && args[i] != "" {
			return args[i]
		}
		return os.Getenv(env)
	}
	p.APIKey = arg(0, "NEW_RELIC_API_KEY")
	if accountID := arg(1, "NEW_RELIC_ACCOUNT_ID"); accountID != "" {
		id, err := strconv.Atoi(accountID)
		if err != nil {
			return fmt.Errorf("newrelic: account ID %q: %w", accountID, err)
		}
		p.accountID = id
	}
	p.Region = strings.ToUpper(arg(2, "NEW_RELIC_REGION"))
	if p.Region == "" {
		p.Region = "US"
	}
	if _, err := region.Parse(p.Region); err != nil {
		return fmt.Errorf("newrelic: %w", err)
	}
	return nil
}

func (p *NewRelicProvider) GetName() string {
	return "newrelic"
}

// GetProviderData configures the provider block of the generated roots with
// the account and region listed; the API key stays in NEW_RELIC_API_KEY.
func (p *NewRelicProvider) GetProviderData(_ ...string) map[string]interface{} {
	config := map[string]interface{}{"region": p.Region}
	if p.accountID != 0 {
		config["account_id"] = p.accountID
	}
	return map[string]interface{}{"provider": map[string]interface{}{p.GetName(): config}}
}

func (p *NewRelicProvider) GetSupportedService() map[string]terraformutils.ServiceGenerator {
	return map[string]terraformutils.ServiceGenerator{
		"alert":           &AlertGenerator{},
		"alert_channel":   &AlertChannelGenerator{},
		"alert_condition": &AlertConditionGenerator{},
		"alert_policy":    &AlertPolicyGenerator{},
		"infra":           &InfraGenerator{},
		"synthetics":      &SyntheticsGenerator{},
		"tags":            &TagsGenerator{},
	}
}

func (p *NewRelicProvider) InitService(serviceName string, verbose bool) error {
	var isSupported bool
	if _, isSupported = p.GetSupportedService()[serviceName]; !isSupported {
		return errors.New("newrelic: " + serviceName + " not supported service")
	}
	p.Service = p.GetSupportedService()[serviceName]
	p.Service.SetName(serviceName)
	p.Service.SetVerbose(verbose)
	p.Service.SetArgs(map[string]interface{}{
		"apiKey":    p.APIKey,
		"accountID": p.accountID,
		"region":    p.Region,
	})
	p.Service.SetProviderName(p.GetName())

	return nil
}

// GetSource is the provider's registry source, for required_providers.
func (p *NewRelicProvider) GetSource() string {
	return "newrelic/newrelic"
}
