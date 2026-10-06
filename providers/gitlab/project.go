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

package gitlab

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/IgnatG/infraharvest/terraformutils"
	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

type ProjectGenerator struct {
	GitLabService
}

// Generate TerraformResources from gitlab API,
func (g *ProjectGenerator) InitResources() error {
	ctx := context.Background()
	client, err := g.createClient()
	if err != nil {
		return err
	}

	group := g.Args["group"].(string)
	g.Resources = append(g.Resources, createProjects(ctx, client, group)...)

	return nil
}

func createProjects(ctx context.Context, client *gitlab.Client, group string) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	opt := &gitlab.ListGroupProjectsOptions{
		ListOptions: gitlab.ListOptions{
			PerPage: 100,
		},
	}
	projects, err := listAll(ctx, func(options ...gitlab.RequestOptionFunc) ([]*gitlab.Project, *gitlab.Response, error) {
		return client.Groups.ListGroupProjects(group, opt, options...)
	})
	if err != nil {
		log.Println(err)
		return nil
	}

	for _, project := range projects {
		resource := terraformutils.NewSimpleResource(
			strconv.FormatInt(project.ID, 10),
			getProjectResourceName(project),
			"gitlab_project",
			"gitlab")

		resources = append(resources, resource)
		resources = append(resources, createProjectVariables(ctx, client, project)...)
		resources = append(resources, createBranchProtections(ctx, client, project)...)
		resources = append(resources, createTagProtections(ctx, client, project)...)
		resources = append(resources, createProjectMembership(ctx, client, project)...)
	}
	return resources
}

func createProjectVariables(ctx context.Context, client *gitlab.Client, project *gitlab.Project) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	projectVariables, err := listAll(ctx, func(options ...gitlab.RequestOptionFunc) ([]*gitlab.ProjectVariable, *gitlab.Response, error) {
		return client.ProjectVariables.ListVariables(project.ID, &gitlab.ListProjectVariablesOptions{}, options...)
	})
	if err != nil {
		log.Println(err)
		return nil
	}

	for _, projectVariable := range projectVariables {
		resource := terraformutils.NewSimpleResource(
			fmt.Sprintf("%d:%s:%s", project.ID, projectVariable.Key, projectVariable.EnvironmentScope),
			fmt.Sprintf("%s___%s___%s", getProjectResourceName(project), projectVariable.Key, projectVariable.EnvironmentScope),
			"gitlab_project_variable",
			"gitlab")

		resources = append(resources, resource)
	}
	return resources
}

func createBranchProtections(ctx context.Context, client *gitlab.Client, project *gitlab.Project) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	protectedBranches, err := listAll(ctx, func(options ...gitlab.RequestOptionFunc) ([]*gitlab.ProtectedBranch, *gitlab.Response, error) {
		return client.ProtectedBranches.ListProtectedBranches(project.ID, &gitlab.ListProtectedBranchesOptions{}, options...)
	})
	if err != nil {
		log.Println(err)
		return nil
	}

	for _, protectedBranch := range protectedBranches {
		resource := terraformutils.NewSimpleResource(
			fmt.Sprintf("%d:%s", project.ID, protectedBranch.Name),
			fmt.Sprintf("%s___%s", getProjectResourceName(project), protectedBranch.Name),
			"gitlab_branch_protection",
			"gitlab")

		resources = append(resources, resource)
	}
	return resources
}

func createTagProtections(ctx context.Context, client *gitlab.Client, project *gitlab.Project) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	protectedTags, err := listAll(ctx, func(options ...gitlab.RequestOptionFunc) ([]*gitlab.ProtectedTag, *gitlab.Response, error) {
		return client.ProtectedTags.ListProtectedTags(project.ID, &gitlab.ListProtectedTagsOptions{}, options...)
	})
	if err != nil {
		log.Println(err)
		return nil
	}

	for _, protectedTag := range protectedTags {
		resource := terraformutils.NewSimpleResource(
			fmt.Sprintf("%d:%s", project.ID, protectedTag.Name),
			fmt.Sprintf("%s___%s", getProjectResourceName(project), protectedTag.Name),
			"gitlab_tag_protection",
			"gitlab")

		resources = append(resources, resource)
	}
	return resources
}

func createProjectMembership(ctx context.Context, client *gitlab.Client, project *gitlab.Project) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	projectMembers, err := listAll(ctx, func(options ...gitlab.RequestOptionFunc) ([]*gitlab.ProjectMember, *gitlab.Response, error) {
		return client.ProjectMembers.ListProjectMembers(project.ID, &gitlab.ListProjectMembersOptions{}, options...)
	})
	if err != nil {
		log.Println(err)
		return nil
	}

	for _, projectMember := range projectMembers {
		resource := terraformutils.NewSimpleResource(
			fmt.Sprintf("%d:%d", project.ID, projectMember.ID),
			fmt.Sprintf("%s___%s", getProjectResourceName(project), projectMember.Username),
			"gitlab_project_membership",
			"gitlab")

		resources = append(resources, resource)
	}
	return resources
}

func getProjectResourceName(project *gitlab.Project) string {
	return fmt.Sprintf("%d___%s", project.ID, strings.ReplaceAll(project.PathWithNamespace, "/", "__"))
}
