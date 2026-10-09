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
	"github.com/IgnatG/infraharvest/terraformutils"
	newrelic "github.com/newrelic/newrelic-client-go/v2/newrelic"
	"github.com/newrelic/newrelic-client-go/v2/pkg/region"
)

type NewRelicService struct { //nolint
	terraformutils.Service
}

// Client is a client for the account's region.
func (s *NewRelicService) Client() (*newrelic.NewRelic, error) {
	return newrelic.New(
		newrelic.ConfigPersonalAPIKey(s.GetArgs()["apiKey"].(string)),
		newrelic.ConfigRegion(s.regionName()),
	)
}

// Region is the account's region, whose endpoints the listers call.
func (s *NewRelicService) Region() (*region.Region, error) {
	name, err := region.Parse(s.regionName())
	if err != nil {
		return nil, err
	}
	return region.Get(name)
}

func (s *NewRelicService) regionName() string {
	if r, _ := s.GetArgs()["region"].(string); r != "" {
		return r
	}
	return string(region.Default)
}

// accountID is the account the listers list, 0 if not given.
func (s *NewRelicService) accountID() int {
	id, _ := s.GetArgs()["accountID"].(int)
	return id
}
