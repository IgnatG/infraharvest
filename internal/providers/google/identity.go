// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"regexp"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// impersonateKey is the key of an environment's google section, beside
// projects, that names the service account its projects are read as, and
// the prefix of the Init arg that carries it.
const impersonateKey = "impersonate_service_account"

// serviceAccountEmail matches a service account's email address.
var serviceAccountEmail = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+\.gserviceaccount\.com$`)

// IdentityKeys are the keys of an environment's google section, beside
// projects, that name the identity its projects are read with:
// impersonate_service_account, the service account to impersonate with
// the credentials the environment has (Application Default Credentials,
// or GOOGLE_CREDENTIALS), as GOOGLE_IMPERSONATE_SERVICE_ACCOUNT does. It
// is an email address, not a key.
func IdentityKeys() []harvest.IdentityKey {
	return []harvest.IdentityKey{
		{Key: impersonateKey, Match: serviceAccountEmail, Format: "a service account's email address"},
	}
}

// IdentityArg returns the Init arg that names serviceAccount, the service
// account a project is read as.
func IdentityArg(serviceAccount string) string {
	return impersonateKey + "=" + serviceAccount
}

// setImpersonation records the service account project is read as in the
// run, by project ID, as Init was given it, "" for none: the clients of the
// project's listers take their credentials from it (see clientOptions).
// Another run, such as a server's next, may read the same project as
// another account.
func (c *runCaches) setImpersonation(project, account string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if account == "" {
		delete(c.impersonations, project)
	} else {
		c.impersonations[project] = account
	}
}

// impersonation returns the service account project is read as in ctx's
// run, "" for none.
func impersonation(ctx context.Context, project string) string {
	c := cachesOf(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.impersonations[project]
}

// TerraformEnv gives Terraform the service account a project is read as,
// so that the plans of its roots sign in as the listing did; none for a
// project read with the environment's credentials, which Terraform has
// already.
func (p *Provider) TerraformEnv(context.Context) (map[string]string, error) {
	env := map[string]string{}
	if p.serviceAccount != "" {
		env[impersonateEnv] = p.serviceAccount
	}
	return env, nil
}
