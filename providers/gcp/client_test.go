// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/api/compute/v1"
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

	svc, err := compute.NewService(context.Background(), clientOptions()...)
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
	if len(grpcClientOptions()) != 3 {
		t.Error("gRPC clients ignore the emulator")
	}
}

func TestClientOptionsWithoutAnEmulator(t *testing.T) {
	t.Setenv(EndpointEnv, "")
	if clientOptions() != nil || grpcClientOptions() != nil {
		t.Error("want Google's endpoints and credentials")
	}
}

// Each provider lists with generators of its own: listers keep what they
// list, and a shared one would carry resources from one project or region
// into the next.
func TestSupportedServicesAreNew(t *testing.T) {
	a, b := (&GCPProvider{}).GetSupportedService(), (&GCPProvider{}).GetSupportedService()
	for name, g := range a {
		if g == b[name] {
			t.Errorf("%s: both providers share a generator", name)
		}
	}
}

// Roots are laid out by project and region, and don't get the provider's
// attribution label, which would change every imported resource's labels.
func TestProviderDataAndScope(t *testing.T) {
	p := &GCPProvider{projectName: "acme-prod"}
	p.region.Name = "europe-west1"
	config := p.GetProviderData()["provider"].(map[string]interface{})["google"].(map[string]interface{})
	if config["project"] != "acme-prod" || config["add_terraform_attribution_label"] != false {
		t.Errorf("provider config: %v", config)
	}
	if account, region, err := p.Scope(context.Background()); err != nil || account != "acme-prod" || region != "europe-west1" {
		t.Errorf("scope: %s %s %v", account, region, err)
	}
	if _, region, _ := (&GCPProvider{projectName: "acme-prod"}).Scope(context.Background()); region != "global" {
		t.Errorf("global scope: %s", region)
	}
}
