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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/IgnatG/infraharvest/terraformutils"
	newrelic "github.com/newrelic/newrelic-client-go/v2/newrelic"
	"github.com/newrelic/newrelic-client-go/v2/pkg/alerts"
	"github.com/newrelic/newrelic-client-go/v2/pkg/region"
)

// infraConditionsPageSize is the Infrastructure API's default page size.
const infraConditionsPageSize = 50

type InfraGenerator struct {
	NewRelicService
}

func (g *InfraGenerator) createAlertInfraConditionResources(client *newrelic.NewRelic) error {
	alertPolicies, err := client.Alerts.ListPolicies(nil)
	if err != nil {
		return err
	}

	reg, err := region.Get(region.Default)
	if err != nil {
		return err
	}
	conditionsURL := reg.InfrastructureURL("/alerts/conditions")
	httpClient := &http.Client{Timeout: time.Minute}
	apiKey := g.GetArgs()["apiKey"].(string)

	for _, alertPolicy := range alertPolicies {
		alertInfraConditions, err := listInfraConditions(context.Background(), httpClient, conditionsURL, apiKey, alertPolicy.ID)
		if err != nil {
			return err
		}
		for _, alertInfraCondition := range alertInfraConditions {
			g.Resources = append(g.Resources, terraformutils.NewResource(
				fmt.Sprintf("%d:%d", alertPolicy.ID, alertInfraCondition.ID),
				fmt.Sprintf("%s-%d", normalizeResourceName(alertInfraCondition.Name), alertInfraCondition.ID),
				"newrelic_infra_alert_condition",
				g.ProviderName,
				map[string]string{
					"type": alertInfraCondition.Type,
				}))
		}
	}
	return nil
}

type infraConditionsPage struct {
	Data []alerts.InfrastructureCondition `json:"data"`
	Meta struct {
		Total int `json:"total"`
	} `json:"meta"`
}

// listInfraConditions lists a policy's infrastructure alert conditions page
// by page. The SDK's ListInfrastructureConditions reads only the first page.
func listInfraConditions(ctx context.Context, client *http.Client, conditionsURL, apiKey string, policyID int) ([]alerts.InfrastructureCondition, error) {
	var all []alerts.InfrastructureCondition
	for {
		page, err := getInfraConditionsPage(ctx, client, conditionsURL, apiKey, policyID, len(all))
		if err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		if page.Meta.Total > 0 {
			if len(page.Data) == 0 || len(all) >= page.Meta.Total {
				return all, nil
			}
		} else if len(page.Data) < infraConditionsPageSize {
			return all, nil
		}
	}
}

func getInfraConditionsPage(ctx context.Context, client *http.Client, conditionsURL, apiKey string, policyID, offset int) (*infraConditionsPage, error) {
	u, err := url.Parse(conditionsURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("policy_id", strconv.Itoa(policyID))
	q.Set("limit", strconv.Itoa(infraConditionsPageSize))
	q.Set("offset", strconv.Itoa(offset))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Api-Key", apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("listing infrastructure conditions of policy %d: %s: %s",
			policyID, resp.Status, strings.TrimSpace(string(body)))
	}
	var page infraConditionsPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("listing infrastructure conditions of policy %d: %w", policyID, err)
	}
	return &page, nil
}

func (g *InfraGenerator) InitResources() error {
	client, err := g.Client()
	if err != nil {
		return err
	}

	err = g.createAlertInfraConditionResources(client)
	if err != nil {
		return err
	}

	return nil
}
