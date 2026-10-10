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
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/IgnatG/infraharvest/terraformutils"
	newrelic "github.com/newrelic/newrelic-client-go/v2/newrelic"
	"github.com/newrelic/newrelic-client-go/v2/pkg/common"
)

type TagsGenerator struct {
	NewRelicService
}

// entityGUID is the GUID of the entity with id in domain and of
// entityType, which newrelic_entity_tags imports by: unpadded base64 of
// account|domain|type|id.
func entityGUID(accountID int, domain, entityType, id string) common.EntityGUID {
	return common.EntityGUID(base64.RawStdEncoding.EncodeToString(
		fmt.Appendf(nil, "%d|%s|%s|%s", accountID, domain, entityType, id)))
}

// addEntityTags adds the newrelic_entity_tags of the entity with guid if it
// has tags that can be changed, once however often it is listed.
func (g *TagsGenerator) addEntityTags(client *newrelic.NewRelic, seen map[common.EntityGUID]bool, guid common.EntityGUID, name string) error {
	if seen[guid] {
		return nil
	}
	seen[guid] = true
	tags, err := client.Entities.GetTagsForEntityWithContextMutable(g.Context(), guid)
	if err != nil {
		return fmt.Errorf("tags of %s: %w", name, err)
	}
	if len(tags) == 0 {
		return nil
	}
	g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
		string(guid),
		normalizeResourceName(name),
		"newrelic_entity_tags",
		g.ProviderName))
	return nil
}

func (g *TagsGenerator) createSyntheticsMonitorTagResources(client *newrelic.NewRelic, seen map[common.EntityGUID]bool) error {
	allMonitors, err := client.Synthetics.ListMonitors()
	if err != nil {
		return err
	}
	for _, monitor := range allMonitors {
		guid := entityGUID(g.accountID(), "SYNTH", "MONITOR", monitor.ID)
		if err := g.addEntityTags(client, seen, guid, fmt.Sprintf("%s-%s", monitor.Name, monitor.ID)); err != nil {
			return err
		}
	}
	return nil
}

func (g *TagsGenerator) createAlertConditionTagResources(client *newrelic.NewRelic, seen map[common.EntityGUID]bool) error {
	alertPolicies, err := client.Alerts.ListPolicies(nil)
	if err != nil {
		return err
	}
	for _, alertPolicy := range alertPolicies {
		alertConditions, err := client.Alerts.ListConditions(alertPolicy.ID)
		if err != nil {
			return err
		}
		for _, c := range alertConditions {
			guid := entityGUID(g.accountID(), "AIOPS", "CONDITION", fmt.Sprint(c.ID))
			if err := g.addEntityTags(client, seen, guid, fmt.Sprintf("%s-%d", c.Name, c.ID)); err != nil {
				return err
			}
		}
		nrqlConditions, err := client.Alerts.ListNrqlConditions(alertPolicy.ID)
		if err != nil {
			return err
		}
		for _, c := range nrqlConditions {
			guid := entityGUID(g.accountID(), "AIOPS", "CONDITION", fmt.Sprint(c.ID))
			if err := g.addEntityTags(client, seen, guid, fmt.Sprintf("%s-%d", c.Name, c.ID)); err != nil {
				return err
			}
		}
	}
	return nil
}

// InitResources lists one newrelic_entity_tags per synthetic monitor and
// alert condition with tags, imported by the entity's GUID, which needs
// the account ID.
func (g *TagsGenerator) InitResources() error {
	if g.accountID() == 0 {
		return errors.New("newrelic: tags need the account ID (--account-id or NEW_RELIC_ACCOUNT_ID)")
	}
	client, err := g.Client()
	if err != nil {
		return err
	}
	seen := map[common.EntityGUID]bool{}
	for _, f := range []func(*newrelic.NewRelic, map[common.EntityGUID]bool) error{
		g.createSyntheticsMonitorTagResources,
		g.createAlertConditionTagResources,
	} {
		if err := f(client, seen); err != nil {
			return err
		}
	}
	return nil
}
