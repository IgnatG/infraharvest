// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"golang.org/x/sync/errgroup"
	"google.golang.org/api/cloudresourcemanager/v3"
)

// Projects returns the IDs of the active projects under organization (an
// organization ID, or "") and folders (folder IDs), in their subfolders
// too, sorted. Listing needs resourcemanager.projects.list and
// resourcemanager.folders.list on them, which roles/browser and
// roles/viewer grant.
func Projects(ctx context.Context, organization string, folders []string) ([]string, error) {
	svc, err := cloudresourcemanager.NewService(ctx, clientOptions(ctx)...)
	if err != nil {
		return nil, err
	}
	return projectsUnder(ctx, svc, organization, folders)
}

// folderParallel is how many folders' projects and subfolders are listed
// at once: an organization's folders are listed level by level, each
// level's folders at once.
const folderParallel = 8

func projectsUnder(ctx context.Context, svc *cloudresourcemanager.Service, organization string, folders []string) ([]string, error) {
	var parents []string
	if organization != "" {
		parents = append(parents, "organizations/"+strings.TrimPrefix(organization, "organizations/"))
	}
	for _, f := range folders {
		parents = append(parents, "folders/"+strings.TrimPrefix(f, "folders/"))
	}
	seen, found := map[string]bool{}, map[string]bool{}
	var projects []string
	for len(parents) > 0 {
		var level []string
		for _, parent := range parents {
			if !seen[parent] {
				seen[parent] = true
				level = append(level, parent)
			}
		}
		ids := make([][]string, len(level))
		subfolders := make([][]string, len(level))
		errs := make([]error, len(level))
		// Every parent of the level is listed, whatever the others answer:
		// each failure is kept in errs, and the group's own error is
		// always nil.
		var lists errgroup.Group
		lists.SetLimit(folderParallel)
		for i, parent := range level {
			lists.Go(func() error {
				ids[i], subfolders[i], errs[i] = listParent(ctx, svc, parent)
				return nil
			})
		}
		_ = lists.Wait()
		if err := errors.Join(errs...); err != nil {
			return nil, err
		}
		parents = nil
		for i := range level {
			for _, id := range ids[i] {
				if !found[id] {
					found[id] = true
					projects = append(projects, id)
				}
			}
			parents = append(parents, subfolders[i]...)
		}
	}
	slices.Sort(projects)
	return projects, nil
}

// listParent returns the IDs of the active projects right under parent,
// an organization or a folder, and the names of its active folders.
func listParent(ctx context.Context, svc *cloudresourcemanager.Service, parent string) (projects, folders []string, err error) {
	err = svc.Projects.List().Parent(parent).Pages(ctx, func(page *cloudresourcemanager.ListProjectsResponse) error {
		for _, p := range page.Projects {
			if p.State == "ACTIVE" {
				projects = append(projects, p.ProjectId)
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("projects of %s: %w", parent, err)
	}
	err = svc.Folders.List().Parent(parent).Pages(ctx, func(page *cloudresourcemanager.ListFoldersResponse) error {
		for _, f := range page.Folders {
			if f.State == "ACTIVE" {
				folders = append(folders, f.Name)
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("folders of %s: %w", parent, err)
	}
	return projects, folders, nil
}
