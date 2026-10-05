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

type InlineHookGenerator struct {
	OktaService
}

func (g InlineHookGenerator) createResources(inlineHookList []okta.InlineHook) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, inlineHook := range inlineHookList {

		resources = append(resources, terraformutils.NewSimpleResource(
			inlineHook.GetId(),
			"inline_hook_"+inlineHook.GetName(),
			"okta_inline_hook",
			"okta"))
	}
	return resources
}

func (g *InlineHookGenerator) InitResources() error {
	ctx, client, e := g.Client()
	if e != nil {
		return e
	}

	output, err := allPages(client.InlineHookAPI.ListInlineHooks(ctx).Execute())
	if err != nil {
		return err
	}

	g.Resources = g.createResources(output)
	return nil
}
