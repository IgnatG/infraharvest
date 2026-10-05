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

package okta

import (
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/okta/okta-sdk-golang/v5/okta"
)

type AppUserSchemaPropertyGenerator struct {
	OktaService
}

func (g AppUserSchemaPropertyGenerator) createResources(appUserSchema *okta.UserSchema, appID string) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	custom, base, err := userSchemaPropertyNames(appUserSchema)
	if err != nil {
		return nil, err
	}
	for _, index := range custom {
		resources = append(resources, terraformutils.NewResource(
			index,
			normalizeResourceName(appID)+"_property_"+normalizeResourceName(index),
			"okta_app_user_schema_property",
			"okta",
			map[string]string{
				"app_id": appID,
				"index":  index,
			}))
	}

	for _, index := range base {
		resources = append(resources, terraformutils.NewResource(
			index,
			normalizeResourceName(appID)+"_property_"+normalizeResourceName(index),
			"okta_app_user_base_schema_property",
			"okta",
			map[string]string{
				"app_id": appID,
				"index":  index,
			}))
	}
	return resources, nil
}

func (g *AppUserSchemaPropertyGenerator) InitResources() error {
	var resources []terraformutils.Resource
	ctx, client, e := g.Client()
	if e != nil {
		return e
	}

	apps, err := getAllApplications(ctx, client)
	if err != nil {
		return err
	}

	for _, app := range apps {
		appUserSchema, _, err := client.SchemaAPI.GetApplicationUserSchema(ctx, app.ID).Execute()
		if err != nil {
			return err
		}

		appResources, err := g.createResources(appUserSchema, app.ID)
		if err != nil {
			return err
		}
		resources = append(resources, appResources...)
	}
	g.Resources = resources
	return nil
}
