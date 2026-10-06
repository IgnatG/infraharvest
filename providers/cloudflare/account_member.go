// Copyright 2020 The Terraformer Authors.
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

package cloudflare

import (
	"errors"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/accounts"
	"github.com/cloudflare/cloudflare-go/v7/shared"
)

type AccountMemberGenerator struct {
	CloudflareService
}

// accountMemberResources records an account's members as
// cloudflare_account_member, imported as <account_id>/<member_id>.
func accountMemberResources(accountID string, members []shared.Member) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, member := range members {
		resources = append(resources, terraformutils.NewResource(
			accountID+"/"+member.ID,
			member.ID,
			"cloudflare_account_member",
			"cloudflare",
			map[string]string{
				"email_address": member.User.Email,
			}))
	}
	return resources
}

func (g *AccountMemberGenerator) InitResources() error {
	accountID := g.accountID()
	if accountID == "" {
		return errors.New("cloudflare: account_member needs CLOUDFLARE_ACCOUNT_ID")
	}
	client, err := g.initializeAPI()
	if err != nil {
		return err
	}
	members, err := collect(client.Accounts.Members.ListAutoPaging(g.Context(), accounts.MemberListParams{AccountID: cloudflare.F(accountID)}))
	if err != nil {
		return err
	}
	g.Resources = append(g.Resources, accountMemberResources(accountID, members)...)

	return nil
}
