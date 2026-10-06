package github

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/IgnatG/infraharvest/terraformutils"
)

// fakeGitHub serves the REST API under /api/v3, the way GitHub Enterprise
// does. Each path has one JSON array per page; every page but the last
// links to the next one, as GitHub does. Paths it does not know answer [].
type fakeGitHub struct {
	pages map[string][]string

	mu      sync.Mutex
	headers map[string]http.Header // last request headers, by path
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v3")
	f.mu.Lock()
	f.headers[path] = r.Header.Clone()
	f.mu.Unlock()

	pages, ok := f.pages[path]
	if !ok {
		pages = []string{"[]"}
	}
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		page, _ = strconv.Atoi(p)
	}
	if page < 1 || page > len(pages) {
		http.Error(w, "no such page", http.StatusNotFound)
		return
	}
	if page < len(pages) {
		next := *r.URL
		q := next.Query()
		q.Set("page", strconv.Itoa(page+1))
		next.RawQuery = q.Encode()
		w.Header().Set("Link", fmt.Sprintf(`<http://%s%s>; rel="next"`, r.Host, next.RequestURI()))
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, pages[page-1])
}

func (f *fakeGitHub) header(path, name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.headers[path].Get(name)
}

func newFakeGitHub(t *testing.T, pages map[string][]string) (*fakeGitHub, string) {
	t.Helper()
	f := &fakeGitHub{pages: pages, headers: map[string]http.Header{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL + "/"
}

type generator interface {
	SetArgs(map[string]interface{})
	InitResources() error
	GetResources() []terraformutils.Resource
}

func runGenerator(t *testing.T, g generator, baseURL string) []string {
	t.Helper()
	g.SetArgs(map[string]interface{}{
		"owner":           "acme",
		"token":           "secret",
		"base_url":        baseURL,
		"app_id":          int64(0),
		"installation_id": int64(0),
		"pem":             "",
	})
	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range g.GetResources() {
		line := r.InstanceInfo.Type + " " + r.InstanceState.ID + " " + r.RawName
		if repo := r.InstanceState.Attributes["repository"]; repo != "" {
			line += " repository=" + repo
		}
		got = append(got, line)
	}
	return got
}

func assertResources(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("resources:\n got  %q\n want %q", got, want)
	}
}

func TestRepositoriesListEveryPage(t *testing.T) {
	f, baseURL := newFakeGitHub(t, map[string][]string{
		"/orgs/acme/repos":              {`[{"name":"api"}]`, `[{"name":"web"}]`},
		"/repos/acme/api/hooks":         {`[{"id":1}]`, `[{"id":2}]`},
		"/repos/acme/api/branches":      {`[{"name":"main","protected":true}]`, `[{"name":"dev","protected":false}]`},
		"/repos/acme/api/collaborators": {`[{"login":"alice"}]`, `[{"login":"bob"}]`},
		"/repos/acme/api/keys":          {`[{"id":7,"title":"deploy"}]`},
	})

	got := runGenerator(t, &RepositoriesGenerator{}, baseURL)
	assertResources(t, got, []string{
		"github_repository api api",
		"github_repository_webhook 1 api_1 repository=api",
		"github_repository_webhook 2 api_2 repository=api",
		"github_branch_protection api:main api_main",
		"github_repository_collaborator api:alice api:alice",
		"github_repository_collaborator api:bob api:bob",
		"github_repository_deploy_key api:7 api:deploy",
		"github_repository web web",
	})
	if auth := f.header("/orgs/acme/repos", "Authorization"); auth != "Bearer secret" {
		t.Errorf("Authorization = %q, want the token", auth)
	}
}

func TestTeamsListEveryPage(t *testing.T) {
	_, baseURL := newFakeGitHub(t, map[string][]string{
		"/orgs/acme/teams":              {`[{"id":10,"name":"Core","slug":"core"}]`, `[{"id":11,"name":"Ops","slug":"ops"}]`},
		"/orgs/acme/teams/core/members": {`[{"login":"alice"}]`, `[{"login":"bob"}]`},
		"/orgs/acme/teams/core/repos":   {`[{"name":"api"}]`},
	})

	got := runGenerator(t, &TeamsGenerator{}, baseURL)
	assertResources(t, got, []string{
		"github_team 10 Core",
		"github_team_membership 10:alice Core_alice",
		"github_team_membership 10:bob Core_bob",
		"github_team_repository 10:api Core_api",
		"github_team 11 Ops",
	})
}

func TestOrganizationListsMembersBlocksAndProjects(t *testing.T) {
	f, baseURL := newFakeGitHub(t, map[string][]string{
		"/orgs/acme/members":  {`[{"login":"alice"}]`, `[{"login":"bob"}]`},
		"/orgs/acme/blocks":   {`[{"login":"spammer"}]`},
		"/orgs/acme/projects": {`[{"id":5}]`, `[{"id":6}]`},
	})

	got := runGenerator(t, &OrganizationGenerator{}, baseURL)
	assertResources(t, got, []string{
		"github_membership acme:alice alice",
		"github_membership acme:bob bob",
		"github_organization_block spammer spammer",
		"github_organization_project 5 5",
		"github_organization_project 6 6",
	})
	if accept := f.header("/orgs/acme/projects", "Accept"); accept != classicProjectsMediaType {
		t.Errorf("projects Accept = %q, want %q", accept, classicProjectsMediaType)
	}
}

func TestOrganizationWebhooksAndUserSSHKeys(t *testing.T) {
	_, baseURL := newFakeGitHub(t, map[string][]string{
		"/orgs/acme/hooks": {`[{"id":3}]`, `[{"id":4}]`},
		"/user/keys":       {`[{"id":8}]`, `[{"id":9}]`},
	})

	assertResources(t, runGenerator(t, &OrganizationWebhooksGenerator{}, baseURL), []string{
		"github_organization_webhook 3 3",
		"github_organization_webhook 4 4",
	})
	assertResources(t, runGenerator(t, &UserSSHKeyGenerator{}, baseURL), []string{
		"github_user_ssh_key 8 8",
		"github_user_ssh_key 9 9",
	})
}

func TestNewClientBaseURL(t *testing.T) {
	for _, tc := range []struct{ baseURL, want string }{
		{"", githubDefaultURL},
		{githubDefaultURL, githubDefaultURL},
		{"https://ghe.example.com", "https://ghe.example.com/api/v3/"},
		{"https://ghe.example.com/api/v3/", "https://ghe.example.com/api/v3/"},
	} {
		client, err := newClient(tc.baseURL, "secret", 0, 0, "")
		if err != nil {
			t.Fatalf("%q: %v", tc.baseURL, err)
		}
		if got := client.BaseURL(); got != tc.want {
			t.Errorf("base URL for %q = %q, want %q", tc.baseURL, got, tc.want)
		}
	}
}

func TestInitToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "from-env")

	p := &GithubProvider{}
	if err := p.Init([]string{"acme", "", ""}); err != nil {
		t.Fatal(err)
	}
	if p.token != "from-env" || p.baseURL != githubDefaultURL {
		t.Errorf("token %q base URL %q, want GITHUB_TOKEN and the default URL", p.token, p.baseURL)
	}

	p = &GithubProvider{}
	if err := p.Init([]string{"acme", "from-flag", "https://ghe.example.com"}); err != nil {
		t.Fatal(err)
	}
	if p.token != "from-flag" || p.baseURL != "https://ghe.example.com" {
		t.Errorf("token %q base URL %q, want the flags", p.token, p.baseURL)
	}

	t.Setenv("GITHUB_TOKEN", "")
	if err := (&GithubProvider{}).Init([]string{"acme", "", ""}); err == nil {
		t.Error("Init without a token or GitHub App succeeded")
	}
}
