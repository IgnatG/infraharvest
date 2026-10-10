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
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"

	"github.com/IgnatG/infraharvest/terraformutils"

	githubAPI "github.com/google/go-github/v92/github"
)

// classicProjectsMediaType is the media type the Projects (classic) API
// answers with.
const classicProjectsMediaType = "application/vnd.github.inertia-preview+json"

type OrganizationProjectGenerator struct {
	GithubService
}

// Generate TerraformResources from Github API,
func (g *OrganizationProjectGenerator) InitResources() error {
	ctx := g.Context()
	client, err := g.createClient()
	if err != nil {
		return err
	}

	owner := g.Args["owner"].(string)
	g.Resources = append(g.Resources, createOrganizationProjects(ctx, client, owner)...)

	return nil
}

// classicProject is the part of a Projects (classic) project the lister reads.
type classicProject struct {
	ID int64 `json:"id"`
}

// createOrganizationProjects lists the organization's Projects (classic),
// which github_organization_project manages. go-github only covers Projects
// (the new ones) now, so this calls GET /orgs/{org}/projects directly.
func createOrganizationProjects(ctx context.Context, client *githubAPI.Client, owner string) []terraformutils.Resource {
	resources := []terraformutils.Resource{}

	page := 1
	for {
		projects, nextPage, err := listOrganizationClassicProjects(ctx, client, owner, page)
		if err != nil {
			log.Println(err)
			return nil
		}

		for _, project := range projects {
			resources = append(resources, terraformutils.NewSimpleResource(
				strconv.FormatInt(project.ID, 10),
				strconv.FormatInt(project.ID, 10),
				"github_organization_project",
				"github"))
		}

		if nextPage == 0 {
			break
		}
		page = nextPage
	}
	return resources
}

func listOrganizationClassicProjects(ctx context.Context, client *githubAPI.Client, owner string, page int) ([]classicProject, int, error) {
	u := fmt.Sprintf("orgs/%s/projects?per_page=100&page=%d", url.PathEscape(owner), page)
	req, err := client.NewRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", classicProjectsMediaType)

	var projects []classicProject
	resp, err := client.Do(req, &projects)
	if err != nil {
		return nil, 0, err
	}
	return projects, resp.NextPage, nil
}
