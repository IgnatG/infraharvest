// Copyright 2018 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package okta

import (
	"context"
	"fmt"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/okta/okta-sdk-golang/v5/okta"
)

type OktaService struct { //nolint
	terraformutils.Service
}

// Client returns an Okta management API client for the org in the service
// arguments, authenticated with its API token.
func (s *OktaService) Client() (context.Context, *okta.APIClient, error) {
	orgName := s.Args["org_name"].(string)
	baseURL := s.Args["base_url"].(string)
	apiToken := s.Args["api_token"].(string)

	orgURL := fmt.Sprintf("https://%v.%v", orgName, baseURL)

	config, err := okta.NewConfiguration(
		okta.WithOrgUrl(orgURL),
		okta.WithToken(apiToken),
	)
	if err != nil {
		return nil, nil, err
	}
	client := okta.NewAPIClient(config)

	return s.Context(), client, nil
}

// allPages returns items plus the items of every page after resp, so a
// listing can be written allPages(client.XAPI.ListX(ctx).Execute()).
func allPages[T any](items []T, resp *okta.APIResponse, err error) ([]T, error) {
	if err != nil {
		return nil, err
	}
	for resp != nil && resp.HasNextPage() {
		var next []T
		resp, err = resp.Next(&next)
		if err != nil {
			return nil, err
		}
		items = append(items, next...)
	}
	return items, nil
}
