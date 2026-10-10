// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/option"
)

// A tree of folders and projects: folders/1 holds folders/2 (with a
// project of its own) and a deleted folder; the organization holds
// folders/1 and a project.
func resourceManager(t *testing.T) *httptest.Server {
	t.Helper()
	projects := map[string][]*cloudresourcemanager.Project{
		"organizations/9": {{ProjectId: "org-app", State: "ACTIVE"}, {ProjectId: "gone", State: "DELETE_REQUESTED"}},
		"folders/1":       {{ProjectId: "team-a", State: "ACTIVE"}},
		"folders/2":       {{ProjectId: "team-b", State: "ACTIVE"}, {ProjectId: "team-a", State: "ACTIVE"}},
		"folders/3":       {{ProjectId: "deleted-folder-project", State: "ACTIVE"}},
	}
	folders := map[string][]*cloudresourcemanager.Folder{
		"organizations/9": {{Name: "folders/1", State: "ACTIVE"}},
		"folders/1":       {{Name: "folders/2", State: "ACTIVE"}, {Name: "folders/3", State: "DELETE_REQUESTED"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parent := r.URL.Query().Get("parent")
		switch r.URL.Path {
		case "/v3/projects":
			// Two pages, to check they are all read.
			list := projects[parent]
			if r.URL.Query().Get("pageToken") == "" && len(list) > 1 {
				_ = json.NewEncoder(w).Encode(cloudresourcemanager.ListProjectsResponse{Projects: list[:1], NextPageToken: "next"})
				return
			}
			if len(list) > 1 {
				list = list[1:]
			}
			_ = json.NewEncoder(w).Encode(cloudresourcemanager.ListProjectsResponse{Projects: list})
		case "/v3/folders":
			_ = json.NewEncoder(w).Encode(cloudresourcemanager.ListFoldersResponse{Folders: folders[parent]})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProjectsUnder(t *testing.T) {
	srv := resourceManager(t)
	svc, err := cloudresourcemanager.NewService(t.Context(), option.WithEndpoint(srv.URL+"/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		organization string
		folders      []string
		want         []string
	}{
		{"9", nil, []string{"org-app", "team-a", "team-b"}},
		{"", []string{"2"}, []string{"team-a", "team-b"}},
		{"organizations/9", []string{"folders/1"}, []string{"org-app", "team-a", "team-b"}},
	} {
		got, err := projectsUnder(t.Context(), svc, tc.organization, tc.folders)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%q %v: got %v, want %v", tc.organization, tc.folders, got, tc.want)
		}
	}
}
