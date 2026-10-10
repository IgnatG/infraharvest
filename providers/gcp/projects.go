// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/api/cloudresourcemanager/v3"
)

// Projects returns the IDs of the active projects under organization (an
// organization ID, or "") and folders (folder IDs), in their subfolders
// too, sorted. Listing needs resourcemanager.projects.list and
// resourcemanager.folders.list on them, which roles/browser and
// roles/viewer grant.
func Projects(ctx context.Context, organization string, folders []string) ([]string, error) {
	svc, err := cloudresourcemanager.NewService(ctx, clientOptions()...)
	if err != nil {
		return nil, err
	}
	return projectsUnder(ctx, svc, organization, folders)
}

func projectsUnder(ctx context.Context, svc *cloudresourcemanager.Service, organization string, folders []string) ([]string, error) {
	var parents []string
	if organization != "" {
		parents = append(parents, "organizations/"+strings.TrimPrefix(organization, "organizations/"))
	}
	for _, f := range folders {
		parents = append(parents, "folders/"+strings.TrimPrefix(f, "folders/"))
	}
	seen := map[string]bool{}
	var projects []string
	for len(parents) > 0 {
		parent := parents[0]
		parents = parents[1:]
		if seen[parent] {
			continue
		}
		seen[parent] = true
		err := svc.Projects.List().Parent(parent).Pages(ctx, func(page *cloudresourcemanager.ListProjectsResponse) error {
			for _, p := range page.Projects {
				if p.State == "ACTIVE" && !slices.Contains(projects, p.ProjectId) {
					projects = append(projects, p.ProjectId)
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("projects of %s: %w", parent, err)
		}
		err = svc.Folders.List().Parent(parent).Pages(ctx, func(page *cloudresourcemanager.ListFoldersResponse) error {
			for _, f := range page.Folders {
				if f.State == "ACTIVE" {
					parents = append(parents, f.Name)
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("folders of %s: %w", parent, err)
		}
	}
	slices.Sort(projects)
	return projects, nil
}
