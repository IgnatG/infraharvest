// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/workspaces"
	"github.com/aws/aws-sdk-go-v2/service/workspaces/types"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// WorkspacesLister lists the region's WorkSpaces directories, the
// workspaces and the IP access control groups.
type WorkspacesLister struct {
	Service
}

// List lists the directories, then the workspaces, then the IP
// groups. Any list failing fails the service.
func (g *WorkspacesLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := workspaces.NewFromConfig(config)
	if err := g.listDirectories(g.Context(), client); err != nil {
		return err
	}
	if err := g.listWorkspaces(g.Context(), client); err != nil {
		return err
	}
	return g.listIPGroups(g.Context(), client)
}

// listDirectories adds each directory registered with WorkSpaces as an
// aws_workspaces_directory, by its directory ID, whether it has workspaces
// or not. Directories being deregistered, or deregistered, are left out:
// Terraform reads them as gone.
func (g *WorkspacesLister) listDirectories(ctx context.Context, client *workspaces.Client) error {
	p := workspaces.NewDescribeWorkspaceDirectoriesPaginator(client, &workspaces.DescribeWorkspaceDirectoriesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, directory := range page.Directories {
			switch directory.State {
			case types.WorkspaceDirectoryStateDeregistering, types.WorkspaceDirectoryStateDeregistered:
				continue
			}
			id := aws.ToString(directory.DirectoryId)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_workspaces_directory", "aws"))
		}
	}
	return nil
}

// listWorkspaces adds each workspace as an aws_workspaces_workspace, by
// ID, but those terminating or terminated.
func (g *WorkspacesLister) listWorkspaces(ctx context.Context, client *workspaces.Client) error {
	p := workspaces.NewDescribeWorkspacesPaginator(client, &workspaces.DescribeWorkspacesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, ws := range page.Workspaces {
			switch ws.State {
			case types.WorkspaceStateTerminating, types.WorkspaceStateTerminated:
				continue
			}
			id := aws.ToString(ws.WorkspaceId)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_workspaces_workspace", "aws"))
		}
	}
	return nil
}

// listIPGroups adds each IP access control group as an
// aws_workspaces_ip_group, by ID. The SDK has no paginator for it.
func (g *WorkspacesLister) listIPGroups(ctx context.Context, client *workspaces.Client) error {
	return paginateByMarker(func(token *string) (*string, error) {
		out, err := client.DescribeIpGroups(ctx, &workspaces.DescribeIpGroupsInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, group := range out.Result {
			id := aws.ToString(group.GroupId)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_workspaces_ip_group", "aws"))
		}
		return out.NextToken, nil
	})
}
