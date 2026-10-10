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

type GroupGenerator struct {
	GitLabService
}

// Generate TerraformResources from gitlab API,
func (g *GroupGenerator) InitResources() error {
	ctx := g.Context()
	client, err := g.createClient()
	if err != nil {
		return err
	}

	group := g.Args["group"].(string)
	g.Resources = append(g.Resources, createGroups(ctx, client, group)...)

	return nil
}

func createGroups(ctx context.Context, client *gitlab.Client, groupID string) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	group, _, err := client.Groups.GetGroup(groupID, nil, gitlab.WithContext(ctx))
	if err != nil {
		log.Println(err)
		return nil
	}

	resource := terraformutils.NewSimpleResource(
		strconv.FormatInt(group.ID, 10),
		getGroupResourceName(group),
		"gitlab_group",
		"gitlab")

	resources = append(resources, resource)
	resources = append(resources, createGroupVariables(ctx, client, group)...)
	resources = append(resources, createGroupMembership(ctx, client, group)...)

	return resources
}

func createGroupVariables(ctx context.Context, client *gitlab.Client, group *gitlab.Group) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	groupVariables, err := listAll(ctx, func(options ...gitlab.RequestOptionFunc) ([]*gitlab.GroupVariable, *gitlab.Response, error) {
		return client.GroupVariables.ListVariables(group.ID, &gitlab.ListGroupVariablesOptions{}, options...)
	})
	if err != nil {
		log.Println(err)
		return nil
	}

	for _, groupVariable := range groupVariables {
		resource := terraformutils.NewSimpleResource(
			fmt.Sprintf("%d:%s:%s", group.ID, groupVariable.Key, groupVariable.EnvironmentScope),
			fmt.Sprintf("%s___%s___%s", getGroupResourceName(group), groupVariable.Key, groupVariable.EnvironmentScope),
			"gitlab_group_variable",
			"gitlab")

		resources = append(resources, resource)
	}
	return resources
}

func createGroupMembership(ctx context.Context, client *gitlab.Client, group *gitlab.Group) []terraformutils.Resource {
	resources := []terraformutils.Resource{}
	groupMembers, err := listAll(ctx, func(options ...gitlab.RequestOptionFunc) ([]*gitlab.GroupMember, *gitlab.Response, error) {
		return client.Groups.ListGroupMembers(group.ID, &gitlab.ListGroupMembersOptions{}, options...)
	})
	if err != nil {
		log.Println(err)
		return nil
	}

	for _, groupMember := range groupMembers {
		resource := terraformutils.NewSimpleResource(
			fmt.Sprintf("%d:%d", group.ID, groupMember.ID),
			fmt.Sprintf("%s___%s", getGroupResourceName(group), groupMember.Username),
			"gitlab_group_membership",
			"gitlab")

		resources = append(resources, resource)
	}
	return resources
}

func getGroupResourceName(group *gitlab.Group) string {
	return fmt.Sprintf("%d___%s", group.ID, strings.ReplaceAll(group.FullPath, "/", "__"))
}
