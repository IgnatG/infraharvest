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

package github

import (
	"context"
	"log"
	"strconv"

	"github.com/IgnatG/infraharvest/terraformutils"

	githubAPI "github.com/google/go-github/v92/github"
)

type TeamsGenerator struct {
	GithubService
}

func (g *TeamsGenerator) createTeamsResources(ctx context.Context, team *githubAPI.Team, client *githubAPI.Client) []terraformutils.Resource {
	resources := []terraformutils.Resource{terraformutils.NewSimpleResource(
		strconv.FormatInt(team.GetID(), 10),
		team.GetName(),
		"github_team",
		"github")}
	resources = append(resources, g.createTeamMembersResources(ctx, team, client)...)
	resources = append(resources, g.createTeamRepositoriesResources(ctx, team, client)...)
	return resources
}

func (g *TeamsGenerator) createTeamMembersResources(ctx context.Context, team *githubAPI.Team, client *githubAPI.Client) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	opt := &githubAPI.TeamListTeamMembersOptions{ListOptions: githubAPI.ListOptions{PerPage: 100}}
	for member, err := range client.Teams.ListTeamMembersBySlugIter(ctx, g.Args["owner"].(string), team.GetSlug(), opt) {
		if err != nil {
			log.Println(err)
			break
		}
		resources = append(resources, terraformutils.NewSimpleResource(
			strconv.FormatInt(team.GetID(), 10)+":"+member.GetLogin(),
			team.GetName()+"_"+member.GetLogin(),
			"github_team_membership",
			"github"))
	}
	return resources
}

func (g *TeamsGenerator) createTeamRepositoriesResources(ctx context.Context, team *githubAPI.Team, client *githubAPI.Client) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	opt := &githubAPI.ListOptions{PerPage: 100}
	for repo, err := range client.Teams.ListTeamReposBySlugIter(ctx, g.Args["owner"].(string), team.GetSlug(), opt) {
		if err != nil {
			log.Println(err)
			break
		}
		resources = append(resources, terraformutils.NewSimpleResource(
			strconv.FormatInt(team.GetID(), 10)+":"+repo.GetName(),
			team.GetName()+"_"+repo.GetName(),
			"github_team_repository",
			"github"))
	}
	return resources
}

// InitResources generates TerraformResources from Github API,
func (g *TeamsGenerator) InitResources() error {
	ctx := g.Context()
	client, err := g.createClient()
	if err != nil {
		return err
	}

	opt := &githubAPI.ListOptions{PerPage: 100}

	for team, err := range client.Teams.ListTeamsIter(ctx, g.Args["owner"].(string), opt) {
		if err != nil {
			log.Println(err)
			return nil
		}
		g.Resources = append(g.Resources, g.createTeamsResources(ctx, team, client)...)
	}

	return nil
}
