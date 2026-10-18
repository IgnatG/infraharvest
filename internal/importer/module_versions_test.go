// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	goversion "github.com/hashicorp/go-version"

	"github.com/infraharvest/infraharvest-cli/internal/adapters"
	"github.com/infraharvest/infraharvest-cli/internal/harvest"
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

// A module version that accepts only an older major release of the
// provider isn't called, rather than hold the root a major behind; one
// that accepts a release of the newest major still holds it back within it.
func TestNewestMajor(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/providers/hashicorp/azurerm/versions" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"versions":[{"version":"4.81.0"},{"version":"5.0.0"},{"version":"5.9.0"},{"version":"5.10.0"},{"version":"6.0.0-beta1"}]}`))
	}))
	defer registry.Close()
	old := moduleConstraint{module: "acme/identity/azurerm", version: "0.5.3", constraint: goversion.MustConstraints(goversion.NewConstraint(">= 4.60.0, < 5.0.0"))}
	within := moduleConstraint{module: "acme/network/azurerm", version: "1.0.0", constraint: goversion.MustConstraints(goversion.NewConstraint(">= 5.0, < 5.10"))}
	none := moduleConstraint{module: "acme/vault/azurerm", version: "2.0.0", constraint: goversion.MustConstraints(goversion.NewConstraint(">= 7.0"))}
	latest := goversion.Must(goversion.NewVersion("5.10.0"))

	admitted, refused := newestMajor(t.Context(), registry.Client(), registry.URL, "registry.terraform.io/hashicorp/azurerm", latest, []moduleConstraint{old, within, none})
	if len(admitted) != 1 || admitted[0].module != within.module {
		t.Errorf("admitted: %+v", admitted)
	}
	if len(refused) != 2 || refused[0].module != old.module || refused[1].module != none.module {
		t.Errorf("refused: %+v", refused)
	}

	list := []adapters.Adapter{
		{Source: "acme/identity/azurerm", Version: "0.5.3"},
		{Source: "acme/identity/azurerm", Version: "0.6.0"},
		{Source: "acme/network/azurerm", Version: "1.0.0"},
	}
	got := withoutModules(list, refused)
	if len(got) != 2 || got[0].Version != "0.6.0" || got[1].Source != "acme/network/azurerm" {
		t.Errorf("adapters kept: %+v", got)
	}
	if got := withoutModules(list, nil); len(got) != 3 {
		t.Errorf("nothing refused: %+v", got)
	}
}

// Only adapters whose anchor the import has apply: the others' modules
// mustn't hold the provider back.
func TestApplicableAdapters(t *testing.T) {
	list := []adapters.Adapter{{Source: "acme/bucket/aws", Anchor: "aws_s3_bucket"}, {Source: "acme/role/aws", Anchor: "aws_iam_role"}}
	selected := map[string][]harvest.Resource{
		"s3":  {harvest.RecordBare("logs", "logs", "aws_s3_bucket", "aws")},
		"ec2": {harvest.RecordBare("i-1", "web", "aws_instance", "aws")},
	}
	got := applicableAdapters(list, selected)
	if len(got) != 1 || got[0].Source != "acme/bucket/aws" {
		t.Errorf("got %+v", got)
	}
	if got := applicableAdapters(list, nil); len(got) != 0 {
		t.Errorf("nothing selected: got %+v", got)
	}
}
