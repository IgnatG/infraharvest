// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	goversion "github.com/hashicorp/go-version"

	"github.com/IgnatG/infraharvest/adapters"
)

func TestResolveAdapters(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/modules/acme/bucket/aws/versions" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"modules":[{"versions":[
			{"version":"1.0.0","root":{"providers":[{"name":"aws","source":"hashicorp/aws","version":">= 5.0"}]}},
			{"version":"2.0.0","root":{"providers":[{"name":"aws","source":"hashicorp/aws","version":">= 6.0, < 6.50"}]}}
		]}]}`))
	}))
	defer registry.Close()
	list := []adapters.Adapter{
		{Source: "acme/bucket/aws", Version: "1.0.0"},
		{Source: "acme/missing/aws", Version: "3.0.0"},
	}

	used, newer, constraints := resolveAdapters(t.Context(), registry.Client(), registry.URL, list, "registry.terraform.io/hashicorp/aws", false)
	if used[0].Version != "1.0.0" || used[1].Version != "3.0.0" {
		t.Errorf("tested versions: %+v", used)
	}
	if len(newer) != 1 || newer[0].Source != "acme/bucket/aws" || newer[0].Latest != "2.0.0" || newer[0].Version != "1.0.0" {
		t.Errorf("newer releases: %+v", newer)
	}
	if len(constraints) != 1 || constraints[0].constraint.String() != ">= 5.0" {
		t.Errorf("constraints of the tested versions: %+v", constraints)
	}

	used, newer, constraints = resolveAdapters(t.Context(), registry.Client(), registry.URL, list, "hashicorp/aws", true)
	if used[0].Version != "2.0.0" || len(newer) != 0 {
		t.Errorf("latest-untested: %+v, %+v", used, newer)
	}
	if list[0].Version != "1.0.0" {
		t.Error("the adapters' tested version changed")
	}
	latest := goversion.Must(goversion.NewVersion("6.52.0"))
	if got := heldBack(latest, constraints); !strings.Contains(got, "6.52.0 is available, but acme/bucket/aws 2.0.0 (") || !strings.HasSuffix(got, ") accepts only older releases") {
		t.Errorf("held back: %q", got)
	}
	if got := heldBack(goversion.Must(goversion.NewVersion("6.49.0")), constraints); got != "" {
		t.Errorf("not held back: %q", got)
	}
}
