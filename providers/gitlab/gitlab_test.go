// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gitlab

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"github.com/IgnatG/infraharvest/terraformutils"
)

// listed is the part of a resource a lister decides.
type listed struct {
	ID, Name, Type string
}

func summarize(resources []terraformutils.Resource) []listed {
	var out []listed
	for _, r := range resources {
		out = append(out, listed{r.InstanceState.ID, r.RawName, r.InstanceInfo.Type})
	}
	return out
}

// fakeGitLab serves the GitLab REST API from pages, keyed by escaped request
// path. A path with several pages answers with X-Next-Page until the last one.
func fakeGitLab(t *testing.T, pages map[string][]string, check func(r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Private-Token"); got != "secret" {
			t.Errorf("%s: Private-Token = %q, want the configured token", r.URL, got)
		}
		if check != nil {
			check(r)
		}
		bodies, ok := pages[r.URL.EscapedPath()]
		if !ok {
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			var err error
			if page, err = strconv.Atoi(p); err != nil || page < 1 || page > len(bodies) {
				t.Errorf("%s: bad page %q", r.URL, p)
				http.NotFound(w, r)
				return
			}
		}
		if page < len(bodies) {
			w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, bodies[page-1])
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGroupGenerator(t *testing.T) {
	srv := fakeGitLab(t, map[string][]string{
		"/api/v4/groups/acme": {`{"id":7,"full_path":"acme/platform"}`},
		"/api/v4/groups/7/variables": {
			`[{"key":"TOKEN","environment_scope":"*"}]`,
			`[{"key":"TOKEN","environment_scope":"production"}]`,
		},
		"/api/v4/groups/7/members": {
			`[{"id":5,"username":"alice"}]`,
			`[{"id":6,"username":"bob"}]`,
		},
	}, nil)

	g := &GroupGenerator{}
	g.SetArgs(map[string]interface{}{"group": "acme", "token": "secret", "base_url": srv.URL + "/api/v4/"})
	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}

	got := summarize(g.GetResources())
	want := []listed{
		{"7", "7___acme__platform", "gitlab_group"},
		{"7:TOKEN:*", "7___acme__platform___TOKEN___*", "gitlab_group_variable"},
		{"7:TOKEN:production", "7___acme__platform___TOKEN___production", "gitlab_group_variable"},
		{"7:5", "7___acme__platform___alice", "gitlab_group_membership"},
		{"7:6", "7___acme__platform___bob", "gitlab_group_membership"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestProjectGenerator(t *testing.T) {
	srv := fakeGitLab(t, map[string][]string{
		"/api/v4/groups/acme/projects": {
			`[{"id":11,"path_with_namespace":"acme/api"}]`,
			`[{"id":12,"path_with_namespace":"acme/web"}]`,
		},
		"/api/v4/projects/11/variables": {
			`[{"key":"TOKEN","environment_scope":"*"}]`,
			`[{"key":"TOKEN","environment_scope":"production"}]`,
		},
		"/api/v4/projects/11/protected_branches": {`[{"id":1,"name":"main"}]`, `[{"id":2,"name":"release/*"}]`},
		"/api/v4/projects/11/protected_tags":     {`[{"name":"v*"}]`},
		"/api/v4/projects/11/members":            {`[{"id":5,"username":"alice"}]`},
		"/api/v4/projects/12/variables":          {`[]`},
		"/api/v4/projects/12/protected_branches": {`[]`},
		"/api/v4/projects/12/protected_tags":     {`[]`},
		"/api/v4/projects/12/members":            {`[]`},
	}, func(r *http.Request) {
		if r.URL.Path == "/api/v4/groups/acme/projects" && r.URL.Query().Get("per_page") != "100" {
			t.Errorf("%s: want per_page=100", r.URL)
		}
	})

	// A base URL without the API path gets api/v4/ appended.
	g := &ProjectGenerator{}
	g.SetArgs(map[string]interface{}{"group": "acme", "token": "secret", "base_url": srv.URL})
	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}

	got := summarize(g.GetResources())
	want := []listed{
		{"11", "11___acme__api", "gitlab_project"},
		{"11:TOKEN:*", "11___acme__api___TOKEN___*", "gitlab_project_variable"},
		{"11:TOKEN:production", "11___acme__api___TOKEN___production", "gitlab_project_variable"},
		{"11:main", "11___acme__api___main", "gitlab_branch_protection"},
		{"11:release/*", "11___acme__api___release/*", "gitlab_branch_protection"},
		{"11:v*", "11___acme__api___v*", "gitlab_tag_protection"},
		{"11:5", "11___acme__api___alice", "gitlab_project_membership"},
		{"12", "12___acme__web", "gitlab_project"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestDefaultBaseURL(t *testing.T) {
	for _, baseURL := range []string{"", gitLabDefaultURL} {
		client, err := newClient("secret", baseURL)
		if err != nil {
			t.Fatal(err)
		}
		if got := client.BaseURL().String(); got != gitLabDefaultURL {
			t.Errorf("base URL %q: client uses %q, want %q", baseURL, got, gitLabDefaultURL)
		}
	}
}
