// Copyright 2018 The Terraformer Authors.
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

package commercetools

import (
	"fmt"

	"github.com/IgnatG/infraharvest/providers/commercetools/connectivity"
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/labd/commercetools-go-sdk/platform"
)

// pageSize is the largest page the commercetools query endpoints return.
const pageSize = 500

type CommercetoolsService struct { //nolint
	terraformutils.Service
}

// client returns a client for the project the provider was configured with.
func (s *CommercetoolsService) client() (*platform.ByProjectKeyRequestBuilder, error) {
	args := s.GetArgs()
	cfg := connectivity.Config{
		ClientID:     args["client_id"].(string),
		ClientSecret: args["client_secret"].(string),
		ClientScope:  args["client_scope"].(string),
		ProjectKey:   args["project_key"].(string),
		TokenURL:     args["token_url"].(string) + "/oauth/token",
		BaseURL:      args["base_url"].(string),
	}
	return cfg.NewClient()
}

// listAll pages through a query endpoint sorted by id. commercetools caps
// the offset at 10,000, so each page asks for the ids after the last one
// seen instead. query gets the where predicates for the next page.
func listAll[T any](query func(where []string) ([]T, error), id func(T) string) ([]T, error) {
	var all []T
	var where []string
	for {
		page, err := query(where)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < pageSize {
			return all, nil
		}
		where = []string{fmt.Sprintf("id > %q", id(page[len(page)-1]))}
	}
}

// sortByID is the sort expression listAll relies on.
var sortByID = []string{"id asc"}

// deref returns the value s points to, or "" when s is nil.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
