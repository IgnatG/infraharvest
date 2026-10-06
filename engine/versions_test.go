// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestLockedVersion(t *testing.T) {
	lock := []byte(`# This file is maintained automatically by "terraform init".
provider "registry.terraform.io/hashicorp/aws" {
  version     = "6.67.0"
  constraints = "~> 6.67"
  hashes = [
    "h1:abc=",
  ]
}
`)
	if got := LockedVersion(lock, "registry.terraform.io/hashicorp/aws"); got != "6.67.0" {
		t.Errorf("got %q, want 6.67.0", got)
	}
	if got := LockedVersion(lock, "registry.opentofu.org/hashicorp/aws"); got != "" {
		t.Errorf("another registry's provider: got %q, want none", got)
	}
	if got := LockedVersion(nil, "registry.terraform.io/hashicorp/aws"); got != "" {
		t.Errorf("no lock file: got %q, want none", got)
	}
}

// A connection that fails, or a registry that answers 429 or 5xx, is tried
// again; a 404 isn't; an answer is asked for once.
func TestLatestProviderVersionRetries(t *testing.T) {
	backoff := registryBackoff
	registryBackoff = time.Millisecond
	t.Cleanup(func() { registryBackoff = backoff })

	var calls atomic.Int32
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		switch {
		case strings.Contains(r.URL.Path, "missing"):
			http.NotFound(w, r)
		case strings.Contains(r.URL.Path, "down"):
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case n == 1:
			http.Error(w, "slow down", http.StatusTooManyRequests)
		case n == 2:
			// The connection drops.
			hijacked, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = hijacked.Close()
			}
		default:
			_, _ = w.Write([]byte(`{"versions":[{"version":"6.1.0"}]}`))
		}
	}))
	defer registry.Close()

	got, err := LatestProviderVersion(context.Background(), registry.Client(), registry.URL, "hashicorp/flaky")
	if err != nil || got.String() != "6.1.0" || calls.Load() != 3 {
		t.Fatalf("got %v, %v after %d calls, want 6.1.0 on the third", got, err, calls.Load())
	}
	if got, err := LatestProviderVersion(context.Background(), registry.Client(), registry.URL, "hashicorp/flaky"); err != nil || got.String() != "6.1.0" || calls.Load() != 3 {
		t.Errorf("asked again: %v, %v after %d calls", got, err, calls.Load())
	}

	calls.Store(10)
	if _, err := LatestProviderVersion(context.Background(), registry.Client(), registry.URL, "hashicorp/missing"); err == nil || calls.Load() != 11 {
		t.Errorf("404: %v after %d calls, want one", err, calls.Load()-10)
	}
	calls.Store(10)
	if _, err := LatestProviderVersion(context.Background(), registry.Client(), registry.URL, "hashicorp/down"); err == nil || calls.Load() != 10+registryAttempts {
		t.Errorf("503: %v after %d calls, want %d", err, calls.Load()-10, registryAttempts)
	}
}
