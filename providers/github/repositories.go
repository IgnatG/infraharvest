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

type RepositoriesGenerator struct {
	GithubService
}

// Generate TerraformResources from github API,
func (g *RepositoriesGenerator) InitResources() error {
	ctx := g.Context()
	client, err := g.createClient()
	if err != nil {
		return err
	}

	opt := &githubAPI.RepositoryListByOrgOptions{
		ListOptions: githubAPI.ListOptions{PerPage: 100},
	}
	// list all repositories for the authenticated user
	for repo, err := range client.Repositories.ListByOrgIter(ctx, g.GetArgs()["owner"].(string), opt) {
		if err != nil {
			log.Println(err)
			return nil
		}
		resource := terraformutils.NewSimpleResource(
			repo.GetName(),
			repo.GetName(),
			"github_repository",
			"github")

		g.Resources = append(g.Resources, resource)
		g.Resources = append(g.Resources, g.createRepositoryWebhookResources(ctx, client, repo)...)
		g.Resources = append(g.Resources, g.createRepositoryBranchProtectionResources(ctx, client, repo)...)
		g.Resources = append(g.Resources, g.createRepositoryCollaboratorResources(ctx, client, repo)...)
		g.Resources = append(g.Resources, g.createRepositoryDeployKeyResources(ctx, client, repo)...)
	}

	return nil
}

func (g *RepositoriesGenerator) createRepositoryWebhookResources(ctx context.Context, client *githubAPI.Client, repo *githubAPI.Repository) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	opt := &githubAPI.ListOptions{PerPage: 100}
	for hook, err := range client.Repositories.ListHooksIter(ctx, g.GetArgs()["owner"].(string), repo.GetName(), opt) {
		if err != nil {
			log.Println(err)
			break
		}
		resources = append(resources, terraformutils.NewResource(
			strconv.FormatInt(hook.GetID(), 10),
			repo.GetName()+"_"+strconv.FormatInt(hook.GetID(), 10),
			"github_repository_webhook",
			"github",
			map[string]string{
				"repository": repo.GetName(),
			}))
	}
	return resources
}

func (g *RepositoriesGenerator) createRepositoryBranchProtectionResources(ctx context.Context, client *githubAPI.Client, repo *githubAPI.Repository) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	opt := &githubAPI.BranchListOptions{ListOptions: githubAPI.ListOptions{PerPage: 100}}
	for branch, err := range client.Repositories.ListBranchesIter(ctx, g.GetArgs()["owner"].(string), repo.GetName(), opt) {
		if err != nil {
			log.Println(err)
			break
		}
		if branch.GetProtected() {
			resources = append(resources, terraformutils.NewSimpleResource(
				repo.GetName()+":"+branch.GetName(),
				repo.GetName()+"_"+branch.GetName(),
				"github_branch_protection",
				"github"))
		}
	}
	return resources
}

func (g *RepositoriesGenerator) createRepositoryCollaboratorResources(ctx context.Context, client *githubAPI.Client, repo *githubAPI.Repository) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	opt := &githubAPI.ListCollaboratorsOptions{ListOptions: githubAPI.ListOptions{PerPage: 100}}
	for collaborator, err := range client.Repositories.ListCollaboratorsIter(ctx, g.GetArgs()["owner"].(string), repo.GetName(), opt) {
		if err != nil {
			log.Println(err)
			break
		}
		resources = append(resources, terraformutils.NewSimpleResource(
			repo.GetName()+":"+collaborator.GetLogin(),
			repo.GetName()+":"+collaborator.GetLogin(),
			"github_repository_collaborator",
			"github"))
	}
	return resources
}

func (g *RepositoriesGenerator) createRepositoryDeployKeyResources(ctx context.Context, client *githubAPI.Client, repo *githubAPI.Repository) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	opt := &githubAPI.ListOptions{PerPage: 100}
	for key, err := range client.Repositories.ListKeysIter(ctx, g.GetArgs()["owner"].(string), repo.GetName(), opt) {
		if err != nil {
			log.Println(err)
			break
		}
		resources = append(resources, terraformutils.NewSimpleResource(
			repo.GetName()+":"+strconv.FormatInt(key.GetID(), 10),
			repo.GetName()+":"+key.GetTitle(),
			"github_repository_deploy_key",
			"github"))
	}
	return resources
}
