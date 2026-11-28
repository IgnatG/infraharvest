// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	goversion "github.com/hashicorp/go-version"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// fakeTerraformBinary writes an executable that answers terraform version -json.
func fakeTerraformBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "terraform")
	script := "#!/bin/sh\necho '{\"terraform_version\":\"1.9.0\",\"platform\":\"linux_amd64\",\"provider_selections\":{},\"terraform_outdated\":false}'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// A module version whose constraint no release below the provider's cap
// satisfies is declined: the root keeps the cap, pinning the newest release
// under it, rather than pinning the release the cap exists to avoid.
func TestRootConfigKeepsTheCapWhenAModuleAcceptsNothingUnderIt(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/providers/hashicorp/aws/versions" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"versions":[{"version":"6.50.0"},{"version":"6.73.2"},{"version":"6.74.0"},{"version":"6.75.0"}]}`))
	}))
	defer registry.Close()
	needsNewer := moduleConstraint{module: "acme/bucket/aws", version: "3.0.0", constraint: goversion.MustConstraints(goversion.NewConstraint(">= 6.74"))}
	fine := moduleConstraint{module: "acme/role/aws", version: "1.0.0", constraint: goversion.MustConstraints(goversion.NewConstraint(">= 6.0"))}
	p := rootfile.Provider{Name: "aws", Source: "hashicorp/aws"}

	files, err := rootConfig(t.Context(), registry.Client(), "registry.terraform.io", registry.URL, fakeTerraformBinary(t), p, []moduleConstraint{needsNewer, fine})
	if err != nil {
		t.Fatal(err)
	}
	if got := files.version.String(); got != "6.73.2" {
		t.Errorf("pinned %s, want the newest release under the cap, 6.73.2", got)
	}
	if got := files.provider.Version; got != "~> 6.73.2" {
		t.Errorf("provider constraint %q, want one that keeps the cap", got)
	}
	if len(files.refused) != 1 || files.refused[0].module != needsNewer.module {
		t.Errorf("refused %+v, want only %s declined", files.refused, needsNewer.module)
	}
	if !strings.Contains(files.heldBack, "6.74.0") {
		t.Errorf("held back %q, want the cap's reason", files.heldBack)
	}
}
