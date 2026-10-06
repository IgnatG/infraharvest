// Copyright 2021 The Terraformer Authors.
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
	"log"

	"github.com/IgnatG/infraharvest/terraformutils"
)

type FactorGenerator struct {
	OktaService
}

// orgFactor is a factor of the org factor API, which okta-sdk-golang/v5 has
// no call for.
type orgFactor struct {
	ID         string `json:"id"`
	FactorType string `json:"factorType"`
	Status     string `json:"status"`
}

// hotpFactorProfile is a profile of the org's HOTP factor.
type hotpFactorProfile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (g FactorGenerator) createResources(factorList []orgFactor, hotpFactorProfiles []hotpFactorProfile) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, factor := range factorList {
		if factor.Status == "ACTIVE" {
			resources = append(resources, terraformutils.NewResource(
				factor.ID,
				"factor_"+normalizeResourceNameWithRandom(factor.ID),
				"okta_factor",
				"okta",
				map[string]string{
					"provider_id": factor.ID,
				}))

			if factor.FactorType == "token:hotp" {
				for _, factorProfile := range hotpFactorProfiles {
					resources = append(resources, terraformutils.NewResource(
						factorProfile.ID,
						"factor_totp_"+normalizeResourceNameWithRandom(factorProfile.Name),
						"okta_factor_totp",
						"okta",
						map[string]string{}))
				}
			}
		}
	}
	return resources
}

func (g *FactorGenerator) InitResources() error {
	ctx, client, err := g.Client()
	if err != nil {
		return err
	}
	raw, err := newRawClient(client)
	if err != nil {
		return err
	}

	factors, err := getListFactors(ctx, raw)
	if err != nil {
		return err
	}

	// As before, a failure to list the HOTP profiles only leaves them out.
	var hotpFactorProfiles []hotpFactorProfile
	if hasActiveHotpFactor(factors) {
		hotpFactorProfiles, err = getHotpFactorProfiles(ctx, raw)
		if err != nil {
			log.Printf("okta: listing HOTP factor profiles: %v", err)
		}
	}

	g.Resources = g.createResources(factors, hotpFactorProfiles)
	return nil
}

func hasActiveHotpFactor(factors []orgFactor) bool {
	for _, factor := range factors {
		if factor.Status == "ACTIVE" && factor.FactorType == "token:hotp" {
			return true
		}
	}
	return false
}

func getListFactors(ctx context.Context, client *rawClient) ([]orgFactor, error) {
	return rawList[orgFactor](ctx, client, "/api/v1/org/factors")
}

func getHotpFactorProfiles(ctx context.Context, client *rawClient) ([]hotpFactorProfile, error) {
	return rawList[hotpFactorProfile](ctx, client, "/api/v1/org/factors/hotp/profiles")
}
