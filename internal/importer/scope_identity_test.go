// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// A run reads each scope with the identity of the environment that names
// it, and with none for a scope no environment names.
func TestScopeIdentity(t *testing.T) {
	t.Parallel()
	r := &Run{environments: config.Environments{
		"dev":  {Sections: map[string]config.Section{"aws": {"accounts": {"111111111111"}}}, Identities: map[string]config.Identity{"aws": {"profile": "dev"}}},
		"prod": {Sections: map[string]config.Section{"aws": {"accounts": {"222222222222"}}}},
	}}
	identity := r.ScopeIdentity(harvest.Profile{Command: "aws", Scope: harvest.Scope{Field: "accounts"}})
	if got := identity("111111111111"); got["profile"] != "dev" {
		t.Errorf("dev: %v", got)
	}
	for _, scope := range []string{"222222222222", "333333333333", ""} {
		if got := identity(scope); len(got) != 0 {
			t.Errorf("%q: %v", scope, got)
		}
	}
}
