// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/compute/v1"

	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

// With an emulator set, Google API calls go there, keeping their paths.
func TestClientOptionsUseTheEmulator(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"name":"main"}]}`))
	}))
	defer server.Close()
	t.Setenv(EndpointEnv, server.URL)

	svc, err := compute.NewService(context.Background(), clientOptions(t.Context())...)
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc.Networks.List("infraharvest-e2e").Do()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Name != "main" {
		t.Errorf("networks: %+v", list.Items)
	}
	if len(paths) != 1 || paths[0] != "/compute/v1/projects/infraharvest-e2e/global/networks" {
		t.Errorf("paths: %v", paths)
	}
}

func TestClientOptionsWithoutAnEmulator(t *testing.T) {
	t.Setenv(EndpointEnv, "")
	if clientOptions(t.Context()) != nil {
		t.Error("want Google's endpoints and credentials")
	}
}

// Two runs in one process, at once, each limit the credentials' client
// at their own run's rate: every call of a run shares one client, and the
// requests of one run never wait for the other's. Run with -race.
func TestLimitedClientIsTheRunsOwn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	base := server.Client()
	runs := []*ratelimit.Limiter{ratelimit.New(1), ratelimit.New(1000)}
	clients := make([]*http.Client, len(runs))
	var wg sync.WaitGroup
	for i, run := range runs {
		wg.Go(func() { clients[i] = limitedBy(run, base) })
	}
	wg.Wait()
	if clients[0] == clients[1] {
		t.Fatal("the runs share a client")
	}
	if limitedBy(runs[0], base) != clients[0] {
		t.Error("a run's calls got another client")
	}
	if limitedBy(runs[0], nil) != nil {
		t.Error("a client without credentials")
	}
	// waited sends two requests through client, the second with a
	// deadline shorter than a second, and reports whether it waited past
	// it.
	waited := func(client *http.Client) bool {
		for i := range 2 {
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
			resp, err := client.Do(req)
			cancel()
			if err != nil {
				return i == 1
			}
			resp.Body.Close()
		}
		return false
	}
	if !waited(clients[0]) {
		t.Error("the run of 1 a second sent two requests at once")
	}
	if waited(clients[1]) {
		t.Error("the run of 1000 a second waited: it has the other run's rate")
	}
}

// Each provider lists with listers of its own: listers keep what they
// list, and a shared one would carry resources from one project or region
// into the next.
func TestSupportedServicesAreNew(t *testing.T) {
	a, b := (&Provider{}).Services(), (&Provider{}).Services()
	for name, g := range a {
		if g == b[name] {
			t.Errorf("%s: both providers share a lister", name)
		}
	}
}

// Roots are laid out by project and region, and don't get the provider's
// attribution label, which would change every imported resource's labels.
func TestProviderBlockAndScope(t *testing.T) {
	p := &Provider{projectName: "acme-prod"}
	p.region.Name = "europe-west1"
	config := p.ProviderBlock()["provider"].(map[string]interface{})["google"].(map[string]interface{})
	if config["project"] != "acme-prod" || config["region"] != "europe-west1" || config["add_terraform_attribution_label"] != false {
		t.Errorf("provider config: %v", config)
	}
	if account, region, err := p.Scope(context.Background()); err != nil || account != "acme-prod" || region != "europe-west1" {
		t.Errorf("scope: %s %s %v", account, region, err)
	}
	if _, region, _ := (&Provider{projectName: "acme-prod"}).Scope(context.Background()); region != "global" {
		t.Errorf("global scope: %s", region)
	}
}
