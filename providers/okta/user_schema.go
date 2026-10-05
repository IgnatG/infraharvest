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
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/okta/okta-sdk-golang/v5/okta"
)

type UserSchemaPropertyGenerator struct {
	OktaService
}

func (g UserSchemaPropertyGenerator) createResources(userSchema *okta.UserSchema, userTypeID string, userTypeName string) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	custom, base, err := userSchemaPropertyNames(userSchema)
	if err != nil {
		return nil, err
	}
	for _, index := range custom {
		resources = append(resources, terraformutils.NewResource(
			index,
			normalizeResourceName(userTypeName)+"_property_"+normalizeResourceName(index),
			"okta_user_schema_property",
			"okta",
			map[string]string{
				"index":     index,
				"user_type": userTypeID,
			}))
	}

	for _, index := range base {
		resources = append(resources, terraformutils.NewResource(
			index,
			normalizeResourceName(userTypeName)+"_property_"+normalizeResourceName(index),
			"okta_user_base_schema_property",
			"okta",
			map[string]string{
				"index":     index,
				"user_type": userTypeID,
			}))
	}
	return resources, nil
}

func (g *UserSchemaPropertyGenerator) InitResources() error {
	var resources []terraformutils.Resource
	ctx, client, e := g.Client()
	if e != nil {
		return e
	}

	userTypes, err := getUserTypes(ctx, client)
	if err != nil {
		return err
	}

	for _, userType := range userTypes {
		schemaID := getUserTypeSchemaID(userType)
		if schemaID != "" {
			schema, _, err := client.SchemaAPI.GetUserSchema(ctx, schemaID).Execute()
			if err != nil {
				return err
			}

			name := userTypeName(userType)
			userTypeID := "default"
			if name != "user" {
				userTypeID = userType.GetId()
			}

			typeResources, err := g.createResources(schema, userTypeID, name)
			if err != nil {
				return err
			}
			resources = append(resources, typeResources...)
		}
	}

	g.Resources = resources
	return nil
}

func getUserTypes(ctx context.Context, client *okta.APIClient) ([]okta.UserType, error) {
	return allPages(client.UserTypeAPI.ListUserTypes(ctx).Execute())
}

// userTypeName returns the user type's name. The SDK's UserType model only
// declares the ID, so the rest of the user type is in AdditionalProperties.
func userTypeName(ut okta.UserType) string {
	name, _ := ut.AdditionalProperties["name"].(string)
	return name
}

func getUserTypeSchemaID(ut okta.UserType) string {
	fm, ok := ut.AdditionalProperties["_links"].(map[string]interface{})
	if ok {
		sm, ok := fm["schema"].(map[string]interface{})
		if ok {
			href, ok := sm["href"].(string)
			if ok {
				u, err := url.Parse(href)
				if err != nil {
					return ""
				}
				return strings.TrimPrefix(u.EscapedPath(), "/api/v1/meta/schemas/user/")
			}
		}
	}
	return ""
}

// userSchemaPropertyNames returns the names of the custom and of the base
// properties of a user or app user schema, sorted. The SDK models base
// properties as a struct with a field per well-known Okta user property and
// the others in AdditionalProperties, so the names are read back from its
// JSON form.
func userSchemaPropertyNames(schema *okta.UserSchema) (custom, base []string, err error) {
	if schema == nil || schema.Definitions == nil {
		return nil, nil, nil
	}
	if c := schema.Definitions.Custom; c != nil && c.Properties != nil {
		for name := range *c.Properties {
			custom = append(custom, name)
		}
	}
	if b := schema.Definitions.Base; b != nil && b.Properties != nil {
		raw, err := json.Marshal(b.Properties)
		if err != nil {
			return nil, nil, err
		}
		var properties map[string]json.RawMessage
		if err := json.Unmarshal(raw, &properties); err != nil {
			return nil, nil, err
		}
		for name := range properties {
			base = append(base, name)
		}
	}
	sort.Strings(custom)
	sort.Strings(base)
	return custom, base, nil
}
