// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/go-version"
)

func TestRequiredVersionAndProviderConstraint(t *testing.T) {
	v := version.Must(version.NewVersion("1.16.5"))
	if got, want := RequiredVersion(v), ">= 1.16, < 2.0"; got != want {
		t.Errorf("RequiredVersion: got %q, want %q", got, want)
	}
	if got, want := ProviderConstraint(version.Must(version.NewVersion("6.67.2"))), "~> 6.67"; got != want {
		t.Errorf("ProviderConstraint: got %q, want %q", got, want)
	}
}

func TestLatestProviderVersion(t *testing.T) {
	var requested string
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.Path
		// Not sorted, with a prerelease newer than every release.
		_, _ = w.Write([]byte(`{"versions":[{"version":"6.9.0"},{"version":"7.0.0-beta1"},{"version":"6.67.1"},{"version":"6.10.0"}]}`))
	}))
	defer registry.Close()

	got, err := LatestProviderVersion(context.Background(), registry.Client(), registry.URL, "hashicorp/aws")
	if err != nil {
		t.Fatal(err)
	}

	if got.String() != "6.67.1" {
		t.Errorf("got %s, want 6.67.1", got)
	}
	if requested != "/v1/providers/hashicorp/aws/versions" {
		t.Errorf("requested %s", requested)
	}
}

func TestLatestProviderVersionErrors(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "missing") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"versions":[{"version":"1.0.0-rc1"}]}`))
	}))
	defer registry.Close()

	for _, source := range []string{"hashicorp/missing", "hashicorp/prerelease-only", "aws"} {
		if _, err := LatestProviderVersion(context.Background(), registry.Client(), registry.URL, source); err == nil {
			t.Errorf("%s: want an error", source)
		}
	}
}
